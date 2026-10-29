package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"bqbot/internal/driver"
	"bqbot/internal/mcp"
	"bqbot/internal/store"
)

// asker is an engine whose turn "ask <name> <question>" calls ask_bot
// through the grant the harness gave it, as the bqbot MCP server would,
// and replies with the answer. Without a grant it says so.
type asker struct {
	grants chan mcp.Grant // the grant of each turn that had one
}

func (d *asker) Name() string   { return "asker" }
func (d *asker) Binary() string { return "" }

func (d *asker) Run(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
	if in.MCP == nil {
		emit(driver.Event{Kind: driver.EvText, Text: "no bqbot tools"})
		return nil
	}
	b, err := os.ReadFile(in.MCP.Args[len(in.MCP.Args)-1])
	if err != nil {
		return err
	}
	var g mcp.Grant
	if err := json.Unmarshal(b, &g); err != nil {
		return err
	}
	if d.grants != nil {
		d.grants <- g
	}
	c := &mcp.HTTPBackend{URL: g.URL, Token: g.Token}
	f := strings.Fields(in.Text)
	if len(f) < 3 || f[0] != "ask" {
		emit(driver.Event{Kind: driver.EvText, Text: "ok"})
		return nil
	}
	emit(driver.Event{Kind: driver.EvTool, Text: mcp.ToolPrefix + mcp.ToolAsk})
	ans, err := c.AskBot(ctx, f[1], strings.Join(f[2:], " "))
	if err != nil {
		emit(driver.Event{Kind: driver.EvText, Text: "failed: " + err.Error()})
		return nil
	}
	emit(driver.Event{Kind: driver.EvText, Text: "got: " + ans})
	return nil
}

type rig struct {
	t    *testing.T
	st   *store.Store
	h    *Harness
	done chan Update
	ups  chan Update
}

func newRig(t *testing.T, drivers ...driver.Driver) *rig {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := New(st, drivers...)
	if err := h.Listen("/path/to/bqbot"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	r := &rig{t: t, st: st, h: h, done: make(chan Update, 16), ups: make(chan Update, 1024)}
	h.OnUpdate = func(u Update) {
		r.ups <- u
		if u.Done {
			r.done <- u
		}
	}
	return r
}

func (r *rig) bot(name, engine, persona string) store.Bot {
	b := store.Bot{ID: store.NewID(), Name: name, Engine: engine, Persona: persona}
	if err := r.st.SaveBot(b); err != nil {
		r.t.Fatal(err)
	}
	return b
}

// wait returns the next finished turn of the bot.
func (r *rig) wait(botID string) Update {
	r.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case u := <-r.done:
			if u.BotID == botID {
				return u
			}
		case <-deadline:
			r.t.Fatal("the turn did not end")
		}
	}
}

