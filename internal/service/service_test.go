package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bqbot/internal/driver"
	"bqbot/internal/harness"
	"bqbot/internal/store"
)

type fakeDriver struct {
	run func(context.Context, driver.TurnInput, func(driver.Event)) error
}

func (*fakeDriver) Name() string   { return "echo" }
func (*fakeDriver) Binary() string { return "" }
func (d *fakeDriver) Run(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
	return d.run(ctx, in, emit)
}

func newTestService(t *testing.T, d driver.Driver) (*Server, *Client, *harness.Harness) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if d == nil {
		d = &fakeDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
			emit(driver.Event{Kind: driver.EvText, Text: in.Text})
			return nil
		}}
	}
	h := harness.New(st, d)
	s, err := Listen(h, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bots, _ := st.Bots()
		for _, b := range bots {
			h.Stop(b.ID)
		}
		_ = s.Close()
		_ = h.Close()
	})
	return s, NewClient(s.URL, s.Token), h
}
func mustSave(t *testing.T, c *Client, id string) store.Bot {
	t.Helper()
	b := store.Bot{ID: id, Name: "Ada", Engine: "echo", ApprovalMode: "ask"}
	if err := c.SaveBot(b); err != nil {
		t.Fatal(err)
	}
	return b
}
func statusIs(t *testing.T, err error, status int) {
	t.Helper()
	var e *HTTPError
	if !errors.As(err, &e) || e.StatusCode != status {
		t.Fatalf("expected HTTP %d, got %v", status, err)
	}
}

