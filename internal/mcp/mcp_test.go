package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeBackend struct {
	ask   func(ctx context.Context, name, q string) (string, error)
	calls chan string
}

func (f *fakeBackend) ListBots(context.Context) (string, error) { return "- Bo: reviews code\n", nil }

func (f *fakeBackend) AskBot(ctx context.Context, name, q string) (string, error) {
	return f.ask(ctx, name, q)
}

// session runs Serve over pipes and returns a func that sends a line and
// one that reads the next response.
func session(t *testing.T, b Backend) (send func(string), recv func() map[string]any) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), inR, outW, b); outW.Close() }()
	t.Cleanup(func() {
		inW.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return when stdin closed")
		}
	})
	lines := make(chan map[string]any, 16)
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				t.Errorf("bad line %q", sc.Text())
			}
			lines <- m
		}
		close(lines)
	}()
	send = func(s string) { inW.Write([]byte(s + "\n")) }
	recv = func() map[string]any {
		t.Helper()
		select {
		case m := <-lines:
			return m
		case <-time.After(5 * time.Second):
			t.Fatal("no response")
			return nil
		}
	}
	return send, recv
}

func resultText(t *testing.T, m map[string]any) (string, bool) {
	t.Helper()
	res, ok := m["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in %v", m)
	}
	content := res["content"].([]any)
	isErr, _ := res["isError"].(bool)
	return content[0].(map[string]any)["text"].(string), isErr
}

func TestProtocol(t *testing.T) {
	b := &fakeBackend{ask: func(_ context.Context, name, q string) (string, error) {
		if name == "Busy" {
			return "", errors.New("Busy is busy")
		}
		return name + " says hi to " + q, nil
	}}
	send, recv := session(t, b)

	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	m := recv()
	res := m["result"].(map[string]any)
	if m["id"].(float64) != 1 || res["protocolVersion"] != "2025-03-26" || res["serverInfo"].(map[string]any)["name"] != "bqbot" {
		t.Errorf("initialize: %v", m)
	}
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	send(`{"jsonrpc":"2.0","id":"l","method":"tools/list"}`)
	m = recv()
	tools := m["result"].(map[string]any)["tools"].([]any)
	var names []string
	for _, tl := range tools {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	if m["id"] != "l" || strings.Join(names, ",") != "list_bots,ask_bot" {
		t.Errorf("tools/list: %v", m)
	}

	send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ask_bot","arguments":{"name":"Bo","question":"Ada"}}}`)
	if text, isErr := resultText(t, recv()); text != "Bo says hi to Ada" || isErr {
		t.Errorf("ask_bot: %q %v", text, isErr)
	}
	send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ask_bot","arguments":{"name":"Busy","question":"x"}}}`)
	if text, isErr := resultText(t, recv()); text != "Busy is busy" || !isErr {
		t.Errorf("busy ask_bot: %q %v", text, isErr)
	}
	send(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"ask_bot","arguments":{"name":"Bo"}}}`)
	if _, isErr := resultText(t, recv()); !isErr {
		t.Error("ask_bot without a question did not fail")
	}
	send(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"list_bots","arguments":{}}}`)
	if text, _ := resultText(t, recv()); text != "- Bo: reviews code\n" {
		t.Errorf("list_bots: %q", text)
	}
	send(`{"jsonrpc":"2.0","id":6,"method":"nope"}`)
	if m := recv(); m["error"].(map[string]any)["code"].(float64) != -32601 {
		t.Errorf("unknown method: %v", m)
	}
	send(`not json`)
	if m := recv(); m["error"].(map[string]any)["code"].(float64) != -32700 {
		t.Errorf("parse error: %v", m)
	}
}

func TestCancel(t *testing.T) {
	started := make(chan struct{})
	b := &fakeBackend{ask: func(ctx context.Context, _, _ string) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}}
	send, recv := session(t, b)
	send(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"ask_bot","arguments":{"name":"Bo","question":"q"}}}`)
	<-started
	// A slow call does not hold up others.
	send(`{"jsonrpc":"2.0","id":8,"method":"ping"}`)
	if m := recv(); m["id"].(float64) != 8 {
		t.Fatalf("ping: %v", m)
	}
	send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7}}`)
	m := recv()
	if text, isErr := resultText(t, m); m["id"].(float64) != 7 || !isErr || !strings.Contains(text, "canceled") {
		t.Errorf("cancelled call: %v", m)
	}
}

func TestHTTPBackend(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(Reply{Error: "bad token"})
			return
		}
		var req AskRequest
		json.NewDecoder(r.Body).Decode(&req)
		json.NewEncoder(w).Encode(Reply{Text: r.URL.Path + " " + req.Name + " " + req.Question})
	}))
	defer srv.Close()
	c := &HTTPBackend{URL: srv.URL, Token: "tok"}
	if got, err := c.AskBot(context.Background(), "Bo", "why"); err != nil || got != "/ask_bot Bo why" {
		t.Errorf("AskBot: %q %v", got, err)
	}
	c.Token = "nope"
	if _, err := c.ListBots(context.Background()); err == nil || err.Error() != "bad token" {
		t.Errorf("bad token: %v", err)
	}
}

func TestMainGrantFile(t *testing.T) {
	if err := Main(nil); err == nil {
		t.Error("no error without a grant file")
	}
	p := filepath.Join(t.TempDir(), "g.json")
	os.WriteFile(p, []byte(`{"url":""}`), 0o600)
	if err := Main([]string{p}); err == nil {
		t.Error("no error for an empty grant")
	}
}
