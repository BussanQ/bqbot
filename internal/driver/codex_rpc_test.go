package driver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestCodexRPCSubprocess is a real duplex subprocess: it checks client
// requests, sends server requests, and refuses to continue without replies.
func TestCodexRPCSubprocess(t *testing.T) {
	scenario := os.Getenv("BQBOT_CODEX_RPC_TEST")
	if scenario == "" {
		return
	}
	log, err := os.Create(os.Getenv("BQBOT_CODEX_RPC_LOG"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	reader := bufio.NewReader(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	read := func() codexRPCMessage {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		_, _ = log.Write(line)
		var m codexRPCMessage
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	send := func(v any) {
		if m, ok := v.(map[string]any); ok && m["id"] != nil && m["method"] != nil {
			if p, ok := m["params"].(map[string]any); ok {
				p["threadId"], p["turnId"] = "t1", "turn1"
				if p["itemId"] == nil {
					p["itemId"] = "request-item"
				}
			}
		}
		if err := enc.Encode(v); err != nil {
			t.Fatal(err)
		}
	}
	response := func(m codexRPCMessage, v any) { send(map[string]any{"id": m.ID, "result": v}) }
	notify := func(method string, p any) { send(map[string]any{"method": method, "params": p}) }
	method := func(want string) codexRPCMessage {
		m := read()
		if m.Method != want {
			t.Fatalf("method=%q want %q", m.Method, want)
		}
		return m
	}
	response(method("initialize"), map[string]any{"userAgent": "fake"})
	method("initialized")
	thread := read()
	expected := "thread/start"
	if strings.HasPrefix(scenario, "resume") {
		expected = "thread/resume"
	}
	if thread.Method != expected {
		t.Fatalf("thread method=%s", thread.Method)
	}
	if scenario == "resume-missing" || scenario == "resume-generic" {
		message := "thread not found: gone"
		if scenario == "resume-generic" {
			message = "authentication failed"
		}
		send(map[string]any{"id": thread.ID, "error": map[string]any{"code": -32000, "message": message}})
		return
	}
	notify("thread/started", map[string]any{"thread": map[string]any{"id": "t1"}})
	response(thread, map[string]any{"thread": map[string]any{"id": "t1"}})
	response(method("turn/start"), map[string]any{"turn": map[string]any{"id": "turn1", "status": "inProgress"}})
	if scenario == "eof" {
		return
	}
	if scenario == "failed" {
		notify("turn/completed", map[string]any{"threadId": "t1", "turn": map[string]any{"id": "turn1", "status": "failed", "error": map[string]any{"message": "usage limit reached"}}})
		return
	}
	if scenario == "cancel" {
		send(map[string]any{"id": 44, "method": "item/commandExecution/requestApproval", "params": map[string]any{"command": "rm -rf example", "itemId": "cmd1"}})
		_ = read() // Driver must cancel the pending approval and terminate this process.
		return
	}
	if scenario == "steer" {
		response(method("turn/steer"), map[string]any{"turnId": "turn1"})
	}
	if scenario == "foreign" {
		for _, scope := range []map[string]any{
			{"threadId": "another-thread", "turnId": "turn1", "itemId": "cmd1"},
			{"threadId": "t1", "turnId": "another-turn", "itemId": "cmd1"},
		} {
			// Encode directly to preserve intentionally incorrect scope IDs.
			if err := enc.Encode(map[string]any{"id": 100, "method": "item/commandExecution/requestApproval", "params": scope}); err != nil {
				t.Fatal(err)
			}
			reply := read()
			if reply.Error == nil || reply.Error.Code != -32602 {
				t.Fatalf("foreign request accepted: %+v", reply)
			}
		}
	}
	if scenario == "approvals" || scenario == "deny" {
		for _, request := range []struct {
			id     any
			method string
			params any
		}{
			{41, "item/commandExecution/requestApproval", map[string]any{"command": "echo hi", "itemId": "cmd1"}},
			{"file-approve", "item/fileChange/requestApproval", map[string]any{"itemId": "edit1"}},
			{43, "item/permissions/requestApproval", map[string]any{"permissions": map[string]any{"network": map[string]any{"enabled": true}}}},
			{44, "item/tool/requestUserInput", map[string]any{"questions": []any{
				map[string]any{"id": "color", "question": "Which color?", "options": []any{map[string]any{"label": "Blue"}, map[string]any{"label": "Red"}}},
				map[string]any{"id": "name", "question": "What name?"},
			}}},
			{45, "unknown/approval", map[string]any{"tool": "shell"}},
		} {
			send(map[string]any{"id": request.id, "method": request.method, "params": request.params})
			reply := read()
			gotID, _ := json.Marshal(request.id)
			if string(reply.ID) != string(gotID) {
				t.Fatalf("reply id=%s want %s", reply.ID, gotID)
			}
			if request.method == "unknown/approval" {
				if reply.Error == nil || reply.Error.Code != -32601 {
					t.Fatalf("unknown request authorized: %+v", reply)
				}
			} else if reply.Error != nil {
				t.Fatalf("approval error %+v", reply.Error)
			}
		}
	}
	notify("item/started", map[string]any{"threadId": "t1", "item": map[string]any{"id": "cmd1", "type": "commandExecution", "command": "ls"}})
	notify("item/agentMessage/delta", map[string]any{"threadId": "t1", "turnId": "turn1", "itemId": "a1", "delta": "Hel"})
	notify("item/agentMessage/delta", map[string]any{"threadId": "t1", "turnId": "turn1", "itemId": "a1", "delta": "lo"})
	notify("item/completed", map[string]any{"threadId": "t1", "item": map[string]any{"id": "a1", "type": "agentMessage", "text": "Hello"}})
	notify("item/completed", map[string]any{"threadId": "t1", "item": map[string]any{"id": "a2", "type": "agentMessage", "text": "Done."}})
	notify("turn/completed", map[string]any{"threadId": "t1", "turn": map[string]any{"id": "turn1", "status": "completed"}})
	// A genuine app-server stays alive after completing the turn. The driver's
	// successful return must clean up the process, not wait for server EOF.
	_ = read()
}

func fakeCodexRPC(t *testing.T, scenario string) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell launcher")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex-rpc")
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(executable, "'", "'\\''") + "' -test.run='^TestCodexRPCSubprocess$' -- \"$@\"\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BQBOT_CODEX_RPC_TEST", scenario)
	t.Setenv("BQBOT_CODEX_RPC_LOG", filepath.Join(dir, "rpc.jsonl"))
	return bin, dir
}

func codexRPCLog(t *testing.T, dir string) []codexRPCMessage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "rpc.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var messages []codexRPCMessage
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var m codexRPCMessage
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		messages = append(messages, m)
	}
	return messages
}