func TestHTTPCRUDSettingsAndValidation(t *testing.T) {
	_, c, h := newTestService(t, nil)
	b := mustSave(t, c, "bot-a")
	mustSave(t, c, "bot-b")
	b.Name = "Renamed"
	if err := c.SaveBot(b); err != nil {
		t.Fatal(err)
	}
	bots, err := c.Bots()
	if err != nil || len(bots) != 2 || bots[0].Name != "Renamed" {
		t.Fatalf("bots: %+v %v", bots, err)
	}
	dir, err := c.WorkDir(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("work directory: %v", err)
	}
	if err := h.Store.Append(b.ID, store.Message{ID: "message", Role: "user", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	msgs, err := c.Transcript(b.ID)
	if err != nil || len(msgs) != 1 || msgs[0].Text != "hello" {
		t.Fatalf("transcript: %+v %v", msgs, err)
	}
	if err := h.Store.AppendHandoff(b.ID, "bot-b", store.Message{ID: "handoff", Role: "assistant", Text: "answer"}); err != nil {
		t.Fatal(err)
	}
	msgs, err = c.HandoffTranscript(b.ID, "bot-b")
	if err != nil || len(msgs) != 1 || msgs[0].Text != "answer" {
		t.Fatalf("handoff transcript: %+v %v", msgs, err)
	}
	settings := map[string]driver.EngineSettings{"echo": {APIKeyEnv: "TEST_BQBOT_KEY", Models: []string{"custom-model"}}}
	t.Setenv("TEST_BQBOT_KEY", "secret-must-not-leak")
	if err := c.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	got, err := c.Settings()
	if err != nil || got["echo"].APIKeyEnv != "TEST_BQBOT_KEY" {
		t.Fatalf("settings: %+v %v", got, err)
	}
	data, _ := json.Marshal(got)
	if strings.Contains(string(data), "secret-must-not-leak") {
		t.Fatal("literal secret exposed")
	}
	engines, err := c.Engines()
	if err != nil || len(engines) != 1 || engines[0].Name != "echo" || !engines[0].Available {
		t.Fatalf("engines: %+v %v", engines, err)
	}
	for _, mutate := range []func(*store.Bot){
		func(b *store.Bot) { b.ID = "../escape" }, func(b *store.Bot) { b.Engine = "unknown" }, func(b *store.Bot) { b.Model = "invalid\nmodel" }, func(b *store.Bot) { b.Effort = "invalid-effort" }, func(b *store.Bot) { b.ApprovalMode = "unsafe" },
	} {
		bad := b
		mutate(&bad)
		statusIs(t, c.SaveBot(bad), 400)
	}
	statusIs(t, c.SaveSettings(map[string]driver.EngineSettings{"unknown": {}}), 400)
	statusIs(t, c.SaveSettings(map[string]driver.EngineSettings{"echo": {APIKeyEnv: "sk-literal-secret"}}), 400)
	statusIs(t, c.SaveSettings(map[string]driver.EngineSettings{"echo": {BaseURL: "https://secret@example.com"}}), 400)
	_, err = c.Send(b.ID, "message", nil, "bad-mode")
	statusIs(t, err, 400)
	_, err = c.Send(b.ID, "", nil, "")
	statusIs(t, err, 400)
	_, err = c.Send(b.ID, "", []driver.Attachment{{Name: "image.png", MIME: "image/png", Data: base64.StdEncoding.EncodeToString([]byte("not an image"))}}, "")
	statusIs(t, err, 400)
	_, err = c.Transcript("missing")
	statusIs(t, err, 404)
	if _, err = c.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err = c.DeleteBot(b.ID); err != nil {
		t.Fatal(err)
	}
	_, err = c.Transcript(b.ID)
	statusIs(t, err, 404)
	if _, err = os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("work directory was not deleted: %v", err)
	}
}

func TestAuthOriginAndJSONLimits(t *testing.T) {
	s, _, _ := newTestService(t, nil)
	for _, token := range []string{"", "wrong", s.Token + "x"} {
		_, err := NewClient(s.URL, token).Bots()
		if token == "" {
			if err == nil {
				t.Fatal("empty token accepted")
			}
		} else {
			statusIs(t, err, 401)
		}
	}
	tests := []struct {
		method, path, body, origin, host string
		status                           int
	}{
		{"GET", "/v1/bots", "", "https://evil.example", "", 403},
		{"GET", "/v1/bots", "", "", "evil.example", 403},
		{"PUT", "/v1/settings", `{"echo":{"api_key":"literal"}}`, "", "", 400},
		{"PUT", "/v1/settings", `{} {}`, "", "", 400},
		{"PUT", "/v1/settings", `{"echo":{"path":"` + strings.Repeat("x", maxRequestBytes) + `"}}`, "", "", 413},
	}
	for i, tt := range tests {
		r, err := http.NewRequest(tt.method, s.URL+tt.path, strings.NewReader(tt.body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+s.Token)
		r.Header.Set("Content-Type", "application/json")
		if tt.origin != "" {
			r.Header.Set("Origin", tt.origin)
		}
		if tt.host != "" {
			r.Host = tt.host
		}
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tt.status {
			t.Fatalf("case %d: status %d, want %d", i, resp.StatusCode, tt.status)
		}
		if resp.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("CORS enabled")
		}
	}
}

func TestLoopbackOnly(t *testing.T) {
	_, _, h := newTestService(t, nil)
	for _, addr := range []string{":0", "0.0.0.0:0", "[::]:0", "192.0.2.1:0", "example.com:8799"} {
		s, err := Listen(h, addr)
		if err == nil {
			s.Close()
			t.Fatalf("accepted %s", addr)
		}
	}
	for _, base := range []string{"http://example.com:8799", "http://192.0.2.1:8799", "https://127.0.0.1:8799", "http://user:pass@127.0.0.1:8799", "http://127.0.0.1:8799?token=bad"} {
		if _, err := NewClient(base, "token").Bots(); err == nil {
			t.Fatalf("accepted %s", base)
		}
	}
}

func TestApprovalQueueStopOverHTTPAndSSE(t *testing.T) {
	allowed := make(chan driver.ApprovalDecision, 1)
	release := make(chan struct{})
	started := make(chan string, 8)
	d := &fakeDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		started <- in.Text
		switch in.Text {
		case "first":
			if in.Approve == nil {
				return errors.New("missing approval callback")
			}
			decision, err := in.Approve(ctx, driver.ApprovalRequest{ID: "approval-1", Kind: "tool", Tool: "write", Input: json.RawMessage(`{"path":"file.txt"}`)})
			if err != nil {
				return err
			}
			allowed <- decision
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
			}
		case "stop":
			<-ctx.Done()
			return ctx.Err()
		}
		emit(driver.Event{Kind: driver.EvText, Text: "reply: " + in.Text})
		return nil
	}}
	_, c, _ := newTestService(t, d)
	b := mustSave(t, c, "bot-a")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	updates := make(chan harness.Update, 256)
	watchDone := make(chan error, 1)
	go func() {
		watchDone <- c.Watch(ctx, func(u harness.Update) {
			select {
			case updates <- u:
			case <-ctx.Done():
			}
		})
	}()
	if _, err := c.Send(b.ID, "first", nil, ""); err != nil {
		t.Fatal(err)
	}
	var approvalID string
	for approvalID == "" {
		select {
		case u := <-updates:
			if u.Approval != nil {
				approvalID = u.Approval.ID
			} else if len(u.Approvals) > 0 {
				approvalID = u.Approvals[0].ID
			}
		case <-ctx.Done():
			t.Fatal("approval did not arrive over SSE")
		}
	}
	queued, err := c.Send(b.ID, "second", nil, "queue")
	if err != nil || queued.Text != "second" {
		t.Fatalf("queued message: %+v %v", queued, err)
	}
	statusIs(t, c.Approve(b.ID, approvalID, driver.ApprovalDecision{Outcome: "invalid"}), 400)
	if err = c.Approve(b.ID, approvalID, driver.ApprovalDecision{Outcome: "allow"}); err != nil {
		t.Fatal(err)
	}
	select {
	case decision := <-allowed:
		if decision.Outcome != "allow" {
			t.Fatalf("decision: %+v", decision)
		}
	case <-ctx.Done():
		t.Fatal("approval not delivered")
	}
	statusIs(t, c.Approve(b.ID, approvalID, driver.ApprovalDecision{Outcome: "allow"}), 409)
	close(release)
	for {
		select {
		case u := <-updates:
			if u.Done && u.Reply != nil && u.Reply.Text == "reply: second" {
				goto secondDone
			}
		case <-ctx.Done():
			t.Fatal("queued turn did not complete")
		}
	}
