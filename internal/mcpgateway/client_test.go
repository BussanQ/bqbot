package mcpgateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func mockResult(req map[string]json.RawMessage) any {
	var method string
	json.Unmarshal(req["method"], &method)
	switch method {
	case "initialize":
		return map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "fixture", "version": "1"}}
	case "tools/list":
		return map[string]any{"tools": []any{map[string]any{"name": "lookup", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"readOnlyHint": true}}}}
	case "tools/call":
		return map[string]any{"content": []any{map[string]string{"type": "text", "text": "gateway-result"}}}
	}
	return map[string]any{}
}
func response(req map[string]json.RawMessage) []byte {
	data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": mockResult(req)})
	return data
}
func exercise(t *testing.T, c *Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tools, err := c.Tools(ctx)
	if err != nil || len(tools) != 1 || tools[0].Name != "lookup" {
		t.Fatal(tools, err)
	}
	result, err := c.Call(ctx, "lookup", json.RawMessage(`{"query":"example"}`))
	if err != nil || !strings.Contains(string(result), "gateway-result") {
		t.Fatal(string(result), err)
	}
}
func TestHTTPAndStreamingResponse(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer secret" {
					t.Error("header missing")
					w.WriteHeader(401)
					return
				}
				var req map[string]json.RawMessage
				if json.NewDecoder(r.Body).Decode(&req) != nil {
					w.WriteHeader(400)
					return
				}
				var method string
				json.Unmarshal(req["method"], &method)
				if method != "initialize" && r.Header.Get("Mcp-Session-Id") != "session" {
					t.Error("session missing")
				}
				if len(req["id"]) == 0 {
					w.WriteHeader(202)
					return
				}
				w.Header().Set("Mcp-Session-Id", "session")
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "event: message\ndata: %s\n\n", response(req))
					w.(http.Flusher).Flush()
					<-r.Context().Done()
				} else {
					w.Header().Set("Content-Type", "application/json")
					w.Write(response(req))
				}
			}))
			defer server.Close()
			c, err := Open(context.Background(), Config{Transport: "http", URL: server.URL, Headers: map[string]string{"Authorization": "Bearer secret"}})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			exercise(t, c)
		})
	}
}
func TestLegacySSE(t *testing.T) {
	responses := make(chan []byte, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: endpoint\ndata: /messages?session=abc\n\n")
			w.(http.Flusher).Flush()
			for {
				select {
				case data := <-responses:
					fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
					w.(http.Flusher).Flush()
				case <-r.Context().Done():
					return
				}
			}
		}
		if r.URL.Path != "/messages" || r.URL.Query().Get("session") != "abc" {
			t.Error("wrong endpoint")
		}
		var req map[string]json.RawMessage
		json.NewDecoder(r.Body).Decode(&req)
		w.WriteHeader(202)
		if len(req["id"]) > 0 {
			responses <- response(req)
		}
	}))
	defer server.Close()
	c, err := Open(context.Background(), Config{Transport: "sse", URL: server.URL + "/sse"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	exercise(t, c)
}
func TestStdio(t *testing.T) {
	c, err := Open(context.Background(), Config{Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestMCPHelper$"}, Env: append(os.Environ(), "BQBOT_MCP_HELPER=1")})
	if err != nil {
		t.Fatal(err)
	}
	exercise(t, c)
	c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = c.Tools(ctx); err == nil {
		t.Fatal("closed process accepted request")
	}
}
func TestMCPHelper(t *testing.T) {
	if os.Getenv("BQBOT_MCP_HELPER") != "1" {
		return
	}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var req map[string]json.RawMessage
		json.Unmarshal(sc.Bytes(), &req)
		if len(req["id"]) > 0 {
			fmt.Println(string(response(req)))
		}
	}
	os.Exit(0)
}
func TestHTTPRedirectAndCancellation(t *testing.T) {
	called := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer redirect.Close()
	if _, err := Open(context.Background(), Config{Transport: "http", URL: redirect.URL, Headers: map[string]string{"Authorization": "secret"}}); err == nil || called {
		t.Fatal("redirect leaked credential")
	}
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body); <-r.Context().Done() }))
	defer stalled.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := Open(ctx, Config{Transport: "http", URL: stalled.URL}); err == nil {
		t.Fatal("canceled initialize succeeded")
	}
	if time.Since(started) > time.Second {
		t.Fatal("cancellation did not stop HTTP request")
	}
}
