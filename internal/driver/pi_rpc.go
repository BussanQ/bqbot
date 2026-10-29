package driver

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

//go:embed pi_approval.ts
var piApprovalExtension []byte

type piRPCMessage struct {
	Type    string   `json:"type"`
	ID      string   `json:"id"`
	Command string   `json:"command"`
	Success bool     `json:"success"`
	Error   string   `json:"error"`
	Method  string   `json:"method"`
	Title   string   `json:"title"`
	Message string   `json:"message"`
	Options []string `json:"options"`
	Data    struct {
		SessionFile string `json:"sessionFile"`
		SessionID   string `json:"sessionId"`
		Disposition string `json:"disposition"`
	} `json:"data"`
}

func (d *Pi) runRPC(ctx context.Context, in TurnInput, emit func(Event)) error {
	prompt, err := piPrompt(in)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if in.Cursor != "" && (filepath.IsAbs(in.Cursor) || strings.ContainsAny(in.Cursor, `/\\`)) {
		path := in.Cursor
		if !filepath.IsAbs(path) {
			path = filepath.Join(in.Dir, path)
		}
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: pi session file no longer exists", ErrSessionExpired)
		} else if err != nil {
			return err
		}
	}
	unlock, err := lockPiWorkspace(ctx, in.Dir)
	if err != nil {
		return err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := writeMCPConfig(in.Dir, in.MCP); err != nil {
		return err
	}
	// Keep the bundled permission extension outside project extension discovery.
	extDir, err := os.MkdirTemp("", "bqbot-pi-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(extDir)
	extPath := filepath.Join(extDir, "approval.ts")
	if err := os.WriteFile(extPath, piApprovalExtension, 0600); err != nil {
		return err
	}
	bin := d.Binary()
	if in.Settings.Path != "" {
		bin = in.Settings.Path
	}
	procCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(procCtx, bin, append(d.Args(in), "--extension", extPath)...)
	cmd.Dir = in.Dir
	stderr := &tail{max: 4096}
	cmd.Stderr = stderr
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start pi: %w", err)
	}
	defer func() { stdin.Close(); cancel(); cmd.Wait() }()
	write := json.NewEncoder(stdin).Encode
	lines := make(chan []byte)
	readErr := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 16*1024*1024)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			select {
			case lines <- line:
			case <-procCtx.Done():
				return
			}
		}
		readErr <- scanner.Err()
		close(lines)
	}()
	parser := &piParser{emit: emit}
	ready, started := false, false
	startup := time.NewTimer(15 * time.Second)
	defer startup.Stop()
	var steer <-chan string
	answers := make(chan map[string]any)
	for {
		select {
		case <-ctx.Done():
			// The process context guarantees termination even if abort is ignored.
			return ctx.Err()
		case <-startup.C:
			return fmt.Errorf("%w: pi approval extension did not initialize", ErrTurnFailed)
		case message, ok := <-steer:
			if !ok {
				steer = nil
				continue
			}
			if err := write(map[string]any{"type": "steer", "message": message}); err != nil {
				return err
			}
		case answer := <-answers:
			if err := write(answer); err != nil {
				return err
			}
		case line, ok := <-lines:
			if !ok {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if err := <-readErr; err != nil {
					return fmt.Errorf("pi RPC: %w", err)
				}
				// This is Pi's explicit CLI rejection of an unknown session ID.
				// Generic startup/provider errors must never trigger history replay.
				if !started && in.Cursor != "" && strings.Contains(stderr.String(), "No session found matching '"+in.Cursor+"'") {
					return fmt.Errorf("%w: pi could not find the requested session", ErrSessionExpired)
				}
				return fmt.Errorf("%w: pi RPC ended before the turn settled: %s", ErrTurnFailed, strings.TrimSpace(stderr.String()))
			}
			var envelope struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(line, &envelope); err != nil {
				return fmt.Errorf("%w: invalid pi RPC JSON: %w", ErrTurnFailed, err)
			}
			// Conversation events carry a message object; extension UI requests
			// carry a message string. Decode control fields only for their types.
			msg := piRPCMessage{Type: envelope.Type}
			switch msg.Type {
			case "response", "extension_error", "extension_ui_request":
				if err := json.Unmarshal(line, &msg); err != nil {
					return fmt.Errorf("%w: invalid pi RPC %s: %w", ErrTurnFailed, envelope.Type, err)
				}
			}
			if msg.Type == "extension_error" {
				return fmt.Errorf("%w: pi extension: %s", ErrTurnFailed, msg.Error)
			}
			if msg.Type == "extension_ui_request" {
				if msg.Method == "notify" && msg.Message == "bqbot.approval.ready.v1" {
					if !ready {
						ready = true
						if err := write(map[string]any{"id": "bqbot-state", "type": "get_state"}); err != nil {
							return err
						}
					}
					continue
				}
				go func(msg piRPCMessage) {
					answer := piApprovalResponse(procCtx, in.Approve, msg)
					select {
					case answers <- answer:
					case <-procCtx.Done():
					}
				}(msg)
				continue
			}
			if msg.Type == "response" {
				if !msg.Success {
					return fmt.Errorf("%w: pi %s: %s", ErrTurnFailed, msg.Command, msg.Error)
				}
				if msg.ID == "bqbot-state" && ready && !started {
					if msg.Command != "get_state" || msg.Data.SessionID == "" {
						return fmt.Errorf("%w: pi returned invalid session state", ErrTurnFailed)
					}
					cursor := msg.Data.SessionFile
					if cursor == "" {
						cursor = msg.Data.SessionID
					}
					if in.Cursor != "" && !strings.ContainsAny(in.Cursor, `/\\`) && msg.Data.SessionID != in.Cursor {
						return fmt.Errorf("%w: pi resumed a different session", ErrTurnFailed)
					}
					if in.Cursor != "" && strings.ContainsAny(in.Cursor, `/\\`) {
						requested := in.Cursor
						if !filepath.IsAbs(requested) {
							requested = filepath.Join(in.Dir, requested)
						}
						if msg.Data.SessionFile == "" {
							return fmt.Errorf("%w: pi omitted resumed session file", ErrTurnFailed)
						}
						requestedInfo, requestedErr := os.Stat(requested)
						actualInfo, actualErr := os.Stat(msg.Data.SessionFile)
						if requestedErr != nil || actualErr != nil || !os.SameFile(requestedInfo, actualInfo) {
							return fmt.Errorf("%w: pi resumed a different session file", ErrTurnFailed)
						}
					}
					if cursor != "" {
						emit(Event{Kind: EvSession, Cursor: cursor})
					}
					if err := write(prompt); err != nil {
						return err
					}
					started = true
					steer = in.Steer
					startup.Stop()
				}
				if msg.ID == "bqbot-prompt" && msg.Command == "prompt" && msg.Data.Disposition == "handled" {
					return nil
				}
				continue
			}
			if !started {
				continue
			}
			if err := parser.line(line); err != nil {
				return err
			}
			if parser.settled {
				if parser.lastErr != "" {
					return fmt.Errorf("%w: %s", ErrTurnFailed, parser.lastErr)
				}
				return nil
			}
		}
	}
}