secondDone:
	msgs, err := c.Transcript(b.ID)
	if err != nil || len(msgs) < 4 {
		t.Fatalf("transcript after queue: %+v %v", msgs, err)
	}
	if _, err = c.Send(b.ID, "stop", nil, ""); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case text := <-started:
			if text == "stop" {
				goto stopStarted
			}
		case <-ctx.Done():
			t.Fatal("stop turn did not start")
		}
	}
stopStarted:
	if err = c.Stop(b.ID); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case u := <-updates:
			if u.Done && !u.Busy && u.Reply != nil && strings.Contains(u.Reply.Text, "stopped") {
				goto stopped
			}
		case <-ctx.Done():
			t.Fatal("stop did not finish")
		}
	}
stopped:
	cancel()
	select {
	case err := <-watchDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Watch: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Watch ignored cancellation")
	}
}

func TestWatchReconnectResyncAndErrors(t *testing.T) {
	var connections atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if connections.Add(1) == 1 {
			fmt.Fprint(w, "event: update\ndata: {\"bot_id\":\"a\",\"busy\":true}\n\n")
			return
		}
		fmt.Fprint(w, "event: snapshot\ndata: [{\"bot_id\":\"a\",\"done\":true,\"error\":\"recovered error\"}]\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var got []harness.Update
	err := NewClient(ts.URL, "test-token").Watch(ctx, func(u harness.Update) {
		got = append(got, u)
		if u.Done {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || len(got) != 2 || got[1].Err == nil || got[1].Err.Error() != "recovered error" || connections.Load() != 2 {
		t.Fatalf("resync: updates=%+v connections=%d err=%v", got, connections.Load(), err)
	}
	err = NewClient(ts.URL, "wrong").Watch(context.Background(), func(harness.Update) {})
	statusIs(t, err, 401)
}

func TestClientDoesNotForwardTokenOnRedirect(t *testing.T) {
	var reached atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true) }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	_, err := NewClient(source.URL, "test-token").Bots()
	statusIs(t, err, 307)
	if reached.Load() {
		t.Fatal("redirect received service credential")
	}
}

func TestDiscoveryPermissionsAndOwnership(t *testing.T) {
	s, _, _ := newTestService(t, nil)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "service.json")
	if err := s.WriteDiscovery(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v %v", info, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var d Discovery
	if err = json.Unmarshal(data, &d); err != nil || d.URL != s.URL || d.Token != s.Token {
		t.Fatal("bad discovery file")
	}
	if err = s.WriteDiscovery(path); err == nil {
		t.Fatal("overwrote existing discovery")
	}
	other := filepath.Join(dir, "other.json")
	if err = s.WriteDiscovery(other); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(other); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(other, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err = os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if err = s.WriteDiscovery(link); err == nil {
		t.Fatal("overwrote symlink")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("own discovery was not removed")
	}
	data, err = os.ReadFile(other)
	if err != nil || string(data) != "replacement" {
		t.Fatal("removed another server's discovery")
	}
	if err = s.WriteDiscovery(path); err == nil {
		t.Fatal("published discovery after close")
	}
}

func TestDiscoveryRefusesPublicDirectory(t *testing.T) {
	s, _, _ := newTestService(t, nil)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteDiscovery(filepath.Join(dir, "service.json")); err == nil {
		t.Fatal("published secret in a public directory")
	}
}

func TestAttachmentValidation(t *testing.T) {
	// A tiny valid PNG, with the MIME signature checked before dispatch.
	data := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aD1sAAAAASUVORK5CYII="
	a := driver.Attachment{Name: "one.png", MIME: "image/png", Data: data}
	if err := validateSend(sendRequest{Attachments: []driver.Attachment{a}}); err != nil {
		t.Fatal(err)
	}
	a.Name = "../one.png"
	if err := validateSend(sendRequest{Attachments: []driver.Attachment{a}}); err == nil {
		t.Fatal("unsafe name accepted")
	}
	a.Name = "one.png"
	a.Data = strings.Repeat("A", base64.StdEncoding.EncodedLen(5<<20)+4)
	if err := validateSend(sendRequest{Attachments: []driver.Attachment{a}}); err == nil {
		t.Fatal("oversized image accepted")
	}
}
