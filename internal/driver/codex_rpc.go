package driver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// The app-server uses newline-delimited JSON-RPC, with requests in both
// directions. IDs are raw JSON so numeric and string server IDs round-trip.
type codexRPCMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *codexRPCError  `json:"error,omitempty"`
}

type codexRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type codexRPCRead struct {
	message codexRPCMessage
	err     error
}

func (d *Codex) runRPC(ctx context.Context, in TurnInput, emit func(Event)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Reject invalid attachments before launching any executable or creating a
	// provider session. History attachments are only sent for a new thread.
	attachments := append([]Attachment(nil), in.Attachments...)
	if in.Cursor == "" {
		for _, message := range in.History {
			attachments = append(attachments, message.Attachments...)
		}
	}
	for _, attachment := range attachments {
		if _, err := imageDataURL(attachment); err != nil {
			return err
		}
	}
	runctx, cancel := context.WithCancel(ctx)
	defer cancel()
	bin := d.Binary()
	if in.Settings.Path != "" {
		bin = in.Settings.Path
	}
	cmd := exec.CommandContext(runctx, bin, d.Args(in)...)
	cmd.Dir = in.Dir
	cmd.WaitDelay = time.Second
	stderr := &tail{max: 4096}
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("start %s: %w", bin, err)
	}
	defer func() {
		cancel()
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	messages := make(chan codexRPCRead, 16)
	go func() {
		r := bufio.NewReader(stdout)
		for {
			line, readErr := r.ReadBytes('\n')
			if len(strings.TrimSpace(string(line))) > 0 {
				var m codexRPCMessage
				if err := json.Unmarshal(line, &m); err != nil {
					readErr = fmt.Errorf("invalid app-server JSON: %w", err)
				} else {
					select {
					case messages <- codexRPCRead{message: m}:
					case <-runctx.Done():
						return
					}
				}
			}
			if readErr != nil {
				select {
				case messages <- codexRPCRead{err: readErr}:
				case <-runctx.Done():
				}
				return
			}
		}
	}()
	enc := json.NewEncoder(stdin)
	pending := map[string]string{}
	sequence := 0
	send := func(method string, params any) error {
		sequence++
		id := fmt.Sprintf("bqbot-%d", sequence)
		pending[`"`+id+`"`] = method
		return enc.Encode(map[string]any{"id": id, "method": method, "params": params})
	}
	if err := send("initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "bqbot", "version": "0.1.0"},
		"capabilities": map[string]any{"experimentalApi": true},
	}); err != nil {
		return err
	}
	replies := make(chan codexRPCMessage, 16)
	threadID, turnID := "", ""
	seenSession := ""
	emitSession := func(id string) {
		if id != "" && id != seenSession {
			seenSession = id
			emit(Event{Kind: EvSession, Cursor: id})
		}
	}
	streamed := map[string]bool{}
	lastTextItem := ""
	emitText := func(id, value string) {
		if value == "" {
			return
		}
		if lastTextItem != id {
			emit(Event{Kind: EvBreak})
			lastTextItem = id
		}
		emit(Event{Kind: EvText, Text: value})
	}
	for {
		steer := in.Steer
		if turnID == "" {
			steer = nil
		}
		select {
		case <-ctx.Done():
			// Process cancellation also closes all pipe reads, including when a
			// callback has not returned. No unapproved action can be resumed.
			return ctx.Err()
		case reply := <-replies:
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := enc.Encode(reply); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return err
			}
		case input, ok := <-steer:
			if !ok {
				in.Steer = nil
				continue
			}
			if strings.TrimSpace(input) == "" {
				continue
			}
			if err := send("turn/steer", map[string]any{"threadId": threadID, "expectedTurnId": turnID, "input": codexInput(input, nil)}); err != nil {
				return err
			}
		case incoming := <-messages:
			if incoming.err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				detail := strings.TrimSpace(stderr.String())
				if detail == "" {
					detail = incoming.err.Error()
				}
				return fmt.Errorf("%w: codex app-server ended before turn completion: %s", ErrTurnFailed, detail)
			}
			m := incoming.message
			if m.Method != "" && len(m.ID) > 0 && string(m.ID) != "null" {
				var scope struct {
					ThreadID string `json:"threadId"`
					TurnID   string `json:"turnId"`
				}
				_ = json.Unmarshal(m.Params, &scope)
				if (scope.ThreadID != "" && scope.ThreadID != threadID) || (turnID != "" && scope.TurnID != "" && scope.TurnID != turnID) {
					if err := enc.Encode(codexRPCMessage{ID: m.ID, Error: &codexRPCError{Code: -32602, Message: "request belongs to another thread or turn"}}); err != nil {
						return err
					}
					continue
				}
				// Approval callbacks may wait on UI; keep receiving protocol events and
				// cancellation while the person decides.
				go func(request codexRPCMessage) {
					reply := codexServerReply(runctx, in.Approve, request)
					select {
					case replies <- reply:
					case <-runctx.Done():
					}
				}(m)
				continue
			}
			if m.Method == "" {
				method, ok := pending[string(m.ID)]
				if !ok {
					continue
				}
				delete(pending, string(m.ID))
				if m.Error != nil {
					if method == "thread/resume" && codexMissingSession(m.Error) {
						return fmt.Errorf("%w: %s", ErrSessionExpired, m.Error.Message)
					}
					return fmt.Errorf("%w: %s: %s", ErrTurnFailed, method, m.Error.Message)
				}
				switch method {
				case "initialize":
					if err := enc.Encode(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
						return err
					}
					params := map[string]any{"approvalPolicy": "untrusted", "sandbox": "read-only"}
					if in.Model != "" {
						params["model"] = in.Model
					}
					if in.Dir != "" {
						params["cwd"] = in.Dir
					}
					if in.System != "" {
						params["developerInstructions"] = in.System
					}
					method := "thread/start"
					if in.Cursor != "" {
						method = "thread/resume"
						params["threadId"] = in.Cursor
					}
					if err := send(method, params); err != nil {
						return err
					}
				case "thread/start", "thread/resume":
					var result struct {
						Thread struct {
							ID string `json:"id"`
						} `json:"thread"`
					}
					if err := json.Unmarshal(m.Result, &result); err != nil || result.Thread.ID == "" {
						return fmt.Errorf("%w: missing codex thread ID", ErrTurnFailed)
					}
					threadID = result.Thread.ID
					emitSession(threadID)
					input := codexInput(in.Text, in.Attachments)
					if in.Cursor == "" && len(in.History) > 0 {
						input = nil
						input = append(input, map[string]any{"type": "text", "text": "Previous conversation (quoted transcript for context):"})
						for _, history := range in.History {
							input = append(input, codexInput(history.Role+": "+history.Text, history.Attachments)...)
						}
						input = append(input, codexInput("Current user message:\n"+in.Text, in.Attachments)...)
					}
					params := map[string]any{"threadId": threadID, "input": input}
					if in.Model != "" {
						params["model"] = in.Model
					}
					if in.Effort != "" {
						params["effort"] = in.Effort
					}
					if err := send("turn/start", params); err != nil {
						return err
					}
				case "turn/start":
					var result struct {
						Turn struct {
							ID string `json:"id"`
						} `json:"turn"`
					}
					if err := json.Unmarshal(m.Result, &result); err != nil {
						return fmt.Errorf("%w: invalid turn response", ErrTurnFailed)
					}
					if result.Turn.ID != "" {
						turnID = result.Turn.ID
					}
				}
				continue
			}
			var p struct {
				ThreadID  string `json:"threadId"`
				TurnID    string `json:"turnId"`
				ItemID    string `json:"itemId"`
				Delta     string `json:"delta"`
				WillRetry bool   `json:"willRetry"`
				Error     *struct {
					Message string `json:"message"`
				} `json:"error"`
				Thread struct {
					ID string `json:"id"`
				} `json:"thread"`
				Turn struct {
					ID     string `json:"id"`
					Status string `json:"status"`
					Error  *struct {
						Message string `json:"message"`
					} `json:"error"`
				} `json:"turn"`
				Item struct {
					ID      string `json:"id"`
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
			if err := json.Unmarshal(m.Params, &p); err != nil {
				return fmt.Errorf("%w: invalid %s notification", ErrTurnFailed, m.Method)
			}
			if threadID != "" && p.ThreadID != "" && p.ThreadID != threadID {
				continue
			}
			if turnID != "" && p.TurnID != "" && p.TurnID != turnID {
				continue
			}
			switch m.Method {
			case "thread/started":
				// The response is authoritative; don't persist a cursor before resume
				// succeeds, or a rejected continuation could replace the saved cursor.
			case "turn/started":
				turnID = p.Turn.ID
			case "item/agentMessage/delta":
				streamed[p.ItemID] = true
				emitText(p.ItemID, p.Delta)
			case "item/started":
				switch p.Item.Type {
				case "commandExecution":
					emit(Event{Kind: EvTool, Text: "shell: " + clip(p.Item.Command, 80)})
				case "mcpToolCall":
					emit(Event{Kind: EvTool, Text: p.Item.Server + "." + p.Item.Tool})
				case "webSearch":
					emit(Event{Kind: EvTool, Text: "web_search: " + clip(p.Item.Query, 80)})
				}
			case "item/completed":
				switch p.Item.Type {
				case "agentMessage":
					if !streamed[p.Item.ID] {
						emitText(p.Item.ID, p.Item.Text)
					}
				case "fileChange":
					for _, change := range p.Item.Changes {
						emit(Event{Kind: EvTool, Text: "edit: " + clip(change.Path, 80)})
					}
				}
			case "error":
				if !p.WillRetry && p.Error != nil {
					return fmt.Errorf("%w: %s", ErrTurnFailed, p.Error.Message)
				}
			case "turn/completed":
				if turnID != "" && p.Turn.ID != "" && p.Turn.ID != turnID {
					continue
				}
				if p.Turn.Status != "completed" {
					detail := p.Turn.Status
					if p.Turn.Error != nil {
						detail = p.Turn.Error.Message
					}
					return fmt.Errorf("%w: codex turn %s", ErrTurnFailed, detail)
				}
				return nil
			}
		}
	}
}

