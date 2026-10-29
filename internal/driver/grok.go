package driver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Grok runs the Grok CLI's Agent Client Protocol server.
type Grok struct{ Bin string }

func (*Grok) Name() string { return "grok" }
func (d *Grok) Binary() string {
	if d.Bin != "" {
		return d.Bin
	}
	return "grok"
}

type acpError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (e *acpError) Error() string { return fmt.Sprintf("ACP error %d: %s", e.Code, e.Message) }

type acpMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *acpError       `json:"error,omitempty"`
}

type acpConnection struct {
	ctx    context.Context
	input  io.Writer
	output *bufio.Scanner
	in     TurnInput
	emit   func(Event)
	seq    int
	stream bool
}

func (c *acpConnection) write(v any) error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	return json.NewEncoder(c.input).Encode(v)
}
func (c *acpConnection) call(method string, params any, dst any) error {
	c.seq++
	id := c.seq
	if err := c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return err
	}
	for c.output.Scan() {
		if err := c.ctx.Err(); err != nil {
			return err
		}
		var msg acpMessage
		if err := json.Unmarshal(c.output.Bytes(), &msg); err != nil {
			return fmt.Errorf("invalid ACP message: %w", err)
		}
		if msg.Method != "" {
			if err := c.incoming(msg); err != nil {
				return err
			}
			continue
		}
		if string(msg.ID) != strconv.Itoa(id) {
			continue
		}
		if msg.Error != nil {
			return msg.Error
		}
		if dst != nil {
			return json.Unmarshal(msg.Result, dst)
		}
		return nil
	}
	if err := c.ctx.Err(); err != nil {
		return err
	}
	if err := c.output.Err(); err != nil {
		return err
	}
	return fmt.Errorf("%w: ACP stream ended before %s completed", ErrTurnFailed, method)
}

