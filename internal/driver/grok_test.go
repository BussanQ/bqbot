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
	"strings"
	"testing"
	"time"
)

// TestACPHelper is a real interactive subprocess. It waits for each request and
// validates client responses before producing the next protocol message.
func TestACPHelper(t *testing.T) {
	mode := os.Getenv("BQBOT_ACP_HELPER")
	if mode == "" {
		return
	}
	fail := func(message string) { fmt.Fprintln(os.Stderr, message); os.Exit(2) }
	input := bufio.NewScanner(os.Stdin)
	readMessage := func() map[string]any {
		if !input.Scan() {
			fail("client closed stdin")
		}
		var m map[string]any
		if err := json.Unmarshal(input.Bytes(), &m); err != nil {
			fail("bad client JSON")
		}
		return m
	}
	write := func(m any) {
		if err := json.NewEncoder(os.Stdout).Encode(m); err != nil {
			os.Exit(2)
		}
	}
	reply := func(m map[string]any, result any) {
		write(map[string]any{"jsonrpc": "2.0", "id": m["id"], "result": result})
	}
	m := readMessage()
	if m["method"] != "initialize" {
		fail("expected initialize")
	}
	reply(m, map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]bool{"loadSession": mode != "no-load"}})
	m = readMessage()
	if mode == "expired" || mode == "generic" {
		if m["method"] != "session/load" {
			fail("expected load")
		}
		message := "authorization failed"
		if mode == "expired" {
			message = "Session not found"
		}
		write(map[string]any{"jsonrpc": "2.0", "id": m["id"], "error": map[string]any{"code": -32000, "message": message}})
		os.Exit(0)
	}
	if mode == "resume" {
		if m["method"] != "session/load" {
			fail("expected load")
		}
		write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "old replay"}}}})
	} else if m["method"] != "session/new" {
		fail("expected new")
	}
	reply(m, map[string]any{"sessionId": "grok-s", "configOptions": []any{map[string]any{"id": "effort", "options": []any{map[string]string{"value": "high"}}}}})
	m = readMessage()
	if m["method"] != "session/set_model" || m["params"].(map[string]any)["modelId"] != "chosen" {
		fail("expected chosen model")
	}
	reply(m, map[string]any{})
	m = readMessage()
	if m["method"] != "session/set_config_option" {
		fail("expected effort")
	}
	reply(m, map[string]any{})
	m = readMessage()
	if m["method"] != "session/prompt" {
		fail("expected prompt")
	}
	prompt, _ := json.Marshal(m["params"])
	if !strings.Contains(string(prompt), "image/png") {
		fail("missing image")
	}
	if mode == "cancel" {
		write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "cancel now"}}}})
		readMessage()
		os.Exit(2)
	}
	write(map[string]any{"jsonrpc": "2.0", "id": "unknown", "method": "dangerous/newApproval", "params": map[string]any{}})
	if got := readMessage(); got["error"] == nil || got["result"] != nil {
		fail("unknown request was not denied")
	}
	write(map[string]any{"jsonrpc": "2.0", "id": "permission", "method": "session/request_permission", "params": map[string]any{
		"toolCall": map[string]any{"toolCallId": "tool1", "title": "Write", "kind": "edit", "rawInput": map[string]string{"path": "x"}},
		"options":  []any{map[string]string{"optionId": "yes", "kind": "allow_once"}, map[string]string{"optionId": "no", "kind": "reject_once"}},
	}})
	got := readMessage()
	outcome := got["result"].(map[string]any)["outcome"].(map[string]any)
	want := "no"
	if mode == "allow" {
		want = "yes"
	}
	if outcome["optionId"] != want {
		fail("unexpected approval decision")
	}
	write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "hello"}}}})
	reply(m, map[string]string{"stopReason": "end_turn"})
	os.Exit(0)
}

func acpTestInput(t *testing.T, mode string) TurnInput {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell wrapper")
	}
	t.Setenv("BQBOT_ACP_HELPER", mode)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "grok-test")
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(exe, "'", "'\\''") + "' -test.run=^TestACPHelper$ -- \"$@\"\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return TurnInput{Text: "hello", Model: "chosen", Effort: "high", Dir: dir, Settings: EngineSettings{Path: bin}, Attachments: []Attachment{{MIME: "image/png", Data: "aGk="}}}
}

func TestGrokInteractive(t *testing.T) {
	for _, mode := range []string{"allow", "deny", "no-callback", "resume", "callback-error"} {
		t.Run(mode, func(t *testing.T) {
			in := acpTestInput(t, mode)
			calls := 0
			if mode == "resume" {
				in.Cursor = "previous"
			}
			if mode != "no-callback" {
				in.Approve = func(ctx context.Context, request ApprovalRequest) (ApprovalDecision, error) {
					calls++
					if request.Kind != "tool" || request.Tool != "Write" || request.ID != "tool1" {
						t.Errorf("request %+v", request)
					}
					if mode == "callback-error" {
						return ApprovalDecision{Outcome: "allow"}, fmt.Errorf("unavailable")
					}
					outcome := "deny"
					if mode == "allow" {
						outcome = "allow"
					}
					return ApprovalDecision{Outcome: outcome}, nil
				}
			}
			events, err := collect(t, &Grok{Bin: "must-use-custom-path"}, in)
			if err != nil || text(events) != "hello" {
				t.Fatalf("events %v error %v", events, err)
			}
			if mode != "no-callback" && calls != 1 {
				t.Fatalf("callback count %d", calls)
			}
		})
	}
}

func TestGrokSessionExpiration(t *testing.T) {
	for _, mode := range []string{"expired", "generic", "no-load"} {
		t.Run(mode, func(t *testing.T) {
			in := acpTestInput(t, mode)
			in.Cursor = "previous"
			_, err := collect(t, &Grok{}, in)
			if err == nil || errors.Is(err, ErrSessionExpired) != (mode == "expired") {
				t.Fatalf("error %v", err)
			}
		})
	}
}

func TestGrokCancelPendingApproval(t *testing.T) {
	in := acpTestInput(t, "deny")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// A UI callback may take time to observe cancellation. The subprocess must
	// still be released immediately, without waiting for that callback to return.
	release := make(chan struct{})
	defer close(release)
	in.Approve = func(context.Context, ApprovalRequest) (ApprovalDecision, error) {
		cancel()
		<-release
		return ApprovalDecision{Outcome: "allow"}, nil
	}
	done := make(chan error, 1)
	go func() { done <- (&Grok{}).Run(ctx, in, func(Event) {}) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run waited for the approval callback after cancellation")
	}
}

func TestGrokCancel(t *testing.T) {
	in := acpTestInput(t, "cancel")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := (&Grok{}).Run(ctx, in, func(e Event) {
		if e.Kind == EvText {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error %v", err)
	}
}