func TestCodexRPCStreamingAndConfig(t *testing.T) {
	bin, dir := fakeCodexRPC(t, "basic")
	in := TurnInput{Text: "hi", System: "Be brief.", Model: "chosen-model", Effort: "high", Dir: dir,
		Settings: EngineSettings{Path: bin}, Attachments: []Attachment{{MIME: "image/png", Data: "aGVsbG8="}},
		History: []HistoryMessage{{Role: "assistant", Text: "Earlier reply"}},
	}
	var events []Event
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := (&Codex{Bin: "does-not-exist"}).Run(ctx, in, func(e Event) { events = append(events, e) }); err != nil {
		t.Fatal(err)
	}
	if text(events) != "HelloDone." {
		t.Fatalf("text=%q", text(events))
	}
	if !slices.Contains(events, Event{Kind: EvSession, Cursor: "t1"}) || !slices.Contains(events, Event{Kind: EvTool, Text: "shell: ls"}) {
		t.Fatalf("events=%v", events)
	}
	for _, m := range codexRPCLog(t, dir) {
		switch m.Method {
		case "thread/start":
			var p map[string]any
			_ = json.Unmarshal(m.Params, &p)
			if p["model"] != "chosen-model" || p["developerInstructions"] != "Be brief." || p["sandbox"] != "read-only" || p["approvalPolicy"] != "untrusted" {
				t.Fatalf("thread params=%s", m.Params)
			}
		case "turn/start":
			s := string(m.Params)
			for _, want := range []string{`"effort":"high"`, `"model":"chosen-model"`, "Earlier reply", "data:image/png;base64,aGVsbG8="} {
				if !strings.Contains(s, want) {
					t.Errorf("turn lacks %q: %s", want, s)
				}
			}
		}
	}
}

