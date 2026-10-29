package driver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This subprocess speaks RPC interactively: it will not produce a completion
// until it has received a prompt and the matching permission response.
func TestPiRPCHelperProcess(t *testing.T) {
	if os.Getenv("BQBOT_PI_RPC_HELPER") != "1" {
		return
	}
	mode := os.Getenv("BQBOT_PI_RPC_MODE")
	if mode == "missing-session" {
		fmt.Fprintln(os.Stderr, "No session found matching 'gone'")
		os.Exit(1)
	}
	log, err := os.OpenFile(os.Getenv("BQBOT_PI_RPC_LOG"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		panic(err)
	}
	defer log.Close()
	json.NewEncoder(log).Encode(map[string]any{"args": os.Args})
	for i, arg := range os.Args {
		if arg == "--extension" && i+1 < len(os.Args) && os.Args[i+1] != "builtin:mcp" {
			b, err := os.ReadFile(os.Args[i+1])
			if err != nil || !strings.Contains(string(b), `pi.on("tool_call"`) {
				os.Exit(3)
			}
		}
	}
	out := json.NewEncoder(os.Stdout)
	if mode == "invalid-json" {
		fmt.Fprintln(os.Stdout, "{")
	} else if mode == "invalid-notification" {
		out.Encode(map[string]any{"type": "extension_ui_request", "method": "notify", "message": map[string]any{"unexpected": true}})
	} else if mode == "extension-error" {
		out.Encode(map[string]any{"type": "extension_error", "error": "could not load approval extension"})
	} else {
		out.Encode(map[string]any{"type": "extension_ui_request", "method": "notify", "message": "bqbot.approval.ready.v1"})
	}
	reader := bufio.NewScanner(os.Stdin)
	for reader.Scan() {
		fmt.Fprintln(log, reader.Text())
		var request map[string]any
		if json.Unmarshal(reader.Bytes(), &request) != nil {
			os.Exit(4)
		}
		switch request["type"] {
		case "get_state":
			if mode == "invalid-state" {
				out.Encode(map[string]any{"type": "response", "id": request["id"], "command": "get_state", "success": true})
				continue
			}
			if mode == "state-error" {
				out.Encode(map[string]any{"type": "response", "id": request["id"], "command": "get_state", "success": false, "error": "could not read session database"})
				continue
			}
			out.Encode(map[string]any{"type": "response", "id": request["id"], "command": "get_state", "success": true, "data": map[string]any{"sessionId": "p1"}})
		case "prompt":
			if mode == "provider-error" {
				out.Encode(map[string]any{"type": "response", "command": "prompt", "success": false, "error": "provider temporarily unavailable"})
				continue
			}
			out.Encode(map[string]any{"type": "response", "command": "prompt", "success": true})
			if mode == "message-events" {
				out.Encode(map[string]any{"type": "message_start", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "hello"}}}})
				out.Encode(map[string]any{"type": "message_end", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "hello"}}}})
				out.Encode(map[string]any{"type": "message_start", "message": map[string]any{"role": "assistant", "content": []any{}}})
			}
			if mode == "cancel" || mode == "steer" {
				continue
			}
			out.Encode(map[string]any{"type": "extension_ui_request", "id": "tool-1", "method": "confirm", "title": "bqbot.tool.v1", "message": `{"kind":"tool","tool":"write","input":{"path":"a.txt","content":"hi"}}`})
		case "extension_ui_response":
			out.Encode(map[string]any{"type": "message_update", "message": map[string]any{"role": "assistant", "content": []any{}}, "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": fmt.Sprint(request["confirmed"])}})
			if mode == "message-events" {
				out.Encode(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": fmt.Sprint(request["confirmed"])}}, "stopReason": "stop"}})
			}
			out.Encode(map[string]any{"type": "agent_settled"})
		case "steer":
			out.Encode(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": request["message"]}})
			out.Encode(map[string]any{"type": "agent_settled"})
		}
	}
	os.Exit(0)
}

func piRPCFake(t *testing.T, mode string) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "fake-pi")
	// Test executable paths are quoted for POSIX shells without interpolation.
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(exe, "'", "'\\''") + "' -test.run=^TestPiRPCHelperProcess$ -- \"$@\"\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "rpc.jsonl")
	t.Setenv("BQBOT_PI_RPC_HELPER", "1")
	t.Setenv("BQBOT_PI_RPC_MODE", mode)
	t.Setenv("BQBOT_PI_RPC_LOG", log)
	return bin, dir, log
}

