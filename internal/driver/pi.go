package driver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"bqbot/internal/mcp"
)

// Pi runs the coding agent RPC protocol with a bundled tool permission extension.
type Pi struct {
	Bin string // "" is "pi"
}

func (d *Pi) Name() string { return "pi" }

// SupportsSteering reports that active turns accept additional text.
func (*Pi) SupportsSteering() bool { return true }

func (d *Pi) Binary() string {
	if d.Bin != "" {
		return d.Bin
	}
	return "pi"
}

// Args returns CLI options; commands and replies are JSON lines on stdin.
func (d *Pi) Args(in TurnInput) []string {
	args := []string{"--mode", "rpc", "--no-extensions"}
	if in.MCP != nil {
		// An mcp__ entry limits MCP tools to the ones named; --approve
		// trusts the bot's folder, where Run wrote .pi/mcp.json.
		args = append(args, "--extension", "builtin:mcp", "--tools", "read,grep,find,ls,edit,write,bash,"+strings.Join(mcpToolNames(), ","), "--approve")
	} else {
		args = append(args, "--tools", "read,grep,find,ls,edit,write,bash", "--no-mcp")
	}
	if in.Model != "" {
		args = append(args, "--model", in.Model)
	}
	if in.System != "" {
		args = append(args, "--append-system-prompt", in.System)
	}
	if in.Cursor != "" {
		// Resume existing state rather than silently creating a missing session.
		args = append(args, "--session", in.Cursor)
	}
	if in.Effort != "" {
		args = append(args, "--thinking", in.Effort)
	}
	return args
}

// piMCPConfig is the project MCP config pi reads from the bot's folder.
func piMCPConfig(s *MCPServer) ([]byte, error) {
	return json.MarshalIndent(map[string]any{
		"autoEnableCodemode": false,
		"mcpServers": map[string]any{mcp.Server: map[string]any{
			"command":  s.Command,
			"args":     s.Args,
			"exposure": "direct",
			"timeout":  int(mcp.ToolTimeout.Seconds()),
		}},
	}, "", "  ")
}

// writeMCPConfig writes the bot folder's .pi/mcp.json for a turn with the
// bqbot server, and removes it for a turn without.
func writeMCPConfig(dir string, s *MCPServer) error {
	path := filepath.Join(dir, ".pi", "mcp.json")
	if s == nil {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	b, err := piMCPConfig(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func (d *Pi) Run(ctx context.Context, in TurnInput, emit func(Event)) error {
	return d.runRPC(ctx, in, emit)
}

type piLine struct {
	Type    string `json:"type"`
	ID      string `json:"id"` // of the session header
	Message *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Stop    string          `json:"stopReason"`
		Error   string          `json:"errorMessage"`
	} `json:"message"`
	Update *struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
	} `json:"assistantMessageEvent"`
	ToolName   string         `json:"toolName"`
	Args       map[string]any `json:"args"`
	Success    bool           `json:"success"`
	FinalError string         `json:"finalError"`
}

type piParser struct {
	emit     func(Event)
	sawDelta bool // text deltas came for the assistant message in progress
	lastErr  string
	settled  bool
}

func (p *piParser) line(b []byte) error {
	var m piLine
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	switch m.Type {
	case "session":
		if m.ID != "" {
			p.emit(Event{Kind: EvSession, Cursor: m.ID})
		}
	case "message_start":
		if m.Message != nil && m.Message.Role == "assistant" {
			p.sawDelta = false
		}
	case "message_update":
		if u := m.Update; u != nil && u.Type == "text_delta" && u.Delta != "" {
			p.sawDelta = true
			p.emit(Event{Kind: EvText, Text: u.Delta})
		}
	case "message_end":
		msg := m.Message
		if msg == nil || msg.Role != "assistant" {
			return nil
		}
		switch msg.Stop {
		case "error":
			p.lastErr = msg.Error
			if p.lastErr == "" {
				p.lastErr = "the model returned an error"
			}
			return nil
		case "aborted":
			p.lastErr = "pi turn aborted"
			return nil
		}
		p.lastErr = ""
		if !p.sawDelta {
			var blocks []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(msg.Content, &blocks) == nil {
				for _, c := range blocks {
					if c.Type == "text" && c.Text != "" {
						p.emit(Event{Kind: EvText, Text: c.Text})
					}
				}
			}
		}
		p.emit(Event{Kind: EvBreak})
	case "tool_execution_start":
		p.emit(Event{Kind: EvTool, Text: toolLabel(m.ToolName, m.Args)})
	case "auto_retry_end":
		if !m.Success && m.FinalError != "" {
			p.lastErr = m.FinalError
		}
	case "agent_settled":
		p.settled = true
	}
	return nil
}