func codexInput(text string, attachments []Attachment) []map[string]any {
	input := []map[string]any{{"type": "text", "text": text}}
	for _, image := range attachments {
		input = append(input, map[string]any{"type": "image", "url": "data:" + image.MIME + ";base64," + image.Data})
	}
	return input
}

func codexMissingSession(err *codexRPCError) bool {
	// Only classify explicit missing-session diagnostics during thread/resume.
	// Authentication, rate limits, corrupt rollouts, and transport failures must
	// never trigger transcript replay and potentially repeat tool side effects.
	text := strings.ToLower(err.Message)
	for _, phrase := range []string{"thread not found", "thread does not exist", "session not found", "session expired", "no rollout found for thread"} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	var data struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(err.Data, &data)
	return data.Code == "thread_not_found" || data.Code == "session_expired"
}

// Reject unfamiliar permission shapes instead of echoing arbitrary JSON as an
// authorization. Keep this narrower than general app-server notifications.
func codexValidPermissions(raw json.RawMessage) bool {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return false
	}
	var permissions struct {
		Network *struct {
			Enabled *bool `json:"enabled"`
		} `json:"network"`
		FileSystem *struct {
			Read             []string `json:"read"`
			Write            []string `json:"write"`
			GlobScanMaxDepth *uint    `json:"globScanMaxDepth"`
			Entries          []struct {
				Access string `json:"access"`
				Path   struct {
					Type    string  `json:"type"`
					Path    *string `json:"path"`
					Pattern *string `json:"pattern"`
					Value   *struct {
						Kind    string  `json:"kind"`
						Path    *string `json:"path"`
						Subpath *string `json:"subpath"`
					} `json:"value"`
				} `json:"path"`
			} `json:"entries"`
		} `json:"fileSystem"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&permissions) != nil {
		return false
	}
	if fs := permissions.FileSystem; fs != nil {
		if fs.GlobScanMaxDepth != nil && *fs.GlobScanMaxDepth == 0 {
			return false
		}
		for _, paths := range [][]string{fs.Read, fs.Write} {
			for _, path := range paths {
				if path == "" {
					return false
				}
			}
		}
		for _, entry := range fs.Entries {
			if entry.Access != "read" && entry.Access != "write" && entry.Access != "deny" {
				return false
			}
			path := entry.Path
			switch path.Type {
			case "path":
				if path.Path == nil || *path.Path == "" || path.Pattern != nil || path.Value != nil {
					return false
				}
			case "glob_pattern":
				if path.Pattern == nil || *path.Pattern == "" || path.Path != nil || path.Value != nil {
					return false
				}
			case "special":
				if path.Value == nil || path.Path != nil || path.Pattern != nil {
					return false
				}
				switch path.Value.Kind {
				case "root", "minimal", "project_roots", "tmpdir", "slash_tmp":
				default:
					return false
				}
			default:
				return false
			}
		}
	}
	return true
}

func codexServerReply(ctx context.Context, approve ApproveFunc, request codexRPCMessage) codexRPCMessage {
	reply := codexRPCMessage{ID: request.ID}
	result := func(value any) codexRPCMessage { reply.Result, _ = json.Marshal(value); return reply }
	failure := func(code int, message string) codexRPCMessage {
		reply.Error = &codexRPCError{Code: code, Message: message}
		return reply
	}
	var id string
	if json.Unmarshal(request.ID, &id) != nil {
		id = string(request.ID)
	}
	ask := func(req ApprovalRequest) ApprovalDecision {
		if approve == nil || ctx.Err() != nil {
			return ApprovalDecision{Outcome: "deny"}
		}
		answer, err := approve(ctx, req)
		if err != nil || ctx.Err() != nil {
			return ApprovalDecision{Outcome: "deny"}
		}
		return answer
	}
	// Validate the shared routing fields before exposing a supported request to
	// approval policies. Malformed or context-free tool requests must not become
	// authorized merely because a policy allows all edits or all known tools.
	validScope := func() bool {
		var p struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			ItemID   string `json:"itemId"`
		}
		return json.Unmarshal(request.Params, &p) == nil && p.ThreadID != "" && p.TurnID != "" && p.ItemID != ""
	}
	switch request.Method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		var params struct {
			Command *string `json:"command"`
			Kind    string  `json:"kind"`
		}
		if !validScope() || json.Unmarshal(request.Params, &params) != nil || (params.Kind != "" && params.Kind != "command" && params.Kind != "writeStdin") {
			return failure(-32602, "invalid tool approval request")
		}
		tool := "shell"
		if request.Method == "item/fileChange/requestApproval" {
			tool = "edit"
		}
		decision := ask(ApprovalRequest{ID: id, Kind: "tool", Tool: tool, Input: request.Params})
		outcome := "decline"
		if decision.Outcome == "allow" {
			outcome = "accept"
		}
		return result(map[string]string{"decision": outcome})
	case "item/permissions/requestApproval":
		var params struct {
			Permissions json.RawMessage `json:"permissions"`
		}
		if !validScope() || json.Unmarshal(request.Params, &params) != nil || !codexValidPermissions(params.Permissions) {
			return failure(-32602, "invalid permission request")
		}
		decision := ask(ApprovalRequest{ID: id, Kind: "tool", Tool: "permissions", Input: request.Params})
		permissions := json.RawMessage(`{}`)
		if decision.Outcome == "allow" {
			permissions = params.Permissions
		}
		return result(map[string]any{"permissions": permissions, "scope": "turn"})
	case "item/tool/requestUserInput":
		var params struct {
			Questions []struct {
				ID       string `json:"id"`
				Question string `json:"question"`
				Options  []struct {
					Label string `json:"label"`
				} `json:"options"`
			} `json:"questions"`
		}
		if !validScope() || json.Unmarshal(request.Params, &params) != nil || len(params.Questions) == 0 {
			return failure(-32602, "invalid user input request")
		}
		seen := map[string]bool{}
		for _, question := range params.Questions {
			if question.ID == "" || strings.TrimSpace(question.Question) == "" || seen[question.ID] {
				return failure(-32602, "invalid or duplicate question")
			}
			seen[question.ID] = true
			for _, option := range question.Options {
				if strings.TrimSpace(option.Label) == "" {
					return failure(-32602, "invalid question option")
				}
			}
		}
		answers := map[string]any{}
		for _, question := range params.Questions {
			options := make([]string, 0, len(question.Options))
			for _, option := range question.Options {
				options = append(options, option.Label)
			}
			decision := ask(ApprovalRequest{ID: id + ":" + question.ID, Kind: "question", Tool: "request_user_input", Input: request.Params, Question: question.Question, Options: options})
			values := []string{}
			if decision.Outcome == "answer" {
				values = append(values, decision.Answer)
			}
			answers[question.ID] = map[string]any{"answers": values}
		}
		return result(map[string]any{"answers": answers})
	default:
		// Never interpret unknown methods, dynamic tools, auth refreshes or
		// unfamiliar approval types as an authorization request we can accept.
		return failure(-32601, "unsupported server request: "+request.Method)
	}
}
