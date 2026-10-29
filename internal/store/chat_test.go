package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func phase3Store(t *testing.T) (*Store, Bot, Bot) {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := Bot{ID: "alpha", Name: "Alpha", Engine: "echo", Cursor: "main-alpha", Pinned: true, Avatar: "🤖", Created: time.Now()}
	b := Bot{ID: "beta", Name: "Beta", Engine: "echo", Handoffs: map[string]string{"alpha": "legacy-beta"}, Created: time.Now()}
	for _, bot := range []Bot{a, b} {
		if err := s.SaveBot(bot); err != nil {
			t.Fatal(err)
		}
	}
	return s, a, b
}
func mustStore(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func TestThreadsMigrateAndKeepIndependentHistories(t *testing.T) {
	s, a, b := phase3Store(t)
	mustStore(t, s.Append(a.ID, Message{ID: "old-main", Role: "user", Text: "main history"}))
	mustStore(t, s.AppendHandoff(b.ID, a.ID, Message{ID: "old-hand", Role: "assistant", Text: "handoff history"}))
	first, err := s.Threads(b.ID)
	mustStore(t, err)
	again, err := s.Threads(b.ID)
	mustStore(t, err)
	if len(first) != 2 || len(again) != 2 || first[1].ID != again[1].ID {
		t.Fatalf("migration not idempotent: %+v %+v", first, again)
	}
	hand, err := s.HandoffThread(b.ID, a.ID)
	mustStore(t, err)
	if hand.Cursor != "legacy-beta" || !hand.Legacy {
		t.Fatalf("legacy continuation lost: %+v", hand)
	}
	rows, err := s.ThreadTranscript(hand.ID)
	mustStore(t, err)
	if len(rows) != 1 || rows[0].Text != "handoff history" {
		t.Fatalf("history lost: %+v", rows)
	}
	mustStore(t, s.CloseThread(hand.ID))
	fresh, err := s.HandoffThread(b.ID, a.ID)
	mustStore(t, err)
	if fresh.ID == hand.ID || fresh.Cursor != "" {
		t.Fatalf("closed thread reused: %+v", fresh)
	}
	mustStore(t, s.AppendThread(fresh.ID, Message{ID: "fresh", Role: "user", Text: "new handoff"}))
	rows, err = s.HandoffTranscript(b.ID, a.ID)
	mustStore(t, err)
	if len(rows) != 1 {
		t.Fatal("new handoff modified archived legacy history")
	}
	mustStore(t, s.SaveThread(Thread{ID: "extra", BotID: a.ID, Title: "Another topic", Cursor: "untrusted"}))
	extra, err := s.Thread("extra")
	mustStore(t, err)
	if extra.Cursor != "" {
		t.Fatal("profile injected cursor")
	}
	mustStore(t, s.AppendThread(extra.ID, Message{ID: "extra-msg", Role: "user", Text: "separate"}))
	rows, err = s.Transcript(a.ID)
	mustStore(t, err)
	if len(rows) != 1 || rows[0].Text != "main history" {
		t.Fatalf("main changed: %+v", rows)
	}
	if err := s.CloseThread(a.ID); err == nil {
		t.Fatal("closed main")
	}
	reopened, err := Open(s.dir)
	mustStore(t, err)
	rows, err = reopened.ThreadTranscript(extra.ID)
	mustStore(t, err)
	if len(rows) != 1 {
		t.Fatal("thread not persistent")
	}
}
func TestThreadCursorAndCompactionPreserveTranscript(t *testing.T) {
	s, a, _ := phase3Store(t)
	mustStore(t, s.SaveThread(Thread{ID: "topic", BotID: a.ID, Title: "Topic"}))
	before, err := s.Thread("topic")
	mustStore(t, err)
	mustStore(t, s.SaveThreadCursor(before, "native-session"))
	stale, err := s.Thread("topic")
	mustStore(t, err)
	for i := 0; i < 28; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		mustStore(t, s.AppendThread("topic", Message{ID: fmt.Sprintf("m%d", i), Role: role, Text: fmt.Sprintf("Important fact %d", i)}))
	}
	compacted, err := s.CompactThread("topic")
	mustStore(t, err)
	if compacted.Cursor != "" || compacted.SummaryThrough != "m15" || !strings.Contains(compacted.Summary, "Important fact 0") {
		t.Fatalf("compaction %+v", compacted)
	}
	mustStore(t, s.SaveThreadCursor(stale, "stale-session"))
	after, err := s.Thread("topic")
	mustStore(t, err)
	if after.Cursor != "" {
		t.Fatal("stale cursor restored after compaction")
	}
	rows, err := s.ThreadTranscript("topic")
	mustStore(t, err)
	if len(rows) != 28 {
		t.Fatal("compaction deleted original messages")
	}
	mustStore(t, s.SaveThreadCursor(after, "resumed"))
	a.Model = "different"
	mustStore(t, s.SaveProfile(a))
	after, err = s.Thread("topic")
	mustStore(t, err)
	if after.Cursor != "" {
		t.Fatal("model change retained native session")
	}
	mustStore(t, s.SaveThreadCursor(compacted, "obsolete"))
	after, err = s.Thread("topic")
	mustStore(t, err)
	if after.Cursor != "" {
		t.Fatal("old engine turn restored cursor")
	}
}
func TestThreadModelRoundTripDoesNotRestoreCursor(t *testing.T) {
	s, a, _ := phase3Store(t)
	mustStore(t, s.SaveThread(Thread{ID: "roundtrip", BotID: a.ID, Title: "Round trip"}))
	initial, err := s.Thread("roundtrip")
	mustStore(t, err)
	mustStore(t, s.SaveThreadCursor(initial, "old-session"))
	a.Model = "model-b"
	mustStore(t, s.SaveProfile(a))
	a.Model = ""
	mustStore(t, s.SaveProfile(a))
	current, err := s.Thread("roundtrip")
	mustStore(t, err)
	if current.Cursor != "" {
		t.Fatal("old cursor revived after A to B to A")
	}
	mustStore(t, s.SaveThreadCursor(initial, "late-session-from-model-a"))
	current, err = s.Thread("roundtrip")
	mustStore(t, err)
	if current.Cursor != "" {
		t.Fatal("turn from previous model generation restored cursor")
	}
}

