package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStore(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	b := Bot{ID: NewID(), Name: "Ada", Engine: "echo"}
	if err := s.SaveBot(b); err != nil {
		t.Fatal(err)
	}
	b.Cursor = "c1"
	s.SaveBot(b)
	bots, _ := s.Bots()
	if len(bots) != 1 || bots[0].Cursor != "c1" {
		t.Fatalf("bots %+v", bots)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "bots.json")); fi.Mode().Perm() != 0o600 {
		t.Errorf("bots.json mode %v", fi.Mode().Perm())
	}

	s.Append(b.ID, Message{ID: "1", Role: "user", Text: "hi"})
	// A line cut short by a crash is skipped.
	f, _ := os.OpenFile(filepath.Join(dir, "threads", b.ID+".ndjson"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"id":"2","ro` + "\n")
	f.Close()
	s.Append(b.ID, Message{ID: "3", Role: "assistant", Text: "hello"})
	msgs, err := s.Transcript(b.ID)
	if err != nil || len(msgs) != 2 || msgs[1].Text != "hello" {
		t.Fatalf("transcript %+v, %v", msgs, err)
	}

	// Handoff sessions are kept apart from the conversation with the person,
	// and go when either bot does.
	c := Bot{ID: NewID(), Name: "Bo", Engine: "echo", Handoffs: map[string]string{b.ID: "s9"}}
	s.SaveBot(c)
	s.AppendHandoff(b.ID, c.ID, Message{ID: "h1", Role: "user", Text: "q", From: "Bo"})
	s.AppendHandoff(c.ID, b.ID, Message{ID: "h2", Role: "user", Text: "q2", From: "Ada"})
	if h, _ := s.HandoffTranscript(b.ID, c.ID); len(h) != 1 || h[0].From != "Bo" {
		t.Errorf("handoff transcript %+v", h)
	}
	if msgs, _ := s.Transcript(b.ID); len(msgs) != 2 {
		t.Errorf("transcript %+v", msgs)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "handoffs", b.ID, c.ID+".ndjson")); fi == nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("handoff file %v", fi)
	}
	if _, err := s.HandoffTranscript(b.ID, "../x"); err == nil {
		t.Error("HandoffTranscript accepted a bad id")
	}

	work, _ := s.WorkDir(b.ID)
	if err := s.DeleteBot(b.ID); err != nil {
		t.Fatal(err)
	}
	bots, _ = s.Bots()
	msgs, _ = s.Transcript(b.ID)
	if _, err := os.Stat(work); len(bots) != 1 || len(msgs) != 0 || !os.IsNotExist(err) {
		t.Errorf("after delete: bots %v, msgs %v, workdir %v", bots, msgs, err)
	}
	if bots[0].Handoffs[b.ID] != "" {
		t.Errorf("Bo kept the cursor of its session with the deleted bot: %v", bots[0].Handoffs)
	}
	if h, _ := s.HandoffTranscript(c.ID, b.ID); len(h) != 0 {
		t.Errorf("Bo kept its session with the deleted bot: %+v", h)
	}
	if _, err := os.Stat(filepath.Join(dir, "handoffs", b.ID)); !os.IsNotExist(err) {
		t.Errorf("the deleted bot's handoffs are left: %v", err)
	}
}

func TestInvalidID(t *testing.T) {
	s, _ := Open(t.TempDir())
	for _, id := range []string{"", "../x", "a/b", "A"} {
		if _, err := s.WorkDir(id); err == nil {
			t.Errorf("WorkDir(%q) accepted", id)
		}
		if err := s.DeleteBot(id); err == nil {
			t.Errorf("DeleteBot(%q) accepted", id)
		}
	}
}
