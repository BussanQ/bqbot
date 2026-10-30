package webgo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"bqbot/internal/driver"
	"bqbot/internal/harness"
	"bqbot/internal/mcp"
	"bqbot/internal/service"
	"bqbot/internal/store"
)

var testStores sync.Map

func testStore(a *app) *store.Store {
	st, _ := testStores.Load(a)
	return st.(*store.Store)
}

func newTestApp(t *testing.T, st *store.Store, h *harness.Harness, post func(func())) (*app, error) {
	t.Helper()
	srv, err := service.Listen(h, "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	a, err := newApp(service.NewClient(srv.URL, srv.Token), post)
	if err != nil {
		srv.Close()
		return nil, err
	}
	testStores.Store(a, st)
	a.connect()
	t.Cleanup(func() { a.cancel(); srv.Close(); h.Close(); testStores.Delete(a) })
	return a, nil
}

// testApp's harness posts to a channel that the test drains on the "main
// thread", as Window.Update would.
func testApp(t *testing.T) (*app, chan func()) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := harness.New(st, &driver.Echo{}, &driver.Claude{})
	posts := make(chan func(), 1024)
	a, err := newTestApp(t, st, h, func(fn func()) { posts <- fn })
	if err != nil {
		t.Fatal(err)
	}
	return a, posts
}

// waitIdle runs posted updates until the bot's turn is over.
func waitIdle(t *testing.T, a *app, tt *ui.Tester, posts chan func()) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case fn := <-posts:
			fn()
			tt.Frame()
			if !a.thread(a.selected).busy {
				return
			}
		case <-deadline:
			t.Fatal("the turn did not end")
		}
	}
}

func TestCreateChatDelete(t *testing.T) {
	a, posts := testApp(t)
	tt := ui.NewTester(a.view, 1000, 700)
	if !tt.HasText("Create a bot to start chatting.") {
		t.Fatalf("texts %q", tt.Texts())
	}
	if err := tt.Click("New bot"); err != nil {
		t.Fatal(err)
	}
	tt.Type("Ada")
	if err := tt.Click("Create"); err != nil {
		t.Fatal(err)
	}
	if len(a.bots) != 1 || a.bots[0].Name != "Ada" || a.bots[0].Engine != "echo" {
		t.Fatalf("bots %+v, form error %q", a.bots, a.formErr)
	}
	if !tt.HasText("Say hello to Ada.") {
		t.Fatalf("texts %q", tt.Texts())
	}

	if err := tt.Click("Message"); err != nil {
		t.Fatal(err)
	}
	tt.Type("hi there")
	tt.Key(0, ui.KeyEnter)
	if !tt.HasText("hi there") {
		t.Fatalf("no message in %q", tt.Texts())
	}
	waitIdle(t, a, tt, posts)
	if !tt.HasText("Echo: hi there") {
		t.Fatalf("no reply in %q", tt.Texts())
	}
	if a.thread(a.selected).draft != "" {
		t.Errorf("draft %q after sending", a.thread(a.selected).draft)
	}

	// The conversation survives a restart.
	b, _ := newTestApp(t, testStore(a), harness.New(testStore(a), &driver.Echo{}), func(func()) {})
	tt2 := ui.NewTester(b.view, 1000, 700)
	if !tt2.HasText("hi there") || !tt2.HasText("Echo: hi there") {
		t.Errorf("after reopening, texts %q", tt2.Texts())
	}

	if err := tt.Click("Delete bot"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Delete"); err != nil { // the alert's button
		t.Fatal(err)
	}
	if len(a.bots) != 0 || !tt.HasText("Create a bot to start chatting.") {
		t.Errorf("after delete: bots %+v, texts %q", a.bots, tt.Texts())
	}
}

func TestCreateNeedsName(t *testing.T) {
	a, _ := testApp(t)
	tt := ui.NewTester(a.view, 1000, 700)
	tt.Click("New bot")
	tt.Click("Create")
	if len(a.bots) != 0 || !tt.HasText("Give the bot a name.") {
		t.Errorf("bots %+v, texts %q", a.bots, tt.Texts())
	}
}

// relay is an engine that asks Bo, through the bqbot tools of its turn,
// whatever the person says, and replies with Bo's answer.
type relay struct{}