func TestAskBot(t *testing.T) {
	r := newRig(t, &asker{}, &driver.Echo{})
	ada := r.bot("Ada", "asker", "")
	bo := r.bot("Bo", "echo", "Reviews code.")
	if _, err := r.h.Send(bo, "hello"); err != nil { // Bo's conversation with the person
		t.Fatal(err)
	}
	r.wait(bo.ID)
	if _, err := r.h.Send(ada, "ask bo what is 2+2"); err != nil {
		t.Fatal(err)
	}
	// Bo's handoff turn shows to the UI only as busy, answering Ada.
	if u := r.wait(bo.ID); u.Reply != nil || u.Answering != "Ada" {
		t.Errorf("Bo's update %+v", u)
	}
	u := r.wait(ada.ID)
	if !strings.HasPrefix(u.Reply.Text, "got: Echo: Ada, another") || !strings.HasSuffix(u.Reply.Text, "what is 2+2") {
		t.Errorf("Ada's reply %q", u.Reply.Text)
	}
	// The handoff line replaces the raw tool line.
	if strings.Join(u.Reply.Tools, "|") != "已转交给 Bo" {
		t.Errorf("Ada's tools %q", u.Reply.Tools)
	}
	// The question and answer are kept apart from Bo's conversation with
	// the person, and in a session of their own.
	msgs, _ := r.st.Transcript(bo.ID)
	if len(msgs) != 2 || msgs[0].Text != "hello" {
		t.Errorf("Bo's transcript %+v", msgs)
	}
	hand, _ := r.st.HandoffTranscript(bo.ID, ada.ID)
	if len(hand) != 2 || hand[0].From != "Ada" || hand[0].Text != "what is 2+2" || !strings.HasSuffix(hand[1].Text, "what is 2+2") {
		t.Errorf("Bo's handoff transcript %+v", hand)
	}
	bots, _ := r.st.Bots()
	var boNow store.Bot
	for _, b := range bots {
		if b.ID == bo.ID {
			boNow = b
		}
	}
	if boNow.Cursor == "" || boNow.Handoffs[ada.ID] == "" || boNow.Handoffs[ada.ID] == boNow.Cursor {
		t.Errorf("Bo's sessions: cursor %q, handoffs %v", boNow.Cursor, boNow.Handoffs)
	}
	for len(r.ups) > 0 {
		if u := <-r.ups; u.BotID == bo.ID && u.Answering != "" && u.Reply != nil {
			t.Errorf("the UI got Bo's handoff reply: %+v", u)
		}
	}
}

// A second question from the same bot resumes their session; another
// asker gets a session of its own.
func TestHandoffSessions(t *testing.T) {
	cursors := make(chan string, 4)
	r := newRig(t, &asker{}, &cursorEcho{seen: cursors})
	ada := r.bot("Ada", "asker", "")
	cy := r.bot("Cy", "asker", "")
	bo := r.bot("Bo", "cecho", "")
	r.h.Send(ada, "ask Bo one")
	r.wait(ada.ID)
	r.h.Send(ada, "ask Bo two")
	r.wait(ada.ID)
	r.h.Send(cy, "ask Bo three")
	r.wait(cy.ID)
	if a, b, c := <-cursors, <-cursors, <-cursors; a != "" || b != "s1" || c != "" {
		t.Errorf("Bo resumed %q, %q, %q", a, b, c)
	}
	bots, _ := r.st.Bots()
	for _, b := range bots {
		if b.ID == bo.ID && (b.Handoffs[ada.ID] != "s1" || b.Handoffs[cy.ID] != "s2" || b.Cursor != "") {
			t.Errorf("Bo %+v", b)
		}
	}
}

// cursorEcho reports the session each turn resumes and starts s1, s2, ...
type cursorEcho struct {
	seen chan string
	n    int
}

func (d *cursorEcho) Name() string   { return "cecho" }
func (d *cursorEcho) Binary() string { return "" }
func (d *cursorEcho) Run(_ context.Context, in driver.TurnInput, emit func(driver.Event)) error {
	d.seen <- in.Cursor
	if in.Cursor == "" {
		d.n++
		emit(driver.Event{Kind: driver.EvSession, Cursor: fmt.Sprintf("s%d", d.n)})
	}
	emit(driver.Event{Kind: driver.EvText, Text: "ok"})
	return nil
}

func TestAskDepthLimit(t *testing.T) {
	r := newRig(t, &asker{})
	ada := r.bot("Ada", "asker", "")
	bo := r.bot("Bo", "asker", "")
	r.h.Send(ada, "ask Bo ask Ada again")
	// Bo answers Ada without bqbot tools, so it cannot ask in turn.
	r.wait(bo.ID)
	if u := r.wait(ada.ID); u.Reply.Text != "got: no bqbot tools" {
		t.Errorf("Ada's reply %q", u.Reply.Text)
	}
}