func TestPiRPCApprovalImagesEffortResume(t *testing.T) {
	bin, dir, log := piRPCFake(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var events []Event
	var seen ApprovalRequest
	err := (&Pi{Bin: "does-not-exist"}).Run(ctx, TurnInput{
		Dir: dir, Text: "hello", Cursor: "p1", Model: "openai/gpt-5", Effort: "high", Settings: EngineSettings{Path: bin},
		Attachments: []Attachment{{MIME: "image/png", Data: "aGk="}},
		Approve: func(_ context.Context, req ApprovalRequest) (ApprovalDecision, error) {
			seen = req
			return ApprovalDecision{Outcome: "allow"}, nil
		},
	}, func(ev Event) { events = append(events, ev) })
	if err != nil {
		t.Fatal(err)
	}
	if seen.Kind != "tool" || seen.Tool != "write" || seen.ID != "tool-1" || !strings.Contains(string(seen.Input), "a.txt") {
		t.Fatalf("approval: %+v", seen)
	}
	if text(events) != "true" {
		t.Fatalf("events: %+v", events)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"--mode","rpc"`, `"--session","p1"`, `"--thinking","high"`, `"mimeType":"image/png"`, `"data":"aGk="`, `"message":"hello"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("RPC log missing %q: %s", want, b)
		}
	}
}

func TestPiRPCMessageEvents(t *testing.T) {
	bin, dir, _ := piRPCFake(t, "message-events")
	approvals := 0
	events, err := collect(t, &Pi{Bin: bin}, TurnInput{
		Dir: dir, Text: "hello",
		Approve: func(_ context.Context, req ApprovalRequest) (ApprovalDecision, error) {
			approvals++
			if req.Kind != "tool" || req.Tool != "write" {
				t.Errorf("approval: %+v", req)
			}
			return ApprovalDecision{Outcome: "allow"}, nil
		},
	})
	if err != nil || text(events) != "true" || approvals != 1 {
		t.Fatalf("events=%v approvals=%d error=%v", events, approvals, err)
	}
	breaks := 0
	for _, event := range events {
		if event.Kind == EvBreak {
			breaks++
		}
	}
	if breaks != 1 {
		t.Fatalf("expected one completed assistant message, got %d", breaks)
	}
}

func TestPiRPCMissingApprovalDenies(t *testing.T) {
	bin, dir, _ := piRPCFake(t, "")
	events, err := collect(t, &Pi{Bin: bin}, TurnInput{Dir: dir, Text: "hello", ApprovalMode: "full-auto"})
	if err != nil || text(events) != "false" {
		t.Fatalf("events=%v error=%v", events, err)
	}
}

func TestPiRPCSteer(t *testing.T) {
	bin, dir, _ := piRPCFake(t, "steer")
	steer := make(chan string, 1)
	steer <- "change direction"
	close(steer)
	events, err := collect(t, &Pi{Bin: bin}, TurnInput{Dir: dir, Text: "hello", Steer: steer})
	if err != nil || text(events) != "change direction" {
		t.Fatalf("events=%v error=%v", events, err)
	}
}

func TestPiRPCCancellation(t *testing.T) {
	bin, dir, _ := piRPCFake(t, "cancel")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	err := (&Pi{Bin: bin}).Run(ctx, TurnInput{Dir: dir, Text: "hello"}, func(Event) {})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
}