func (relay) Name() string   { return "relay" }
func (relay) Binary() string { return "" }
func (relay) Run(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
	if strings.HasPrefix(in.Text, "Handoff completed by ") {
		_, result, ok := strings.Cut(in.Text, "\n\n")
		if !ok || in.MCP != nil {
			return errors.New("invalid completion turn")
		}
		emit(driver.Event{Kind: driver.EvText, Text: "Bo said: " + result})
		return nil
	}
	if in.MCP == nil {
		return errors.New("no bqbot tools")
	}
	b, err := os.ReadFile(in.MCP.Args[1])
	if err != nil {
		return err
	}
	var g mcp.Grant
	json.Unmarshal(b, &g)
	ans, err := (&mcp.HTTPBackend{URL: g.URL, Token: g.Token}).AskBot(ctx, "Bo", in.Text)
	if err != nil {
		return err
	}
	var receipt struct {
		Status string `json:"status"`
	}
	if json.Unmarshal([]byte(ans), &receipt) != nil || receipt.Status != "accepted" {
		return errors.New("handoff was not accepted")
	}
	emit(driver.Event{Kind: driver.EvText, Text: "Waiting for Bo's reply."})
	return nil
}

func TestHandoffInChat(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	h := harness.New(st, relay{}, &driver.Echo{})
	if err := h.Listen("bqbot"); err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	ada := store.Bot{ID: store.NewID(), Name: "Ada", Engine: "relay"}
	bo := store.Bot{ID: store.NewID(), Name: "Bo", Engine: "echo"}
	st.SaveBot(ada)
	st.SaveBot(bo)
	posts := make(chan func(), 1024)
	a, err := newTestApp(t, st, h, func(fn func()) { posts <- fn })
	if err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(a.view, 1000, 700)
	tt.Click("Message")
	tt.Type("ping")
	tt.Key(0, ui.KeyEnter)
	want := "Bo said: Echo: Ada, another of the person's bots, asks you this. Your answer goes back to Ada, not to the person. ping"
	pumpUntil(t, tt, posts, func() bool { return !a.thread(ada.ID).busy && tt.HasText(want) })
	if !tt.HasText("已转交给 Bo") || !tt.HasText(want) {
		t.Fatalf("Ada's chat %q", tt.Texts())
	}
	// Bo's chat with the person does not get Ada's question or its answer.
	for len(posts) > 0 {
		(<-posts)()
	}
	a.selected = bo.ID
	tt.Frame()
	if tt.HasText("ping") || len(a.thread(bo.ID).msgs) != 0 || a.thread(bo.ID).busy {
		t.Errorf("Bo's chat %q, thread %+v", tt.Texts(), a.thread(bo.ID).msgs)
	}
}

// While a bot answers another bot, its chat says so and the person waits.
func TestAnsweringLine(t *testing.T) {
	a, _ := testApp(t)
	b := store.Bot{ID: store.NewID(), Name: "Bo", Engine: "echo"}
	testStore(a).SaveBot(b)
	a.bots, a.selected = []store.Bot{b}, b.ID
	tt := ui.NewTester(a.view, 1000, 700)
	a.fold(harness.Update{BotID: b.ID, Busy: true, Answering: "Ada"})
	tt.Frame()
	if !tt.HasText("正在回答 Ada 的提问（单独的会话）") || !tt.HasText("Stop") {
		t.Fatalf("texts %q", tt.Texts())
	}
	a.fold(harness.Update{BotID: b.ID, Done: true})
	tt.Frame()
	if tt.HasText("正在回答 Ada 的提问（单独的会话）") {
		t.Errorf("texts %q", tt.Texts())
	}
}