func TestAppendCannotRecreateDeletedConversation(t *testing.T) {
	for _, kind := range []string{"thread", "room"} {
		t.Run(kind, func(t *testing.T) {
			s, a, _ := phase3Store(t)
			var appendMessage func() error
			var remove func() error
			var path string
			if kind == "room" {
				mustStore(t, s.SaveRoom(Room{ID: "team", Name: "Team", Members: []string{a.ID}, LeadID: a.ID}))
				appendMessage = func() error { return s.AppendRoom("team", Message{ID: NewID(), Text: "racing"}) }
				remove = func() error { return s.DeleteRoom("team") }
				path = s.roomTranscriptPath("team")
			} else {
				mustStore(t, s.SaveThread(Thread{ID: "topic", BotID: a.ID, Title: "Topic"}))
				thread, err := s.Thread("topic")
				mustStore(t, err)
				path = s.conversationPath(thread)
				appendMessage = func() error { return s.AppendThread("topic", Message{ID: NewID(), Text: "racing"}) }
				remove = func() error { return s.DeleteBot(a.ID) }
			}
			var wg sync.WaitGroup
			start := make(chan struct{})
			for range 30 {
				wg.Add(1)
				go func() { defer wg.Done(); <-start; _ = appendMessage() }()
			}
			close(start)
			mustStore(t, remove())
			wg.Wait()
			if err := appendMessage(); err == nil {
				t.Fatal("append after delete succeeded")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("deleted transcript reappeared: %v", err)
			}
		})
	}
}