func TestAskBusyAndUnknown(t *testing.T) {
	r := newRig(t, &asker{}, &driver.Echo{Delay: 5 * time.Millisecond})
	ada := r.bot("Ada", "asker", "")
	bo := r.bot("Bo", "echo", "")
	if _, err := r.h.Send(bo, "a long answer"); err != nil {
		t.Fatal(err)
	}
	r.h.Send(ada, "ask Bo anything")
	u := r.wait(ada.ID)
	if !strings.HasPrefix(u.Reply.Text, "got: Echo: Ada, another") {
		t.Fatalf("queued handoff: %q", u.Reply.Text)
	}
	if strings.Join(u.Reply.Tools, "|") != "已转交给 Bo" {
		t.Fatalf("tools: %v", u.Reply.Tools)
	}

	r.h.Send(ada, "ask Nobody anything")
	if u := r.wait(ada.ID); !strings.Contains(u.Reply.Text, `no bot is named "Nobody"`) {
		t.Errorf("unknown: %q", u.Reply.Text)
	}
	r.h.Send(ada, "ask ada anything")
	if u := r.wait(ada.ID); u.Reply.Text != "failed: you cannot ask yourself" {
		t.Errorf("self: %q", u.Reply.Text)
	}
}

func TestStopCallerStopsTarget(t *testing.T) {
	r := newRig(t, &asker{}, &driver.Echo{Delay: time.Second})
	ada := r.bot("Ada", "asker", "")
	bo := r.bot("Bo", "echo", "")
	r.h.Send(ada, "ask Bo one two three four")
	deadline := time.After(5 * time.Second)
	for !r.h.Busy(bo.ID) {
		select {
		case <-deadline:
			t.Fatal("Bo never started")
		case <-time.After(10 * time.Millisecond):
		}
	}
	// Busy reserves a FIFO slot before prepare writes the user message. This
	// test stops an executing handoff; queued cancellation is covered separately.
	eventually(t, func() bool { msgs, err := r.st.HandoffTranscript(bo.ID, ada.ID); return err == nil && len(msgs) > 0 })
	r.h.Stop(ada.ID)
	r.wait(bo.ID)
	r.wait(ada.ID)
	if hand, _ := r.st.HandoffTranscript(bo.ID, ada.ID); len(hand) != 2 || !strings.Contains(hand[1].Text, "*(stopped)*") {
		t.Errorf("Bo's handoff transcript %+v", hand)
	}
}

func TestGrantRevoked(t *testing.T) {
	d := &asker{grants: make(chan mcp.Grant, 1)}
	r := newRig(t, d)
	ada := r.bot("Ada", "asker", "")
	r.h.Send(ada, "hello")
	g := <-d.grants
	r.wait(ada.ID)
	c := &mcp.HTTPBackend{URL: g.URL, Token: g.Token}
	if _, err := c.ListBots(context.Background()); err == nil {
		t.Error("the token still works after the turn")
	}
	c.Token = ""
	if _, err := c.ListBots(context.Background()); err == nil {
		t.Error("no token works")
	}
	entries, _ := os.ReadDir(os.TempDir())
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "bqbot-grant-") {
			b, _ := os.ReadFile(os.TempDir() + "/" + e.Name())
			if strings.Contains(string(b), g.Token) {
				t.Error("the grant file was not removed")
			}
		}
	}
}

func TestPersonaRoster(t *testing.T) {
	r := newRig(t)
	ada := r.bot("Ada", "asker", "Plans trips.")
	r.bot("Bo", "echo", "Reviews   code\nin Go.")
	if p := r.h.persona(ada, false); strings.Contains(p, "Bo") {
		t.Errorf("roster without handoff: %q", p)
	}
	p := r.h.persona(ada, true)
	if !strings.Contains(p, "Plans trips.") || !strings.Contains(p, "\n- Bo: Reviews code in Go.") ||
		strings.Contains(p, "- Ada") || !strings.Contains(p, "mcp__bqbot__ask_bot") {
		t.Errorf("persona %q", p)
	}
}