func TestCodexRPCApprovals(t *testing.T) {
	for _, mode := range []string{"approvals", "deny", "callback-error", "invalid-outcome"} {
		t.Run(mode, func(t *testing.T) {
			scenario := mode
			if mode != "approvals" {
				scenario = "deny"
			}
			bin, dir := fakeCodexRPC(t, scenario)
			in := TurnInput{Text: "hello", ApprovalMode: "full-auto", Dir: dir}
			var requests []ApprovalRequest
			if mode != "deny" {
				in.Approve = func(ctx context.Context, req ApprovalRequest) (ApprovalDecision, error) {
					requests = append(requests, req)
					if mode == "callback-error" {
						return ApprovalDecision{Outcome: "allow"}, errors.New("UI unavailable")
					}
					if mode == "invalid-outcome" {
						return ApprovalDecision{Outcome: "unexpected"}, nil
					}
					if req.Kind == "question" {
						return ApprovalDecision{Outcome: "answer", Answer: "Blue"}, nil
					}
					return ApprovalDecision{Outcome: "allow"}, nil
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := (&Codex{Bin: bin}).Run(ctx, in, func(Event) {}); err != nil {
				t.Fatal(err)
			}
			if mode != "deny" && len(requests) != 5 {
				t.Fatalf("callback count=%d", len(requests))
			}
			if mode == "approvals" && (!slices.Equal(requests[3].Options, []string{"Blue", "Red"}) || requests[3].Question != "Which color?") {
				t.Fatalf("question=%+v", requests[3])
			}
			for _, m := range codexRPCLog(t, dir) {
				if m.Method != "" {
					continue
				}
				if m.Error != nil {
					if m.Error.Code != -32601 {
						t.Fatalf("unknown=%+v", m.Error)
					}
					continue
				}
				result := string(m.Result)
				if mode == "approvals" {
					if !strings.Contains(result, `"accept"`) && !strings.Contains(result, `"enabled":true`) && !strings.Contains(result, `"Blue"`) {
						t.Fatalf("not approved: %s", result)
					}
				} else if strings.Contains(result, `"accept"`) || strings.Contains(result, `"enabled":true`) || strings.Contains(result, `"Blue"`) {
					t.Fatalf("fail-open result=%s", result)
				}
			}
		})
	}
}

func TestCodexRPCResumeAndErrors(t *testing.T) {
	for _, scenario := range []string{"resume", "resume-missing", "resume-generic", "eof", "failed"} {
		t.Run(scenario, func(t *testing.T) {
			bin, dir := fakeCodexRPC(t, scenario)
			in := TurnInput{Text: "again", Dir: dir}
			if strings.HasPrefix(scenario, "resume") {
				in.Cursor = "existing"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := (&Codex{Bin: bin}).Run(ctx, in, func(Event) {})
			switch scenario {
			case "resume":
				if err != nil {
					t.Fatal(err)
				}
			case "resume-missing":
				if !errors.Is(err, ErrSessionExpired) {
					t.Fatalf("err=%v", err)
				}
			default:
				if !errors.Is(err, ErrTurnFailed) || errors.Is(err, ErrSessionExpired) {
					t.Fatalf("err=%v", err)
				}
			}
			for _, m := range codexRPCLog(t, dir) {
				if m.Method == "thread/resume" && !strings.Contains(string(m.Params), `"threadId":"existing"`) {
					t.Fatalf("resume=%s", m.Params)
				}
			}
		})
	}
}

func TestCodexRPCCancelPendingApproval(t *testing.T) {
	bin, dir := fakeCodexRPC(t, "cancel")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	callbackDone := make(chan struct{})
	in := TurnInput{Text: "hello", Dir: dir, Approve: func(ctx context.Context, req ApprovalRequest) (ApprovalDecision, error) {
		cancel()
		<-ctx.Done()
		close(callbackDone)
		return ApprovalDecision{Outcome: "allow"}, ctx.Err()
	}}
	done := make(chan error, 1)
	go func() { done <- (&Codex{Bin: bin}).Run(ctx, in, func(Event) {}) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel hung")
	}
	select {
	case <-callbackDone:
	case <-time.After(time.Second):
		t.Fatal("callback context not canceled")
	}
}

func TestCodexRPCSteer(t *testing.T) {
	bin, dir := fakeCodexRPC(t, "steer")
	steer := make(chan string, 1)
	steer <- "Also explain why"
	close(steer)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := (&Codex{Bin: bin}).Run(ctx, TurnInput{Text: "hello", Dir: dir, Steer: steer}, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range codexRPCLog(t, dir) {
		if m.Method == "turn/steer" {
			found = true
			if !strings.Contains(string(m.Params), `"expectedTurnId":"turn1"`) || !strings.Contains(string(m.Params), "Also explain why") {
				t.Fatalf("steer=%s", m.Params)
			}
		}
	}
	if !found {
		t.Fatal("missing steer")
	}
}

func TestCodexRPCArgs(t *testing.T) {
	args := (&Codex{}).Args(TurnInput{MCP: testMCP})
	if args[0] != "app-server" {
		t.Fatal(args)
	}
	for _, want := range []string{`mcp_servers={}`, `sandbox_mode="read-only"`, `approval_policy="untrusted"`, `mcp_servers.bqbot.enabled_tools=["ask_bot","list_bots","list_threads","close_thread","list_room_targets"]`} {
		if !slices.Contains(args, want) {
			t.Error(fmt.Sprintf("args lack %s: %v", want, args))
		}
	}
}

func TestCodexRPCRejectAttachmentsBeforeStart(t *testing.T) {
	for _, attachment := range []Attachment{
		{MIME: "text/plain", Data: "aGVsbG8="},
		{MIME: "image/png", Data: "invalid base64!"},
		{MIME: "image/jpeg", Data: ""},
	} {
		for _, history := range []bool{false, true} {
			in := TurnInput{Settings: EngineSettings{Path: filepath.Join(t.TempDir(), "must-not-launch")}}
			if history {
				in.History = []HistoryMessage{{Role: "user", Attachments: []Attachment{attachment}}}
			} else {
				in.Attachments = []Attachment{attachment}
			}
			err := (&Codex{}).Run(context.Background(), in, func(Event) { t.Error("emitted before validation") })
			if err == nil || strings.Contains(err.Error(), "start ") {
				t.Fatalf("attachment %+v history=%v reached subprocess: %v", attachment, history, err)
			}
		}
	}
}

func TestCodexRPCMalformedApprovalsFailClosed(t *testing.T) {
	for _, tc := range []struct{ method, params string }{
		{"item/commandExecution/requestApproval", `null`},
		{"item/commandExecution/requestApproval", `[]`},
		{"item/commandExecution/requestApproval", `{"command":"rm example"}`},
		{"item/commandExecution/requestApproval", `{"threadId":"t1","turnId":"turn1","itemId":"x","command":42}`},
		{"item/commandExecution/requestApproval", `{"threadId":"t1","turnId":"turn1","itemId":"x","kind":"unknown"}`},
		{"item/fileChange/requestApproval", `{}`},
		{"item/permissions/requestApproval", `{"threadId":"t1","turnId":"turn1","itemId":"x","permissions":null}`},
		{"item/permissions/requestApproval", `{"threadId":"t1","turnId":"turn1","itemId":"x","permissions":{"unknown":true}}`},
		{"item/permissions/requestApproval", `{"threadId":"t1","turnId":"turn1","itemId":"x","permissions":{"network":{"enabled":"yes"}}}`},
		{"item/permissions/requestApproval", `{"threadId":"t1","turnId":"turn1","itemId":"x","permissions":{"fileSystem":{"entries":[{"access":"admin","path":{"type":"path","path":"/"}}]}}}`},
		{"item/tool/requestUserInput", `{"threadId":"t1","turnId":"turn1","itemId":"x","questions":[{"question":"No ID"}]}`},
		{"item/tool/requestUserInput", `{"threadId":"t1","turnId":"turn1","itemId":"x","questions":[{"id":"q","question":"?"},{"id":"q","question":"?"}]}`},
		{"item/tool/requestUserInput", `{"threadId":"t1","turnId":"turn1","itemId":"x","questions":[{"id":"q","question":"?","options":[{}]}]}`},
	} {
		t.Run(tc.method+tc.params, func(t *testing.T) {
			reply := codexServerReply(context.Background(), func(context.Context, ApprovalRequest) (ApprovalDecision, error) {
				t.Error("invalid request reached approval policy")
				return ApprovalDecision{Outcome: "allow"}, nil
			}, codexRPCMessage{ID: json.RawMessage(`42`), Method: tc.method, Params: json.RawMessage(tc.params)})
			if reply.Error == nil || reply.Error.Code != -32602 || reply.Result != nil {
				t.Fatalf("malformed request authorized: %+v", reply)
			}
		})
	}
}

func TestCodexRPCRejectForeignApprovalScope(t *testing.T) {
	bin, dir := fakeCodexRPC(t, "foreign")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := (&Codex{Bin: bin}).Run(ctx, TurnInput{Dir: dir, Approve: func(context.Context, ApprovalRequest) (ApprovalDecision, error) {
		t.Error("foreign request reached approval policy")
		return ApprovalDecision{Outcome: "allow"}, nil
	}}, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCodexRPCPermissionGrantIsSingleTurn(t *testing.T) {
	permissions := json.RawMessage(`{"network":{"enabled":true},"fileSystem":{"entries":[{"access":"write","path":{"type":"path","path":"/tmp/example"}},{"access":"read","path":{"type":"special","value":{"kind":"project_roots"}}}]}}`)
	params, err := json.Marshal(map[string]any{"threadId": "t1", "turnId": "turn1", "itemId": "permission1", "permissions": permissions})
	if err != nil {
		t.Fatal(err)
	}
	reply := codexServerReply(context.Background(), func(_ context.Context, request ApprovalRequest) (ApprovalDecision, error) {
		if request.Tool != "permissions" || request.Kind != "tool" {
			t.Errorf("request=%+v", request)
		}
		return ApprovalDecision{Outcome: "allow"}, nil
	}, codexRPCMessage{ID: json.RawMessage(`"approve"`), Method: "item/permissions/requestApproval", Params: params})
	var grant struct {
		Permissions json.RawMessage `json:"permissions"`
		Scope       string          `json:"scope"`
	}
	if reply.Error != nil || json.Unmarshal(reply.Result, &grant) != nil || grant.Scope != "turn" || string(grant.Permissions) != string(permissions) {
		t.Fatalf("invalid grant: %+v", reply)
	}
}

func TestCodexRPCCancelUnresponsiveCallback(t *testing.T) {
	bin, dir := fakeCodexRPC(t, "cancel")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	done := make(chan error, 1)
	go func() {
		done <- (&Codex{Bin: bin}).Run(ctx, TurnInput{Dir: dir, Approve: func(context.Context, ApprovalRequest) (ApprovalDecision, error) {
			close(started)
			<-release // Deliberately ignore the canceled callback context.
			return ApprovalDecision{Outcome: "allow"}, nil
		}}, func(Event) {})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("approval callback did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel result=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("driver waited for unresponsive approval callback")
	}
}