func (c *acpConnection) incoming(msg acpMessage) error {
	if len(msg.ID) != 0 && string(msg.ID) != "null" {
		if msg.Method != "session/request_permission" {
			return c.write(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": -32601, "message": "Client method is not supported"}})
		}
		var p struct {
			ToolCall struct {
				ID       string          `json:"toolCallId"`
				Title    string          `json:"title"`
				Kind     string          `json:"kind"`
				RawInput json.RawMessage `json:"rawInput"`
			} `json:"toolCall"`
			Options []struct {
				ID   string `json:"optionId"`
				Kind string `json:"kind"`
			} `json:"options"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return err
		}
		decision := ApprovalDecision{Outcome: "deny"}
		// Permission is granted only for a known option and an explicit callback decision.
		if c.in.Approve != nil {
			tool := p.ToolCall.Title
			if tool == "" {
				tool = p.ToolCall.Kind
			}
			request := ApprovalRequest{ID: p.ToolCall.ID, Kind: "tool", Tool: tool, Input: p.ToolCall.RawInput}
			answers := make(chan ApprovalDecision, 1)
			go func() {
				d, err := c.in.Approve(c.ctx, request)
				if err != nil {
					d = ApprovalDecision{Outcome: "deny"}
				}
				answers <- d
			}()
			select {
			case <-c.ctx.Done():
				return c.ctx.Err()
			case decision = <-answers:
			}
		}
		outcome := map[string]any{"outcome": "cancelled"}
		want := "reject_once"
		if decision.Outcome == "allow" {
			want = "allow_once"
		}
		for _, option := range p.Options {
			if option.Kind == want && option.ID != "" {
				outcome = map[string]any{"outcome": "selected", "optionId": option.ID}
				break
			}
		}
		return c.write(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"outcome": outcome}})
	}
	if msg.Method != "session/update" || !c.stream {
		return nil
	}
	var p struct {
		Update struct {
			Type    string `json:"sessionUpdate"`
			Title   string `json:"title"`
			Content struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"update"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		return err
	}
	switch p.Update.Type {
	case "agent_message_chunk":
		if p.Update.Content.Type == "text" && p.Update.Content.Text != "" {
			c.emit(Event{Kind: EvText, Text: p.Update.Content.Text})
		}
	case "tool_call":
		c.emit(Event{Kind: EvTool, Text: p.Update.Title})
	}
	return nil
}

// acpExpired recognizes explicit session failures only during session/load.
func acpExpired(err error) bool {
	e, ok := err.(*acpError)
	if !ok {
		return false
	}
	var data struct {
		Code string `json:"code"`
		Type string `json:"type"`
	}
	_ = json.Unmarshal(e.Data, &data)
	for _, code := range []string{data.Code, data.Type} {
		switch strings.ToLower(code) {
		case "session_not_found", "session_expired", "sessionnotfound":
			return true
		}
	}
	message := strings.ToLower(strings.TrimSpace(e.Message))
	return message == "session not found" || message == "session expired" || strings.HasPrefix(message, "session not found:")
}

func (d *Grok) Run(ctx context.Context, in TurnInput, emit func(Event)) error {
	bin := d.Binary()
	if in.Settings.Path != "" {
		bin = in.Settings.Path
	}
	cmd := exec.CommandContext(ctx, bin, "--acp")
	cmd.Dir = in.Dir
	cmd.WaitDelay = time.Second
	stderr := &tail{max: 4096}
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start grok: %w", err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	// Close pipes as soon as cancellation arrives, even if a child inherited stdout.
	stopClosing := context.AfterFunc(ctx, func() { _ = stdout.Close(); _ = stdin.Close() })
	defer stopClosing()
	c := &acpConnection{ctx: ctx, input: stdin, output: bufio.NewScanner(stdout), in: in, emit: emit}
	c.output.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var initialized struct {
		AgentCapabilities struct {
			LoadSession bool `json:"loadSession"`
		} `json:"agentCapabilities"`
	}
	if err := c.call("initialize", map[string]any{"protocolVersion": 1, "clientInfo": map[string]string{"name": "bqbot", "version": "1"}, "clientCapabilities": map[string]any{}}, &initialized); err != nil {
		return err
	}
	cwd := in.Dir
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return err
	}
	servers := []any{}
	if in.MCP != nil {
		servers = append(servers, map[string]any{"name": "bqbot", "command": in.MCP.Command, "args": in.MCP.Args, "env": []any{}})
	}
	params := map[string]any{"cwd": cwd, "mcpServers": servers}
	method := "session/new"
	if in.Cursor != "" {
		if !initialized.AgentCapabilities.LoadSession {
			return fmt.Errorf("%w: Grok does not support loading sessions", ErrTurnFailed)
		}
		method = "session/load"
		params["sessionId"] = in.Cursor
	}
	var session struct {
		ID     string `json:"sessionId"`
		Models *struct {
			Current   string `json:"currentModelId"`
			Available []struct {
				ID string `json:"modelId"`
			} `json:"availableModels"`
		} `json:"models"`
		ConfigOptions []struct {
			ID       string `json:"id"`
			Category string `json:"category"`
			Options  []struct {
				Value string `json:"value"`
			} `json:"options"`
		} `json:"configOptions"`
	}
	if err := c.call(method, params, &session); err != nil {
		if method == "session/load" && acpExpired(err) {
			return fmt.Errorf("%w: %v", ErrSessionExpired, err)
		}
		return err
	}
	sessionID := session.ID
	if sessionID == "" {
		sessionID = in.Cursor
	}
	if sessionID == "" {
		return fmt.Errorf("%w: ACP did not return a session ID", ErrTurnFailed)
	}
	if in.Model != "" {
		if err := c.call("session/set_model", map[string]any{"sessionId": sessionID, "modelId": in.Model}, nil); err != nil {
			return err
		}
	}
	if in.Effort != "" {
		// ACP exposes provider-specific effort settings through advertised config options.
		for _, option := range session.ConfigOptions {
			if option.Category != "thought_level" && option.ID != "reasoning_effort" && option.ID != "effort" {
				continue
			}
			for _, value := range option.Options {
				if value.Value == in.Effort {
					if err := c.call("session/set_config_option", map[string]any{"sessionId": sessionID, "configId": option.ID, "value": in.Effort}, nil); err != nil {
						return err
					}
					break
				}
			}
		}
	}
	emit(Event{Kind: EvSession, Cursor: sessionID})
	prompt := []any{}
	if in.System != "" && in.Cursor == "" {
		prompt = append(prompt, map[string]string{"type": "text", "text": in.System})
	}
	// History is supplied by the harness only for a fresh/recovered CLI session.
	if in.Cursor == "" {
		for _, h := range in.History {
			if h.Role != "user" && h.Role != "assistant" {
				continue
			}
			prompt = append(prompt, map[string]string{"type": "text", "text": h.Role + ": " + h.Text})
			for _, a := range h.Attachments {
				if _, err := imageDataURL(a); err != nil {
					return err
				}
				prompt = append(prompt, map[string]string{"type": "image", "data": a.Data, "mimeType": a.MIME})
			}
		}
	}
	prompt = append(prompt, map[string]string{"type": "text", "text": in.Text})
	for _, a := range in.Attachments {
		if _, err := imageDataURL(a); err != nil {
			return err
		}
		prompt = append(prompt, map[string]string{"type": "image", "data": a.Data, "mimeType": a.MIME})
	}
	c.stream = true
	var result struct {
		StopReason string `json:"stopReason"`
	}
	if err := c.call("session/prompt", map[string]any{"sessionId": sessionID, "prompt": prompt}, &result); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if result.StopReason != "end_turn" {
		return fmt.Errorf("%w: ACP stopped with %q", ErrTurnFailed, result.StopReason)
	}
	return nil
}
