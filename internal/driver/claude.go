package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"bqbot/internal/mcp"
)

// Claude runs print-mode stream-json turns with a separate permission MCP server.
type Claude struct {
	Bin string // "" is "claude"
}

func (d *Claude) Name() string { return "claude" }

func (d *Claude) Binary() string {
	if d.Bin != "" {
		return d.Bin
	}
	return "claude"
}

// Args returns the CLI arguments for a turn; the prompt goes on stdin.
func (d *Claude) Args(in TurnInput) []string {
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages"}
	// Keep permission handling with the bridge even for automatic modes: the
	// harness applies their policy. Never bypass permissions at the CLI level.
	if in.ApprovalMCP == nil {
		args = append(args, "--permission-mode", "dontAsk")
	} else {
		args = append(args, "--permission-mode", "manual", "--permission-prompt-tool", mcp.ApprovalToolName)
	}
	if in.Model != "" {
		args = append(args, "--model", in.Model)
	}
	if in.Effort != "" {
		args = append(args, "--effort", in.Effort)
	}
	if in.System != "" {
		args = append(args, "--append-system-prompt", in.System)
	}
	if in.Cursor != "" {
		args = append(args, "--resume", in.Cursor)
	}
	if claudeStructuredInput(in) {
		args = append(args, "--input-format", "stream-json")
	}
	servers := map[string]any{}
	allowed := []string{}
	for name, server := range map[string]*MCPServer{mcp.Server: in.MCP, mcp.ApprovalServer: in.ApprovalMCP} {
		if server == nil {
			continue
		}
		servers[name] = map[string]any{"type": "stdio", "command": server.Command, "args": server.Args}
	}
	if in.MCP != nil {
		allowed = append(allowed, mcpToolNames(in.MCP)...)
	}
	if in.ApprovalMCP != nil {
		allowed = append(allowed, mcp.ApprovalToolName)
	}
	cfg, _ := json.Marshal(map[string]any{"mcpServers": servers})
	args = append(args, "--mcp-config", string(cfg), "--strict-mcp-config", "--setting-sources", "")
	if len(allowed) > 0 {
		args = append(args, "--allowedTools", strings.Join(allowed, ","))
	}

	return args
}

// mcpToolNames are the bqbot tools as Claude Code and pi name them.
func mcpToolNames(servers ...*MCPServer) []string {
	tools := append([]string{}, mcp.Tools...)
	for _, server := range servers {
		if server != nil {
			tools = append(tools, server.Tools...)
		}
	}
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = mcp.ToolPrefix + t
	}
	return names
}

func (d *Claude) Run(ctx context.Context, in TurnInput, emit func(Event)) error {
	prompt, err := claudeInput(in)
	if err != nil {
		return err
	}
	p := &claudeParser{emit: emit}
	bin := d.Binary()
	if in.Settings.Path != "" {
		bin = in.Settings.Path
	}
	if err := runLines(ctx, bin, d.Args(in), in.Dir, prompt, p.line); err != nil {
		if in.Cursor != "" && !p.sawText && !p.sawTool && !errors.Is(err, context.Canceled) && claudeSessionMissing(err.Error()) {
			return fmt.Errorf("%w: %v", ErrSessionExpired, err)
		}
		return err
	}

	if !p.done {
		return fmt.Errorf("%w: claude ended without a result", ErrTurnFailed)
	}
	return nil
}

type claudeLine struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Event     *struct {
		Type  string `json:"type"`
		Delta *struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	} `json:"event"`
	Message *struct {
		Content []struct {
			Type  string         `json:"type"`
			Text  string         `json:"text"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
	} `json:"message"`
	Result  string   `json:"result"`
	IsError bool     `json:"is_error"`
	Errors  []string `json:"errors"`
}

type claudeParser struct {
	emit     func(Event)
	sawDelta bool // text deltas came for the assistant message in progress
	sawText  bool
	sawTool  bool
	done     bool
}

func (p *claudeParser) line(b []byte) error {
	var m claudeLine
	if json.Unmarshal(b, &m) != nil {
		return nil // the CLI prints a few lines that are not JSON
	}
	switch m.Type {
	case "system":
		if m.Subtype == "init" && m.SessionID != "" {
			p.emit(Event{Kind: EvSession, Cursor: m.SessionID})
		}
	case "stream_event":
		if e := m.Event; e != nil && e.Type == "content_block_delta" && e.Delta != nil && e.Delta.Type == "text_delta" {
			p.sawDelta, p.sawText = true, true
			p.emit(Event{Kind: EvText, Text: e.Delta.Text})
		}
	case "assistant":
		if m.Message == nil {
			return nil
		}
		for _, c := range m.Message.Content {
			switch c.Type {
			case "text":
				if !p.sawDelta && c.Text != "" {
					p.sawText = true
					p.emit(Event{Kind: EvText, Text: c.Text})
				}
			case "tool_use":
				p.sawTool = true
				p.emit(Event{Kind: EvTool, Text: toolLabel(c.Name, c.Input)})
			}
		}
		p.sawDelta = false
		p.emit(Event{Kind: EvBreak})
	case "result":
		p.done = true
		if m.SessionID != "" {
			p.emit(Event{Kind: EvSession, Cursor: m.SessionID})
		}
		if m.IsError || m.Subtype != "success" {
			msg := m.Result
			if msg == "" && len(m.Errors) > 0 {
				msg = m.Errors[0]
			}
			if msg == "" {
				msg = m.Subtype
			}
			return fmt.Errorf("%w: %s", ErrTurnFailed, msg)
		}
		if !p.sawText && m.Result != "" {
			p.emit(Event{Kind: EvText, Text: m.Result})
		}
	}
	return nil
}

// toolLabel describes a tool call in one short line.
func toolLabel(name string, input map[string]any) string {
	for _, k := range []string{"command", "file_path", "path", "pattern", "url", "query", "description"} {
		if s, ok := input[k].(string); ok && s != "" {
			return name + ": " + clip(s, 80)
		}
	}
	return name
}

func claudeSessionMissing(message string) bool {
	s := strings.ToLower(message)
	return strings.Contains(s, "no conversation found with session id") ||
		strings.Contains(s, "session not found") || strings.Contains(s, "session has expired")
}

func claudeStructuredInput(in TurnInput) bool {
	return len(in.Attachments) > 0 || (in.Cursor == "" && len(in.History) > 0)
}

// Replay is sent as context in one user prompt, never as independent turns
// which could execute historical tools again.
func claudeInput(in TurnInput) (string, error) {
	if !claudeStructuredInput(in) {
		return in.Text, nil
	}
	content := []any{}
	addText := func(s string) { content = append(content, map[string]any{"type": "text", "text": s}) }
	addImages := func(images []Attachment) error {
		for _, image := range images {
			if _, err := imageDataURL(image); err != nil {
				return err
			}

			content = append(content, map[string]any{"type": "image", "source": map[string]any{
				"type": "base64", "media_type": image.MIME, "data": image.Data,
			}})
		}
		return nil
	}
	if in.Cursor == "" && len(in.History) > 0 {
		addText("Previous conversation (context only):")
		for _, h := range in.History {
			addText(h.Role + ": " + h.Text)
			if err := addImages(h.Attachments); err != nil {
				return "", err
			}
		}
		addText("Current user message:")
	}
	addText(in.Text)
	if err := addImages(in.Attachments); err != nil {
		return "", err
	}
	b, err := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": content}})
	return string(b) + "\n", err
}