func TestEditBot(t *testing.T) {
	a, _ := testApp(t)
	b := store.Bot{ID: store.NewID(), Name: "Ada", Engine: "echo", Cursor: "c1"}
	testStore(a).SaveBot(b)
	a.bots, a.selected = []store.Bot{b}, b.ID
	tt := ui.NewTester(a.view, 1000, 700)
	if err := tt.Click("Edit bot"); err != nil {
		t.Fatal(err)
	}
	if a.newName != "Ada" || !tt.HasText("Edit bot") {
		t.Fatalf("form %q, texts %q", a.newName, tt.Texts())
	}
	a.newName, a.newPersona = "Ada Lovelace", "Be brief."
	if err := tt.Click("Save"); err != nil {
		t.Fatal(err)
	}
	bots, _ := testStore(a).Bots()
	if len(bots) != 1 || bots[0].Name != "Ada Lovelace" || bots[0].Persona != "Be brief." || bots[0].Cursor != "c1" {
		t.Errorf("bots %+v", bots)
	}
	if a.creating || !tt.HasText("Ada Lovelace") {
		t.Errorf("texts %q", tt.Texts())
	}
	// A new model drops the sessions, which the old one made.
	a.openEdit(bots[0])
	a.newModel = "m2"
	a.saveForm()
	if bots, _ = testStore(a).Bots(); bots[0].Cursor != "" || bots[0].Model != "m2" {
		t.Errorf("after the model changed: %+v", bots[0])
	}
}

// A reply to a bot not shown counts as unread until the bot is chosen.
func TestUnreadBadge(t *testing.T) {
	a, _ := testApp(t)
	ada := store.Bot{ID: store.NewID(), Name: "Ada", Engine: "echo"}
	bo := store.Bot{ID: store.NewID(), Name: "Bo", Engine: "echo"}
	testStore(a).SaveBot(ada)
	testStore(a).SaveBot(bo)
	a.bots, a.selected = []store.Bot{ada, bo}, ada.ID
	tt := ui.NewTester(a.view, 1000, 700)
	a.fold(harness.Update{BotID: bo.ID, Done: true, Reply: &store.Message{ID: "r1", Role: "assistant", Text: "hi"}})
	tt.Frame()
	if a.thread(bo.ID).unread != 1 || !tt.HasText("1") {
		t.Fatalf("unread %d, texts %q", a.thread(bo.ID).unread, tt.Texts())
	}
	if err := tt.Click("Bo"); err != nil {
		t.Fatal(err)
	}
	if a.selected != bo.ID || a.thread(bo.ID).unread != 0 {
		t.Errorf("selected %q, unread %d", a.selected, a.thread(bo.ID).unread)
	}
}

func TestToolsFoldAndCopy(t *testing.T) {
	a, _ := testApp(t)
	b := store.Bot{ID: store.NewID(), Name: "Ada", Engine: "echo"}
	testStore(a).SaveBot(b)
	testStore(a).Append(b.ID, store.Message{ID: "m1", Role: "assistant", Text: "done it", Tools: []string{"已转交给 Bo", "Bash ls", "Read go.mod"}})
	a.bots, a.selected = []store.Bot{b}, b.ID
	tt := ui.NewTester(a.view, 1000, 700)
	if !tt.HasText("已转交给 Bo") || !tt.HasText("Ran 2 tools") || tt.HasText("Bash ls") {
		t.Fatalf("texts %q", tt.Texts())
	}
	if err := tt.Click("Ran 2 tools"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(time.Second)
	if !tt.HasText("Bash ls") {
		t.Errorf("after opening, texts %q", tt.Texts())
	}
	if err := tt.Click("Copy"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != "done it" {
		t.Errorf("clipboard %q", tt.Clipboard())
	}
}

func TestSessionsView(t *testing.T) {
	a, _ := testApp(t)
	ada := store.Bot{ID: store.NewID(), Name: "Ada", Engine: "echo"}
	bo := store.Bot{ID: store.NewID(), Name: "Bo", Engine: "echo"}
	testStore(a).SaveBot(ada)
	testStore(a).SaveBot(bo)
	testStore(a).AppendHandoff(bo.ID, ada.ID, store.Message{ID: "h1", Role: "user", Text: "what is 2+2?", From: "Ada"})
	testStore(a).AppendHandoff(bo.ID, ada.ID, store.Message{ID: "h2", Role: "assistant", Text: "four"})
	a.bots, a.selected = []store.Bot{ada, bo}, bo.ID
	tt := ui.NewTester(a.view, 1000, 700)
	if err := tt.Click("Handoff sessions"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("what is 2+2?") || !tt.HasText("four") {
		t.Errorf("texts %q", tt.Texts())
	}
}
