package computer

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"bqbot/internal/store"
)

func pngFixture(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if e := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 4, 3))); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func TestComputerBoatScreenshotAndInput(t *testing.T) {
	frame := pngFixture(t)
	var commands []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing token")
		}
		if strings.HasSuffix(r.URL.Path, "/commands") {
			var p struct {
				Command string `json:"command"`
			}
			json.NewDecoder(r.Body).Decode(&p)
			commands = append(commands, p.Command)
			w.Write([]byte(`{"success":true,"exitCode":0}`))
			return
		}
		if !strings.HasPrefix(r.URL.Query().Get("path"), "/tmp/bqbot-") {
			t.Error("unsafe artifact path")
		}
		w.Write(frame)
	}))
	defer server.Close()
	t.Setenv("COMPUTER_TEST_TOKEN", "secret")
	c := store.ComputerConfig{BotID: "bot", Provider: "boat", URL: server.URL, TokenEnv: "COMPUTER_TEST_TOKEN", SandboxID: "sandbox", Enabled: true}
	out, e := Execute(context.Background(), c, Action{Kind: "screenshot"})
	if e != nil || out.Width != 4 || out.Height != 3 || out.Image == nil {
		t.Fatal(out, e)
	}
	if len(commands) != 2 || !strings.Contains(commands[0], "scrot") || !strings.HasPrefix(commands[1], "rm -f -- ") {
		t.Fatal(commands)
	}
	_, e = Execute(context.Background(), c, Action{Kind: "type", Text: "' $(touch /tmp/unsafe)"})
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(commands[2], `-- ''"'"' $(touch /tmp/unsafe)'`) {
		t.Fatal("shell text was not quoted", commands[2])
	}
}
func TestComputerCuaConsentAndImage(t *testing.T) {
	frame := base64.StdEncoding.EncodeToString(pngFixture(t))
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&p)
		if len(p.ID) == 0 {
			w.WriteHeader(202)
			return
		}
		result := any(map[string]any{"protocolVersion": "2025-06-18"})
		if p.Method == "tools/call" {
			calls.Add(1)
			if p.Params.Name != "get_desktop_state" {
				t.Error(p.Params.Name)
			}
			result = map[string]any{"content": []any{map[string]string{"type": "image", "mimeType": "image/png", "data": frame}}}
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": p.ID, "result": result})
	}))
	defer server.Close()
	c := store.ComputerConfig{BotID: "bot", Provider: "host", URL: server.URL, Enabled: true}
	if _, e := Execute(context.Background(), c, Action{Kind: "screenshot"}); e == nil || calls.Load() != 0 {
		t.Fatal("host accessed without consent")
	}
	c.HostConsent = true
	out, e := Execute(context.Background(), c, Action{Kind: "screenshot"})
	if e != nil || out.Image == nil || calls.Load() != 1 {
		t.Fatal(out, e, calls.Load())
	}
	c.Enabled = false
	if _, e := Execute(context.Background(), c, Action{Kind: "click"}); e == nil || calls.Load() != 1 {
		t.Fatal("disabled computer accessed")
	}
}
func TestComputerRejectsUnsafeActionsAndImages(t *testing.T) {
	for _, a := range []Action{{Kind: "shell"}, {Kind: "key", Key: "Return;sh"}, {Kind: "click", X: -1}, {Kind: "type", Text: "\x00"}, {Kind: "scroll", Direction: "sideways"}} {
		if a.Validate() == nil {
			t.Fatal(a)
		}
	}
	if _, e := screenshot([]byte("<svg></svg>")); e == nil {
		t.Fatal("invalid screenshot accepted")
	}
}
