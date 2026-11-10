package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The child only exposes one tool, and records calls so the parent can detect
// side effects from truncated or partially invalid provider responses.
func TestBuiltinMCPHelper(t *testing.T) {
	log := os.Getenv("BQBOT_TEST_TOOL_CALL")
	if log == "" {
		return
	}
	input := json.NewDecoder(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	for {
		var request struct {
			Method string
			ID     json.RawMessage
			Params struct {
				Name      string
				Arguments map[string]any
			}
		}
		if input.Decode(&request) != nil {
			return
		}
		if len(request.ID) == 0 {
			continue
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]string{"protocolVersion": "2025-06-18"}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "read", "description": "Read a test file", "inputSchema": map[string]string{"type": "object"}}}}
		case "tools/call":
			if request.Params.Name != "read" || request.Params.Arguments["name"] != "file" {
				t.Fatal("invalid MCP call")
			}
			if err := os.WriteFile(log, []byte("called"), 0600); err != nil {
				t.Fatal(err)
			}
			result = map[string]any{"content": []any{map[string]string{"type": "text", "text": "from MCP"}}}
		default:
			t.Fatalf("unexpected method %s", request.Method)
		}
		if err := out.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			return
		}
	}
}

func TestBuiltinToolConversation(t *testing.T) {
	for _, scenario := range []string{"complete", "truncated", "unknown"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "calls")
			t.Setenv("BQBOT_TEST_TOOL_CALL", log)
			t.Setenv("OPENAI_API_KEY", "unrelated-provider-secret")
			if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("Use workspace instructions"), 0600); err != nil {
				t.Fatal(err)
			}
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" {
					t.Error("implicit provider key sent to custom endpoint")
				}
				var body struct {
					Messages []struct {
						Role    string
						Content string
					}
					Tools []any
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Tools) != 1 || len(body.Messages) < 2 {
					t.Error("missing model tools/history")
					w.WriteHeader(400)
					return
				}
				if !strings.Contains(body.Messages[0].Content, "Use workspace instructions") {
					t.Error("missing workspace instructions")
				}
				last := body.Messages[len(body.Messages)-1]
				w.Header().Set("Content-Type", "text/event-stream")
				if last.Role == "tool" {
					if last.Content != "from MCP" {
						t.Error("result was not returned to the model")
					}
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\n")
					return
				}
				send := func(delta any, finish any) {
					b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": delta}, "finish_reason": finish}}})
					fmt.Fprintf(w, "data: %s\n\n", b)
				}
				send([]any{map[string]any{"index": 0, "type": "function", "id": "one", "function": map[string]string{"name": "read", "arguments": `{"name":`}}}, nil)
				deltas := []any{map[string]any{"index": 0, "function": map[string]string{"arguments": `"file"}`}}}
				if scenario == "unknown" {
					deltas = append(deltas, map[string]any{"index": 1, "id": "two", "type": "function", "function": map[string]string{"name": "unlisted", "arguments": "{}"}})
				}
				if scenario == "truncated" {
					send(deltas, nil)
					fmt.Fprint(w, "data: [DONE]\n\n")
					return
				}
				send(deltas, "tool_calls")
			}))
			defer model.Close()
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			in := TurnInput{Text: "read", Dir: dir, Settings: EngineSettings{BaseURL: model.URL}, MCP: &MCPServer{Command: exe, Args: []string{"-test.run=^TestBuiltinMCPHelper$"}}}
			var events []Event
			err = (&Builtin{}).Run(context.Background(), in, func(e Event) { events = append(events, e) })
			_, sideEffect := os.Stat(log)
			if scenario == "complete" {
				if err != nil || sideEffect != nil || text(events) != "done" {
					t.Fatal(err, sideEffect, events)
				}
			} else if err == nil || !os.IsNotExist(sideEffect) {
				t.Fatalf("invalid batch executed: %v %v", err, sideEffect)
			}
		})
	}
}