// RPC has no message-history import command. Fresh/recovered sessions receive
// the transcript as labeled context in one prompt, with images in that order.
func piPrompt(in TurnInput) (map[string]any, error) {
	var context strings.Builder
	var images []map[string]string
	addImages := func(attachments []Attachment, label bool) error {
		for _, image := range attachments {
			if _, err := imageDataURL(image); err != nil {
				return err
			}
			images = append(images, map[string]string{"type": "image", "mimeType": image.MIME, "data": image.Data})
			if label {
				fmt.Fprintf(&context, "\n[image %d: %s]", len(images), image.Name)
			}
		}
		return nil
	}
	if in.Cursor == "" && len(in.History) > 0 {
		context.WriteString("Previous conversation (context; continue with the current user message below):\n")
		for _, h := range in.History {
			if h.Role != "user" && h.Role != "assistant" {
				continue
			}
			fmt.Fprintf(&context, "\n%s: %s", h.Role, h.Text)
			if err := addImages(h.Attachments, true); err != nil {
				return nil, err
			}
			context.WriteByte('\n')
		}
		context.WriteString("\nCurrent user message:\n")
	}
	context.WriteString(in.Text)
	if err := addImages(in.Attachments, in.Cursor == "" && len(in.History) > 0); err != nil {
		return nil, err
	}
	prompt := map[string]any{"id": "bqbot-prompt", "type": "prompt", "message": context.String()}
	if len(images) > 0 {
		prompt["images"] = images
	}
	return prompt, nil
}

func piApprovalResponse(ctx context.Context, approve ApproveFunc, msg piRPCMessage) map[string]any {
	response := map[string]any{"type": "extension_ui_response", "id": msg.ID, "cancelled": true}
	if msg.ID == "" || ctx.Err() != nil {
		return response
	}
	request := ApprovalRequest{ID: msg.ID, Kind: "question", Question: msg.Title, Options: msg.Options}
	switch msg.Method {
	case "confirm":
		response = map[string]any{"type": "extension_ui_response", "id": msg.ID, "confirmed": false}
		if msg.Title == "bqbot.tool.v1" {
			if json.Unmarshal([]byte(msg.Message), &request) != nil || request.Kind != "tool" || strings.TrimSpace(request.Tool) == "" {
				return response
			}
			var input map[string]json.RawMessage
			if json.Unmarshal(request.Input, &input) != nil || input == nil {
				return response
			}
			request.ID = msg.ID
		} else {
			request.Question = msg.Title + "\n" + msg.Message
			request.Options = []string{"Allow", "Deny"}
		}
	case "select", "input", "editor":
	default:
		return response
	}
	if approve == nil {
		return response
	}
	decision, err := approve(ctx, request)
	if err != nil || ctx.Err() != nil {
		return response
	}
	if msg.Method == "confirm" {
		response["confirmed"] = (request.Kind == "tool" && decision.Outcome == "allow") || (request.Kind == "question" && decision.Outcome == "answer" && decision.Answer == "Allow")
	} else if decision.Outcome == "answer" {
		if msg.Method == "select" {
			found := false
			for _, option := range msg.Options {
				if option == decision.Answer {
					found = true
					break
				}
			}
			if !found {
				return response
			}
		}
		delete(response, "cancelled")
		response["value"] = decision.Answer
	}
	return response
}
