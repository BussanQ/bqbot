package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestThreadToolProtocolHTTP(t *testing.T) {
	calls := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer scoped" {
			t.Error("missing grant token")
		}
		if r.URL.Path == "/close_thread" {
			var body struct {
				ThreadID string `json:"thread_id"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.ThreadID != "thread" {
				t.Error("invalid close arguments")
			}
		}
		if r.URL.Path == "/ask_bot" {
			var body AskRequest
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Name != "Bo" || body.Question != "New topic" || body.Thread != "new" {
				t.Error("new thread arguments lost")
			}
		}
		calls <- r.URL.Path
		json.NewEncoder(w).Encode(Reply{Text: "ok"})
	}))
	defer srv.Close()
	backend := &HTTPBackend{URL: srv.URL, Token: "scoped"}
	tools := backendTools(backend)["tools"].([]any)
	if len(tools) != 5 {
		t.Fatalf("tool list %+v", tools)
	}
	for _, name := range []string{ToolThreads, ToolCloseThread, ToolRoomTargets} {
		params, _ := json.Marshal(map[string]any{"name": name, "arguments": map[string]string{"thread_id": "thread"}})
		result := call(context.Background(), backend, params)
		if result["isError"] == true {
			t.Fatal(result)
		}
		if got := <-calls; got != "/"+name {
			t.Fatalf("path %s", got)
		}
	}
	result := call(context.Background(), backend, json.RawMessage(`{"name":"close_thread","arguments":{}}`))
	if result["isError"] != true {
		t.Fatal("missing ID accepted")
	}
	newThread := call(context.Background(), backend, json.RawMessage(`{"name":"ask_bot","arguments":{"name":"Bo","question":"New topic","thread":"new"}}`))
	if newThread["isError"] == true {
		t.Fatal(newThread)
	}
	if got := <-calls; got != "/ask_bot" {
		t.Fatalf("path %s", got)
	}
	invalidThread := call(context.Background(), backend, json.RawMessage(`{"name":"ask_bot","arguments":{"name":"Bo","question":"New topic","thread":"unknown"}}`))
	if invalidThread["isError"] != true {
		t.Fatal("invalid thread selector accepted")
	}
	backend.ApprovalOnly = true
	for _, name := range []string{ToolThreads, ToolCloseThread, ToolRoomTargets} {
		params, _ := json.Marshal(map[string]any{"name": name, "arguments": map[string]string{"thread_id": "thread"}})
		if result := call(context.Background(), backend, params); result["isError"] != true {
			t.Fatalf("approval grant accepted %s", name)
		}
	}
	data, _ := json.Marshal(backendTools(backend))
	if strings.Contains(string(data), ToolThreads) {
		t.Fatal("approval scope exposed thread tools")
	}
}
