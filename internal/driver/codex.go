package driver

import (
	"context"
	"encoding/json"
	"fmt"

	"bqbot/internal/mcp"
)

// Codex runs turns on the Codex CLI (`codex exec --json`), sandboxed
// read-only until the app has approval cards.
type Codex struct {
	Bin string // "" is "codex"
}

func (d *Codex) Name() string { return "codex" }

func (d *Codex) Binary() string {
	if d.Bin != "" {
		return d.Bin
	}
	return "codex"
}

// Args returns the CLI arguments for a turn; the prompt goes on stdin ("-").
func (d *Codex) Args(in TurnInput) []string {
	args := []string{"exec"}
	if in.Cursor != "" {
		args = append(args, "resume")
	}
	args = append(args, "--json", "--skip-git-repo-check", "-c", `sandbox_mode="read-only"`)
	if s := in.MCP; s != nil {
		// -c values are TOML; JSON strings and arrays of strings are valid TOML.
		key := "mcp_servers." + mcp.Server + "."
		args = append(args,
			"-c", key+"command="+tomlValue(s.Command),
			"-c", key+"args="+tomlValue(s.Args),
			"-c", key+"enabled_tools="+tomlValue(mcp.Tools),
			"-c", key+`default_tools_approval_mode="approve"`,
			"-c", fmt.Sprintf("%stool_timeout_sec=%d", key, int(mcp.ToolTimeout.Seconds())))
	}
	if in.Model != "" {
		args = append(args, "-m", in.Model)
	}
	if in.Cursor != "" {
		args = append(args, in.Cursor)
	}
	return append(args, "-")
}

// Prompt is the text sent on stdin: Codex exec has no system prompt flag,
// so a new session starts with the persona.
func (d *Codex) Prompt(in TurnInput) string {
	if in.Cursor == "" && in.System != "" {
		return "Instructions for this whole conversation:\n" + in.System + "\n\n---\n\n" + in.Text
	}
	return in.Text
}

func tomlValue(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (d *Codex) Run(ctx context.Context, in TurnInput, emit func(Event)) error {
	p := &codexParser{emit: emit}
	if err := runLines(ctx, d.Binary(), d.Args(in), in.Dir, d.Prompt(in), p.line); err != nil {
		return err
	}
	if !p.completed {
		if p.lastErr != "" {
			return fmt.Errorf("%w: %s", ErrTurnFailed, p.lastErr)
		}
		return fmt.Errorf("%w: codex ended without completing the turn", ErrTurnFailed)
	}
	return nil
}

type codexLine struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Message  string `json:"message"`
	Error    *struct {
		Message string `json:"message"`
	} `json:"error"`
	Item *struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Command string `json:"command"`
		Server  string `json:"server"`
		Tool    string `json:"tool"`
		Query   string `json:"query"`
		Changes []struct {
			Path string `json:"path"`
		} `json:"changes"`
	} `json:"item"`
}

type codexParser struct {
	emit      func(Event)
	completed bool
	lastErr   string
}

func (p *codexParser) line(b []byte) error {
	var m codexLine
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	switch m.Type {
	case "thread.started":
		if m.ThreadID != "" {
			p.emit(Event{Kind: EvSession, Cursor: m.ThreadID})
		}
	case "item.started":
		if it := m.Item; it != nil {
			switch it.Type {
			case "command_execution":
				p.emit(Event{Kind: EvTool, Text: "shell: " + clip(it.Command, 80)})
			case "mcp_tool_call":
				p.emit(Event{Kind: EvTool, Text: it.Server + "." + it.Tool})
			case "web_search":
				p.emit(Event{Kind: EvTool, Text: "web_search: " + clip(it.Query, 80)})
			}
		}
	case "item.completed":
		if it := m.Item; it != nil {
			switch it.Type {
			case "agent_message":
				p.emit(Event{Kind: EvBreak})
				p.emit(Event{Kind: EvText, Text: it.Text})
			case "file_change":
				for _, c := range it.Changes {
					p.emit(Event{Kind: EvTool, Text: "edit: " + clip(c.Path, 80)})
				}
			}
		}
	case "turn.completed":
		p.completed = true
	case "turn.failed":
		msg := "turn failed"
		if m.Error != nil && m.Error.Message != "" {
			msg = m.Error.Message
		}
		return fmt.Errorf("%w: %s", ErrTurnFailed, msg)
	case "error":
		// Also sent for retries that may still succeed; fatal only if the turn never completes.
		p.lastErr = m.Message
	}
	return nil
}
