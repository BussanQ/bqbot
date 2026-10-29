package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

type approvalBackend struct {
	fakeBackend
	approve func(context.Context, ApprovalRequest) (ApprovalDecision, error)
}

func (b *approvalBackend) ApprovalRestricted() bool { return true }
func (b *approvalBackend) Approve(ctx context.Context, r ApprovalRequest) (ApprovalDecision, error) {
	return b.approve(ctx, r)
}

func permission(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	text, isErr := resultText(t, map[string]any{"result": result})
	if isErr {
		t.Fatal(text)
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(text), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestApprovalScopeAndDecision(t *testing.T) {
	var got ApprovalRequest
	b := &approvalBackend{approve: func(_ context.Context, r ApprovalRequest) (ApprovalDecision, error) {
		got = r
		return ApprovalDecision{Outcome: "allow"}, nil
	}}
	send, recv := session(t, b)
	send(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	ts := recv()["result"].(map[string]any)["tools"].([]any)
	if len(ts) != 1 || ts[0].(map[string]any)["name"] != ToolApprove {
		t.Fatalf("tools: %v", ts)
	}
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ask_bot","arguments":{"name":"Bo","question":"q"}}}`)
	if _, bad := resultText(t, recv()); !bad {
		t.Fatal("approval-only grant exposed handoff")
	}
	p := permission(t, call(context.Background(), b, json.RawMessage(`{"name":"approve_tool","arguments":{"tool_name":"Bash","tool_use_id":"u1","input":{"command":"echo x"}}}`)))
	if p["behavior"] != "allow" || p["updatedInput"].(map[string]any)["command"] != "echo x" || got.Tool != "Bash" || got.ID != "u1" || got.Kind != "tool" {
		t.Fatalf("permission %v request %+v", p, got)
	}
	for _, outcome := range []string{"deny", "answer", "unknown", ""} {
		b.approve = func(context.Context, ApprovalRequest) (ApprovalDecision, error) {
			return ApprovalDecision{Outcome: outcome}, nil
		}
		p = permission(t, call(context.Background(), b, json.RawMessage(`{"name":"approve_tool","arguments":{"tool_name":"Bash","input":{}}}`)))
		if p["behavior"] != "deny" {
			t.Fatalf("outcome %s: %v", outcome, p)
		}
	}
	b.approve = func(context.Context, ApprovalRequest) (ApprovalDecision, error) {
		return ApprovalDecision{}, errors.New("unavailable")
	}
	if p := permission(t, call(context.Background(), b, json.RawMessage(`{"name":"approve_tool","arguments":{"tool_name":"Bash","input":{}}}`))); p["behavior"] != "deny" {
		t.Fatal(p)
	}
}

func TestApprovalQuestions(t *testing.T) {
	var got []ApprovalRequest
	b := &approvalBackend{approve: func(_ context.Context, r ApprovalRequest) (ApprovalDecision, error) {
		got = append(got, r)
		return ApprovalDecision{Outcome: "answer", Answer: "custom answer"}, nil
	}}
	p := permission(t, call(context.Background(), b, json.RawMessage(`{"name":"approve_tool","arguments":{"tool_name":"AskUserQuestion","tool_use_id":"q1","input":{"questions":[{"question":"Which?","options":[{"label":"A"},{"label":"B"}]},{"question":"Why?"}],"metadata":{"keep":true}}}}`)))
	if p["behavior"] != "allow" || len(got) != 2 || got[0].Kind != "question" || !reflect.DeepEqual(got[0].Options, []string{"A", "B"}) || got[0].ID == got[1].ID {
		t.Fatalf("permission %v requests %+v", p, got)
	}
	updated := p["updatedInput"].(map[string]any)
	if updated["answers"].(map[string]any)["Which?"] != "custom answer" || updated["metadata"] == nil {
		t.Fatal(updated)
	}
}

func TestApprovalHTTPWire(t *testing.T) {
	expected := ApprovalRequest{ID: "x", Kind: "question", Tool: "AskUserQuestion", Input: json.RawMessage(`{"x":1}`), Question: "Which?", Options: []string{"A"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/approve" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("bad request %s %s", r.Method, r.URL.Path)
		}
		var got ApprovalRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		if !reflect.DeepEqual(got, expected) {
			t.Errorf("got %+v", got)
		}
		json.NewEncoder(w).Encode(ApprovalDecision{Outcome: "answer", Answer: "A"})
	}))
	defer srv.Close()
	b := &HTTPBackend{URL: srv.URL + "/", Token: "secret", ApprovalOnly: true}
	got, err := b.Approve(context.Background(), expected)
	if err != nil || got.Outcome != "answer" || got.Answer != "A" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestApprovalHTTPFailClosed(t *testing.T) {
	for _, body := range []string{`{"outcome":"wat"}`, `{"text":"allow"}`, `not json`} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
			defer srv.Close()
			b := &HTTPBackend{URL: srv.URL, Token: "tok", ApprovalOnly: true}
			if _, err := b.Approve(context.Background(), ApprovalRequest{}); err == nil {
				t.Fatal("accepted invalid response")
			}
		})
	}
}

func TestApprovalEOFStopsPending(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	b := &approvalBackend{approve: func(ctx context.Context, _ ApprovalRequest) (ApprovalDecision, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return ApprovalDecision{}, ctx.Err()
	}}
	r, w := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), r, io.Discard, b) }()
	io.WriteString(w, `{"id":1,"method":"tools/call","params":{"name":"approve_tool","arguments":{"tool_name":"Bash","input":{}}}}`+"\n")
	<-started
	w.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("EOF did not cancel approvals")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("approval still active")
	}
	// A regular grant must never expose the permission endpoint as a model tool.
	b2 := &HTTPBackend{URL: "http://unused", Token: "t"}
	if strings.Contains(string(mustJSON(t, backendTools(b2))), ToolApprove) {
		t.Fatal("ordinary grant exposed approval")
	}
}
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func TestApprovalCancelledDecisionDenies(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := &approvalBackend{approve: func(context.Context, ApprovalRequest) (ApprovalDecision, error) {
		cancel()
		return ApprovalDecision{Outcome: "allow"}, nil
	}}
	result := permission(t, call(ctx, b, json.RawMessage(`{"name":"approve_tool","arguments":{"tool_name":"Bash","input":{}}}`)))
	if result["behavior"] != "deny" {
		t.Fatalf("cancelled approval accepted: %v", result)
	}
}