func TestPiRPCFailureClassification(t *testing.T) {
	for _, mode := range []string{"provider-error", "extension-error", "invalid-state", "state-error", "invalid-json", "invalid-notification"} {
		t.Run(mode, func(t *testing.T) {
			bin, dir, _ := piRPCFake(t, mode)
			_, err := collect(t, &Pi{Bin: bin}, TurnInput{Dir: dir, Text: "hello"})
			if !errors.Is(err, ErrTurnFailed) || errors.Is(err, ErrSessionExpired) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	t.Run("missing-session", func(t *testing.T) {
		_, err := collect(t, &Pi{Bin: "does-not-exist"}, TurnInput{Dir: t.TempDir(), Cursor: "/nonexistent/bqbot-session.jsonl"})
		if !errors.Is(err, ErrSessionExpired) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("wrong-session", func(t *testing.T) {
		bin, dir, _ := piRPCFake(t, "")
		_, err := collect(t, &Pi{Bin: bin}, TurnInput{Dir: dir, Cursor: "gone"})
		if !errors.Is(err, ErrTurnFailed) || errors.Is(err, ErrSessionExpired) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("unknown-session-id", func(t *testing.T) {
		bin, dir, _ := piRPCFake(t, "missing-session")
		_, err := collect(t, &Pi{Bin: bin}, TurnInput{Dir: dir, Cursor: "gone"})
		if !errors.Is(err, ErrSessionExpired) {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestPiRPCFreshHistory(t *testing.T) {
	bin, dir, log := piRPCFake(t, "")
	_, err := collect(t, &Pi{Bin: bin}, TurnInput{Dir: dir, Text: "current turn", History: []HistoryMessage{
		{Role: "user", Text: "prior question", Attachments: []Attachment{{Name: "old.png", MIME: "image/png", Data: "b2xk"}}},
		{Role: "assistant", Text: "prior answer"},
	}, Attachments: []Attachment{{Name: "new.png", MIME: "image/png", Data: "bmV3"}}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"user: prior question", "assistant: prior answer", "Current user message:", "current turn", "image 1: old.png", "image 2: new.png", `"data":"b2xk"`, `"data":"bmV3"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("RPC log missing %q: %s", want, b)
		}
	}
	resumed, err := piPrompt(TurnInput{Cursor: "existing", Text: "current", History: []HistoryMessage{{Role: "user", Text: "do not replay", Attachments: []Attachment{{MIME: "invalid"}}}}})
	if err != nil || resumed["message"] != "current" || resumed["images"] != nil {
		t.Fatalf("resumed prompt=%v error=%v", resumed, err)
	}
}

func TestPiRPCImagesValidateBeforeLaunch(t *testing.T) {
	for _, attachment := range []Attachment{{MIME: "text/plain", Data: "aGk="}, {MIME: "image/png", Data: "invalid base64!"}, {MIME: "image/png"}} {
		for _, historical := range []bool{false, true} {
			in := TurnInput{Dir: t.TempDir()}
			if historical {
				in.History = []HistoryMessage{{Role: "user", Attachments: []Attachment{attachment}}}
			} else {
				in.Attachments = []Attachment{attachment}
			}
			err := (&Pi{Bin: "bqbot-no-such-executable"}).Run(context.Background(), in, func(Event) {})
			if err == nil || strings.Contains(err.Error(), "start pi") {
				t.Fatalf("input was not validated before starting process: %v", err)
			}
		}
	}
}

func TestPiRPCMalformedConfirmFailsClosed(t *testing.T) {
	for _, body := range []string{`{`, `{}`, `{"kind":"tool","tool":"read"}`, `{"kind":"tool","tool":"read","input":null}`, `{"kind":"tool","tool":"read","input":[]}`} {
		called := false
		response := piApprovalResponse(context.Background(), func(context.Context, ApprovalRequest) (ApprovalDecision, error) {
			called = true
			return ApprovalDecision{Outcome: "allow"}, nil
		}, piRPCMessage{ID: "p", Method: "confirm", Title: "bqbot.tool.v1", Message: body})
		if called || response["confirmed"] != false {
			t.Fatalf("malformed approval %q: called=%v response=%v", body, called, response)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	response := piApprovalResponse(ctx, func(context.Context, ApprovalRequest) (ApprovalDecision, error) {
		cancel()
		return ApprovalDecision{Outcome: "allow"}, nil
	}, piRPCMessage{ID: "p", Method: "confirm", Title: "bqbot.tool.v1", Message: `{"kind":"tool","tool":"read","input":{}}`})
	if response["confirmed"] != false {
		t.Fatalf("cancelled approval allowed: %v", response)
	}
	response = piApprovalResponse(context.Background(), func(context.Context, ApprovalRequest) (ApprovalDecision, error) {
		return ApprovalDecision{Outcome: "allow"}, nil
	}, piRPCMessage{ID: "p", Method: "confirm", Title: "unknown permission request"})
	if response["confirmed"] != false {
		t.Fatalf("unknown confirmation auto-approved: %v", response)
	}
}

func TestPiRPCQuestionAndUnknownRequests(t *testing.T) {
	for _, tc := range []struct {
		method, outcome, answer string
		value                   any
		called                  bool
	}{
		{"select", "answer", "yes", "yes", true}, {"input", "answer", "details", "details", true},
		{"editor", "answer", "body", "body", true}, {"select", "answer", "invalid", nil, true},
		{"execute", "allow", "", nil, false}, {"select", "allow", "", nil, true},
	} {
		t.Run(tc.method+tc.outcome+tc.answer, func(t *testing.T) {
			called := false
			resp := piApprovalResponse(context.Background(), func(_ context.Context, req ApprovalRequest) (ApprovalDecision, error) {
				called = true
				if req.Kind != "question" || req.Question != "Question?" {
					t.Fatalf("request=%+v", req)
				}
				return ApprovalDecision{Outcome: tc.outcome, Answer: tc.answer}, nil
			}, piRPCMessage{ID: "q1", Method: tc.method, Title: "Question?", Options: []string{"yes", "no"}})
			if called != tc.called || resp["value"] != tc.value {
				t.Fatalf("called=%v response=%v", called, resp)
			}
			if tc.value == nil && resp["cancelled"] != true {
				t.Fatalf("not closed: %v", resp)
			}
		})
	}
}
