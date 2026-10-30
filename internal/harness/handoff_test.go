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
	if strings.HasPrefix(in.Text, "Handoff completed") || strings.HasPrefix(in.Text, "Handoff to ") {
		emit(driver.Event{Kind: driver.EvText, Text: "resumed: " + in.Text})
		return nil
	}
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
	text := in.Text
	if strings.Contains(text, "another of the person's bots, asks you this") {
		_, text, _ = strings.Cut(text, "\n\n")
	}
	f := strings.Fields(text)
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
	if _, err := r.h.Send(bo, "hello"); err != nil {
		t.Fatal(err)
	}
	r.wait(bo.ID)
	if _, err := r.h.Send(ada, "ask bo what is 2+2"); err != nil {
		t.Fatal(err)
	}
	receipt := r.wait(ada.ID)
	if !strings.Contains(receipt.Reply.Text, `"status":"accepted"`) || strings.Join(receipt.Reply.Tools, "|") != "已转交给 Bo" {
		t.Fatalf("receipt: %+v", receipt.Reply)
	}
	waitJobs(t, r.h)
	main, _ := r.st.Transcript(ada.ID)
	if len(main) != 4 || !strings.Contains(main[3].Text, "Echo: Ada, another") || !strings.Contains(main[3].Text, "what is 2+2") {
		t.Fatalf("asker transcript: %+v", main)
	}
	thread, err := r.st.HandoffThread(bo.ID, ada.ID)
	if err != nil {
		t.Fatal(err)
	}
	hand, _ := r.st.ThreadTranscript(thread.ID)
	if len(hand) != 2 || hand[0].From != "Ada" || hand[0].Text != "what is 2+2" {
		t.Fatalf("handoff: %+v", hand)
	}
	messages, _ := r.st.Transcript(bo.ID)
	if len(messages) != 2 || messages[0].Text != "hello" {
		t.Fatalf("main polluted: %+v", messages)
	}
	current, _ := r.st.Thread(thread.ID)
	bot, _ := r.h.Bot(bo.ID)
	if current.Cursor == "" || current.Cursor == bot.Cursor {
		t.Fatalf("cursors: %+v %+v", current, bot)
	}
	seen := false
	for len(r.ups) > 0 {
		u := <-r.ups
		if u.ThreadID == thread.ID && u.Done && u.Reply != nil {
			seen = true
		}
	}
	if !seen {
		t.Fatal("handoff was not visible in target thread")
	}
}
func waitJobs(t *testing.T, h *Harness) {
	t.Helper()
	eventually(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.jobs) == 0 && len(h.running) == 0 })
}

// A second question from the same bot resumes their session; another
// asker gets a session of its own.
func TestHandoffSessions(t *testing.T) {
	cursors := make(chan string, 4)
	r := newRig(t, &asker{}, &cursorEcho{seen: cursors})
	ada := r.bot("Ada", "asker", "")
	cy := r.bot("Cy", "asker", "")
	bo := r.bot("Bo", "cecho", "")
	for _, v := range []struct {
		bot  store.Bot
		text string
	}{{ada, "ask Bo one"}, {ada, "ask Bo two"}, {cy, "ask Bo three"}} {
		r.h.Send(v.bot, v.text)
		waitJobs(t, r.h)
	}
	if a, b, c := <-cursors, <-cursors, <-cursors; a != "" || b != "s1" || c != "" {
		t.Fatalf("resumed %q,%q,%q", a, b, c)
	}
	first, _ := r.st.HandoffThread(bo.ID, ada.ID)
	second, _ := r.st.HandoffThread(bo.ID, cy.ID)
	if first.Cursor != "s1" || second.Cursor != "s2" {
		t.Fatalf("sessions %+v %+v", first, second)
	}
	if err := r.h.CloseThread(first.ID); err != nil {
		t.Fatal(err)
	}
	next, _ := r.st.HandoffThread(bo.ID, ada.ID)
	if next.ID == first.ID || next.Cursor != "" {
		t.Fatalf("closed thread reused: %+v", next)
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

func TestAskAncestorRejected(t *testing.T) {
	r := newRig(t, &asker{})
	ada := r.bot("Ada", "asker", "")
	bo := r.bot("Bo", "asker", "")
	r.h.Send(ada, "ask Bo ask Ada again")
	waitJobs(t, r.h)
	thread, _ := r.st.HandoffThread(bo.ID, ada.ID)
	hand, _ := r.st.ThreadTranscript(thread.ID)
	if len(hand) != 2 || !strings.Contains(hand[1].Text, "ancestor") {
		t.Fatalf("depth limit: %+v", hand)
	}
	main, _ := r.st.Transcript(ada.ID)
	if len(main) != 4 || !strings.Contains(main[3].Text, "ancestor") {
		t.Fatalf("wake: %+v", main)
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
	if !strings.Contains(u.Reply.Text, `"status":"accepted"`) {
		t.Fatalf("queued handoff: %q", u.Reply.Text)
	}
	if strings.Join(u.Reply.Tools, "|") != "已转交给 Bo" {
		t.Fatalf("tools: %v", u.Reply.Tools)
	}

	r.wait(ada.ID) // automatic completion turn
	waitJobs(t, r.h)
	r.h.Send(ada, "ask Nobody anything")
	if u := r.wait(ada.ID); !strings.Contains(u.Reply.Text, `no bot is named "Nobody"`) {
		t.Errorf("unknown: %q", u.Reply.Text)
	}
	r.h.Send(ada, "ask ada anything")
	if u := r.wait(ada.ID); !strings.Contains(u.Reply.Text, "cannot delegate to yourself") {
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
	eventually(t, func() bool {
		thread, err := r.st.HandoffThread(bo.ID, ada.ID)
		if err != nil {
			return false
		}
		msgs, err := r.st.ThreadTranscript(thread.ID)
		return err == nil && len(msgs) > 0
	})
	r.h.Stop(ada.ID)
	r.wait(bo.ID)
	waitJobs(t, r.h)
	thread, _ := r.st.HandoffThread(bo.ID, ada.ID)
	if hand, _ := r.st.ThreadTranscript(thread.ID); len(hand) != 2 || !strings.Contains(hand[1].Text, "*(stopped)*") {
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