func TestRoomsMemorySearchCloneAndDelete(t *testing.T) {
	s, a, b := phase3Store(t)
	owned := t.TempDir()
	mustStore(t, os.WriteFile(filepath.Join(owned, "keep.txt"), []byte("user work"), 0600))
	room := Room{ID: "team", Name: "Team", Instructions: "Shared instructions", WorkDir: owned, Members: []string{a.ID, b.ID}, LeadID: a.ID, ReplyMode: "auto"}
	mustStore(t, s.SaveRoom(room))
	dir, err := s.RoomWorkDir(room.ID)
	mustStore(t, err)
	if dir != owned {
		t.Fatal(dir)
	}
	rt, err := s.RoomThread(room.ID, b.ID)
	mustStore(t, err)
	mustStore(t, s.AppendEvent(rt.ID, "", map[string]string{"event": "room reply"}))
	mustStore(t, s.AppendRoom(room.ID, Message{ID: "r1", Role: "assistant", BotID: b.ID, Text: "Needle in room", At: time.Now()}))
	mustStore(t, s.Append(a.ID, Message{ID: "a1", Role: "user", Text: "needle in main", At: time.Now()}))
	mustStore(t, s.SaveMemory(a.ID, Memory{ID: "note", Text: "Prefers Go and Chinese"}))
	mustStore(t, s.SaveMemory(room.ID, Memory{ID: "room-note", Text: "Release Friday"}))
	recall, err := s.Recall(a.ID, "Go")
	mustStore(t, err)
	if len(recall) != 1 || recall[0].ID != "note" {
		t.Fatalf("recall %+v", recall)
	}
	matches, err := s.Search("NEEDLE")
	mustStore(t, err)
	if len(matches) != 2 {
		t.Fatalf("search %+v", matches)
	}
	clone, err := s.CloneBot(a.ID)
	mustStore(t, err)
	if clone.ID == a.ID || clone.Cursor != "" || len(clone.Handoffs) > 0 || clone.Avatar != a.Avatar || clone.Pinned {
		t.Fatalf("clone %+v", clone)
	}
	cloneAgain, err := s.CloneBot(a.ID)
	mustStore(t, err)
	if cloneAgain.Name == clone.Name {
		t.Fatal("clones have ambiguous names")
	}
	rows, err := s.Transcript(clone.ID)
	mustStore(t, err)
	if len(rows) > 0 {
		t.Fatal("clone shared history")
	}
	mustStore(t, s.DeleteBot(a.ID))
	room, err = s.Room(room.ID)
	mustStore(t, err)
	if room.LeadID != b.ID || len(room.Members) != 1 {
		t.Fatalf("deleted member remained %+v", room)
	}
	if _, err := s.Memories(a.ID); err == nil {
		t.Fatal("deleted bot memories accessible")
	}
	mustStore(t, s.DeleteRoom(room.ID))
	if _, err := s.Thread(rt.ID); err == nil {
		t.Fatal("deleted room thread remains")
	}
	if _, err := os.Stat(filepath.Join(s.dir, "events", rt.ID+".ndjson")); !os.IsNotExist(err) {
		t.Fatalf("deleted room runtime events remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(owned, "keep.txt")); err != nil {
		t.Fatal("deleted user-owned room work directory")
	}
	for _, file := range []string{s.threadIndexPath(), s.roomsPath(), s.botsPath()} {
		info, err := os.Stat(file)
		mustStore(t, err)
		if info.Mode().Perm() != 0600 {
			t.Fatalf("permissions %s: %o", file, info.Mode().Perm())
		}
	}
}
func TestChatStorageRejectsInvalidReferences(t *testing.T) {
	s, a, _ := phase3Store(t)
	for _, thread := range []Thread{{ID: "../bad", BotID: a.ID, Title: "x"}, {ID: "new", BotID: "missing", Title: "x"}, {ID: "new", BotID: a.ID, Title: "x", AskerID: "missing"}} {
		if err := s.SaveThread(thread); err == nil {
			t.Fatalf("accepted %+v", thread)
		}
	}
	for _, room := range []Room{{ID: "room", Name: "x", Members: []string{a.ID}, LeadID: "missing"}, {ID: "room", Name: "x", Members: []string{a.ID, a.ID}, LeadID: a.ID}, {ID: "room", Name: "x", Members: []string{a.ID}, LeadID: a.ID, WorkDir: "relative"}} {
		if err := s.SaveRoom(room); err == nil {
			t.Fatalf("accepted %+v", room)
		}
	}
	if err := s.SaveMemory("../bad", Memory{ID: "note", Text: "x"}); err == nil {
		t.Fatal("accepted memory traversal")
	}
	if err := s.SaveDecisionSettings(DecisionSettings{BaseURL: "https://key:secret@example.com", Model: "x"}); err == nil {
		t.Fatal("accepted credentials in URL")
	}
	if err := s.SaveDecisionSettings(DecisionSettings{APIKeyEnv: "BAD-KEY"}); err == nil {
		t.Fatal("accepted invalid env name")
	}
	mustStore(t, s.SaveDecisionSettings(DecisionSettings{Model: "test/model"}))
	settings, err := s.DecisionSettings()
	mustStore(t, err)
	if settings.APIKeyEnv != "OPENROUTER_API_KEY" || settings.BaseURL != "https://openrouter.ai/api/v1" {
		t.Fatalf("defaults %+v", settings)
	}
}
func TestNewHandoffKeepsLegacyAPIsInSync(t *testing.T) {
	s, a, b := phase3Store(t)
	b.Handoffs = nil
	mustStore(t, s.SaveBot(b))
	thread, err := s.HandoffThread(b.ID, a.ID)
	mustStore(t, err)
	mustStore(t, s.AppendThread(thread.ID, Message{ID: "hello", Role: "user", Text: "new question"}))
	mustStore(t, s.SaveThreadCursor(thread, "new-session"))
	rows, err := s.HandoffTranscript(b.ID, a.ID)
	mustStore(t, err)
	if len(rows) != 1 {
		t.Fatalf("legacy transcript API lost new handoff: %+v", rows)
	}
	bots, err := s.Bots()
	mustStore(t, err)
	if bots[1].Handoffs[a.ID] != "new-session" {
		t.Fatal("legacy cursor API did not advance")
	}
	threads, err := s.Threads(b.ID)
	mustStore(t, err)
	if len(threads) != 2 {
		t.Fatalf("duplicate handoff metadata: %+v", threads)
	}
}

func TestConcurrentHandoffThreadCreation(t *testing.T) {
	s, a, b := phase3Store(t)
	var wg sync.WaitGroup
	ids := make(chan string, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			thread, err := s.HandoffThread(b.ID, a.ID)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- thread.ID
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if first != id {
			t.Fatal("duplicate threads created concurrently")
		}
	}
}
