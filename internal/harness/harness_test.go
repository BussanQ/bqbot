package harness

import (
	"testing"
	"time"

	"bqbot/internal/driver"
	"bqbot/internal/store"
)

func TestTurn(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := New(st, &driver.Echo{})
	done := make(chan Update, 1)
	h.OnUpdate = func(u Update) {
		if u.Done {
			done <- u
		}
	}
	bot := store.Bot{ID: store.NewID(), Name: "Ada", Engine: "echo"}
	if err := st.SaveBot(bot); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Send(bot, "  hello world "); err != nil {
		t.Fatal(err)
	}
	var u Update
	select {
	case u = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the turn did not end")
	}
	if u.Err != nil || u.Reply.Text != "Echo: hello world" {
		t.Fatalf("update %+v", u)
	}
	msgs, _ := st.Transcript(bot.ID)
	if len(msgs) != 2 || msgs[0].Text != "hello world" || msgs[1].Role != "assistant" {
		t.Errorf("transcript %+v", msgs)
	}
	bots, _ := st.Bots()
	if bots[0].Cursor == "" {
		t.Error("the session cursor was not saved")
	}
	if h.Busy(bot.ID) {
		t.Error("still busy")
	}
}

func TestBusyAndStop(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	h := New(st, &driver.Echo{Delay: time.Second})
	done := make(chan Update, 1)
	h.OnUpdate = func(u Update) {
		if u.Done {
			done <- u
		}
	}
	bot := store.Bot{ID: store.NewID(), Name: "Ada", Engine: "echo"}
	st.SaveBot(bot)
	if _, err := h.Send(bot, "one two three"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Send(bot, "again"); err != ErrBusy {
		t.Errorf("second send: %v", err)
	}
	h.Stop(bot.ID)
	select {
	case u := <-done:
		if u.Err != nil || u.Reply.Text != "*(stopped)*" {
			t.Errorf("update %+v", u)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not end the turn")
	}
}

func TestUnknownEngine(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	h := New(st)
	if _, err := h.Send(store.Bot{ID: "abc", Engine: "nope"}, "hi"); err == nil {
		t.Error("no error for an engine that is not available")
	}
}
