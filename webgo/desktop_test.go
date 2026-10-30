package webgo

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
	"time"

	"bqbot/internal/driver"
	"bqbot/internal/harness"
	"bqbot/internal/service"
	"bqbot/internal/store"
	"bqbot/web"
)

func testDesktop(t *testing.T) (*Desktop, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := harness.New(st, &driver.Echo{})
	server, err := service.Listen(h, "127.0.0.1:0")
	if err != nil {
		h.Close()
		t.Fatal(err)
	}
	d, err := newDesktop(service.NewClient(server.URL, server.Token))
	if err != nil {
		server.Close()
		h.Close()
		t.Fatal(err)
	}
	d.start(nil)
	t.Cleanup(func() { d.close(); server.Close(); h.Close() })
	return d, st
}
func desktopAction(t *testing.T, d *Desktop, r WebAction) json.RawMessage {
	t.Helper()
	data, err := d.Action(r)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func desktopState(t *testing.T, d *Desktop) webState {
	t.Helper()
	data, err := d.State()
	if err != nil {
		t.Fatal(err)
	}
	var s webState
	if err = json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	return s
}
func waitDesktop(t *testing.T, d *Desktop, predicate func(webState) bool) webState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s := desktopState(t, d)
		if predicate(s) {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("desktop state did not converge")
	return webState{}
}

func TestDesktopCapturedConversation(t *testing.T) {
	d, st := testDesktop(t)
	for _, id := range []string{"ada", "bo"} {
		desktopAction(t, d, WebAction{Kind: "save-bot", Bot: store.Bot{ID: id, Name: id, Engine: "echo"}})
	}
	// The visible conversation is Bo, but a previously captured send belongs to Ada.
	desktopAction(t, d, WebAction{Kind: "send", ID: "ada", Text: "hello from ada"})
	if s := desktopState(t, d); s.Selected != "bo" {
		t.Fatalf("send changed selection: %s", s.Selected)
	}
	desktopAction(t, d, WebAction{Kind: "select", ID: "ada"})
	s := waitDesktop(t, d, func(s webState) bool {
		return s.Conversation != nil && !s.Conversation.Busy && len(s.Conversation.Messages) == 2
	})
	if s.Conversation.Messages[1].Text != "Echo: hello from ada" {
		t.Fatalf("wrong reply: %+v", s.Conversation.Messages)
	}
	other, err := st.Transcript("bo")
	if err != nil || len(other) != 0 {
		t.Fatalf("message leaked to Bo: %+v %v", other, err)
	}
	desktopAction(t, d, WebAction{Kind: "create-thread", BotID: "ada", Text: "Design"})
	s = desktopState(t, d)
	id := s.Conversation.ID
	if id == "ada" {
		t.Fatal("new thread did not select independent conversation")
	}
	desktopAction(t, d, WebAction{Kind: "save-bot", Bot: store.Bot{ID: "ada", Name: "Ada edited", Engine: "echo", Pinned: true}})
	if desktopState(t, d).Conversation.ID != id {
		t.Fatal("editing a bot must keep the selected independent conversation")
	}
	desktopAction(t, d, WebAction{Kind: "send", ID: id, Text: "separate context"})
	waitDesktop(t, d, func(s webState) bool { return !s.Conversation.Busy && len(s.Conversation.Messages) == 2 })
	desktopAction(t, d, WebAction{Kind: "close-thread", ID: id})
	if !desktopState(t, d).Conversation.Closed {
		t.Fatal("closed thread is still editable")
	}
	if _, err := d.Action(WebAction{Kind: "send", ID: id, Text: "should fail"}); err == nil {
		t.Fatal("closed conversation accepted input")
	}
}

func TestDesktopRoomsMemoriesAndSearch(t *testing.T) {
	d, _ := testDesktop(t)
	desktopAction(t, d, WebAction{Kind: "save-bot", Bot: store.Bot{ID: "ada", Name: "Ada", Engine: "echo"}})
	desktopAction(t, d, WebAction{Kind: "save-room", Room: store.Room{ID: "studio", Name: "Studio", Members: []string{"ada"}, LeadID: "ada", ReplyMode: "lead"}})
	desktopAction(t, d, WebAction{Kind: "send", ID: "studio", Scope: "room", Text: "a shared idea"})
	s := waitDesktop(t, d, func(s webState) bool {
		return s.Conversation.RoomID == "studio" && !s.Conversation.Busy && len(s.Conversation.Messages) == 2
	})
	if s.Conversation.ID != "room:studio" {
		t.Fatal("room scope was lost")
	}
	desktopAction(t, d, WebAction{Kind: "save-memory", Scope: "studio", Memory: store.Memory{Text: "Use concise answers"}})
	var memories []store.Memory
	if err := json.Unmarshal(desktopAction(t, d, WebAction{Kind: "memories", Scope: "studio"}), &memories); err != nil {
		t.Fatal(err)
	}
	if len(memories) != 1 || memories[0].ID == "" {
		t.Fatalf("invalid memories: %+v", memories)
	}
	var results []store.SearchResult
	if err := json.Unmarshal(desktopAction(t, d, WebAction{Kind: "search", Text: "shared idea"}), &results); err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 || results[0].RoomID != "studio" {
		t.Fatalf("room search failed: %+v", results)
	}
	desktopAction(t, d, WebAction{Kind: "delete-memory", Scope: "studio", ID: memories[0].ID})
	if _, err := d.Action(WebAction{Kind: "arbitrary-request"}); err == nil {
		t.Fatal("unrecognized operation accepted")
	}
	if _, err := d.Action(WebAction{Kind: "select", ID: "missing"}); err == nil {
		t.Fatal("unknown conversation selected")
	}
}

func TestDesktopWorkDir(t *testing.T) {
	d, st := testDesktop(t)
	desktopAction(t, d, WebAction{Kind: "save-bot", Bot: store.Bot{ID: "ada", Name: "Ada", Engine: "echo"}})
	got, err := d.WorkDir("ada")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := st.WorkDir("ada")
	if got != want {
		t.Fatalf("work dir = %q, want %q", got, want)
	}
	if _, err := d.WorkDir("missing"); err == nil {
		t.Fatal("unknown bot must not resolve a work dir")
	}
}

func TestWebFrontendAssets(t *testing.T) {
	for _, name := range []string{"index.html", "app.js", "style.css", "markdown.js"} {
		data, err := fs.ReadFile(web.Assets, name)
		if err != nil || len(data) == 0 {
			t.Fatalf("missing embedded asset %s: %v", name, err)
		}
	}
	data, _ := fs.ReadFile(web.Assets, "index.html")
	if !strings.Contains(string(data), "Content-Security-Policy") || !strings.Contains(string(data), "connect-src 'none'") {
		t.Fatal("frontend must not connect directly to the authenticated service")
	}
}
