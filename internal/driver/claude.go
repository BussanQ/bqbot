package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"bqbot/internal/mcp"
)

// Claude runs turns on the Claude Code CLI in print mode with stream-json
// output. Tools that would need a person's approval are refused
// (--permission-mode dontAsk) until the app has approval cards.
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
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages",
		"--permission-mode", "dontAsk"}
	if in.Model != "" {
		args = append(args, "--model", in.Model)
	}
	if in.System != "" {
		args = append(args, "--append-system-prompt", in.System)
	}
	if in.Cursor != "" {
		args = append(args, "--resume", in.Cursor)
	}
	if s := in.MCP; s != nil {
		cfg, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{mcp.Server: map[string]any{
			"type": "stdio", "command": s.Command, "args": s.Args,
		}}})
		// Only the bqbot server, and only its tools are allowed without asking.
		args = append(args, "--mcp-config", string(cfg), "--strict-mcp-config",
			"--allowedTools", strings.Join(mcpToolNames(), ","))
	}
	return args
}

// mcpToolNames are the bqbot tools as Claude Code and pi name them.
func mcpToolNames() []string {
	names := make([]string, len(mcp.Tools))
	for i, t := range mcp.Tools {
		names[i] = mcp.ToolPrefix + t
	}
	return names
}

func (d *Claude) Run(ctx context.Context, in TurnInput, emit func(Event)) error {
	p := &claudeParser{emit: emit}
	if err := runLines(ctx, d.Binary(), d.Args(in), in.Dir, in.Text, p.line); err != nil {
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
