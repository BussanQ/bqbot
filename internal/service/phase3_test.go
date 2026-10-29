package service

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"bqbot/internal/driver"
	"bqbot/internal/store"
)

func awaitIdle(t *testing.T, busy func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for busy() {
		if time.Now().After(deadline) {
			t.Fatal("runtime did not settle")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestPhase3HTTPThreadsRoomsAndMetadata(t *testing.T) {
	_, c, h := newTestService(t, &driver.Echo{})
	a := mustSave(t, c, "ada")
	b := mustSave(t, c, "bo")
	b.Name = "Bo"
	if err := c.SaveBot(b); err != nil {
		t.Fatal(err)
	}
	events, unsubscribe := h.Subscribe()
	defer unsubscribe()
	thread, err := c.CreateThread(a.ID, "Named")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case u := <-events:
		if !u.Metadata || u.ThreadID != thread.ID {
			t.Fatalf("metadata %+v", u)
		}
	case <-time.After(time.Second):
		t.Fatal("missing metadata")
	}
	if _, err = c.SendThread(thread.ID, "needle", nil, "queue"); err != nil {
		t.Fatal(err)
	}
	awaitIdle(t, func() bool { return h.Busy(a.ID) })
	messages, err := c.ThreadTranscript(thread.ID)
	if err != nil || len(messages) != 2 || messages[1].Text != "Echo: needle" {
		t.Fatalf("thread messages %+v %v", messages, err)
	}
	main, _ := c.Transcript(a.ID)
	if len(main) != 0 {
		t.Fatal("named thread polluted main")
	}
	threads, err := c.Threads(a.ID)
	if err != nil || len(threads) != 2 {
		t.Fatalf("threads %+v %v", threads, err)
	}
	if err = c.SaveMemory(thread.ID, store.Memory{ID: "note", Text: "needle note"}); err != nil {
		t.Fatal(err)
	}
	notes, err := c.Memories(thread.ID)
	if err != nil || len(notes) != 1 {
		t.Fatalf("notes %+v %v", notes, err)
	}
	if err = c.DeleteMemory(thread.ID, "note"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.CompactThread(thread.ID); err != nil {
		t.Fatal(err)
	}
	room := store.Room{ID: "team", Name: "Team", Members: []string{a.ID, b.ID}, LeadID: a.ID, ReplyMode: "lead", Instructions: "Team rules", WorkDir: t.TempDir()}
	if err = c.SaveRoom(room); err != nil {
		t.Fatal(err)
	}
	if _, err = c.SendRoom(room.ID, "@Bo room needle", nil); err != nil {
		t.Fatal(err)
	}
	awaitIdle(t, func() bool { return h.Busy(b.ID) || h.Busy(a.ID) })
	shared, err := c.RoomTranscript(room.ID)
	if err != nil || len(shared) != 2 || shared[1].BotID != b.ID || shared[1].ThreadID == "" {
		t.Fatalf("room messages %+v %v", shared, err)
	}
	results, err := c.Search("needle")
	if err != nil || len(results) != 4 {
		t.Fatalf("search %+v %v", results, err)
	}
	snapshots, err := c.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	foundThread, foundRoom := false, false
	for _, u := range snapshots {
		if u.ThreadID == thread.ID && len(u.Messages) == 2 {
			foundThread = true
		}
		if u.RoomID == room.ID && len(u.Messages) == 2 {
			foundRoom = true
		}
	}
	if !foundThread || !foundRoom {
		t.Fatalf("incomplete snapshots %+v", snapshots)
	}
	cloned, err := c.CloneBot(a.ID)
	if err != nil || cloned.ID == a.ID || cloned.Cursor != "" {
		t.Fatalf("clone %+v %v", cloned, err)
	}
	if err = c.CloseThread(thread.ID); err != nil {
		t.Fatal(err)
	}
	_, err = c.SendThread(thread.ID, "late", nil, "")
	statusIs(t, err, http.StatusBadRequest)
	if err = c.StopRoom(room.ID); err != nil {
		t.Fatal(err)
	}
	if err = c.DeleteRoom(room.ID); err != nil {
		t.Fatal(err)
	}
	rooms, err := c.Rooms()
	if err != nil || len(rooms) != 0 {
		t.Fatalf("rooms %+v %v", rooms, err)
	}
}

func TestPhase3HTTPValidationAndCredentialBoundary(t *testing.T) {
	_, c, _ := newTestService(t, nil)
	a := mustSave(t, c, "ada")
	_, err := c.CreateThread(a.ID, "bad\nname")
	statusIs(t, err, http.StatusBadRequest)
	_, err = c.ThreadTranscript("../escape")
	statusIs(t, err, http.StatusBadRequest)
	statusIs(t, c.SaveMemory(a.ID, store.Memory{ID: "m", Text: strings.Repeat("x", 8193)}), http.StatusBadRequest)
	_, err = c.Search(strings.Repeat("x", 513))
	statusIs(t, err, http.StatusBadRequest)
	statusIs(t, c.SaveRoom(store.Room{ID: "room", Name: "Bad", Members: []string{a.ID, "missing"}, LeadID: a.ID, ReplyMode: "lead"}), http.StatusBadRequest)
	statusIs(t, c.SaveRoom(store.Room{ID: "room", Name: "Bad", Members: []string{a.ID}, LeadID: a.ID, ReplyMode: "lead", WorkDir: "relative"}), http.StatusBadRequest)
	statusIs(t, c.SaveDecisionSettings(store.DecisionSettings{BaseURL: "https://user:secret@example.com", Model: "m"}), http.StatusBadRequest)
	statusIs(t, c.SaveDecisionSettings(store.DecisionSettings{APIKeyEnv: "secret\nvalue"}), http.StatusBadRequest)
	t.Setenv("BQBOT_SECRET_TEST", "do-not-return-this-secret")
	cfg := store.DecisionSettings{BaseURL: "http://127.0.0.1:1/v1", APIKeyEnv: "BQBOT_SECRET_TEST", Model: "offline"}
	if err = c.SaveDecisionSettings(cfg); err != nil {
		t.Fatal(err)
	}
	read, err := c.DecisionSettings()
	if err != nil || read != cfg {
		t.Fatalf("settings %+v %v", read, err)
	}
	a.Avatar = "/tmp/image"
	statusIs(t, c.SaveBot(a), http.StatusBadRequest)
}

func TestMemoryMetadataUsesActualScope(t *testing.T) {
	_, c, h := newTestService(t, nil)
	a := mustSave(t, c, "ada")
	events, cancel := h.Subscribe()
	defer cancel()
	if err := c.SaveMemory(a.ID, store.Memory{ID: "note", Text: "private"}); err != nil {
		t.Fatal(err)
	}
	select {
	case u := <-events:
		if !u.Metadata || u.ThreadID != a.ID || u.RoomID != "" || u.BotID != a.ID {
			t.Fatalf("private memory route %+v", u)
		}
	case <-time.After(time.Second):
		t.Fatal("no memory update")
	}
	room := store.Room{ID: "room", Name: "Team", Members: []string{a.ID}, LeadID: a.ID, ReplyMode: "lead"}
	if err := c.SaveRoom(room); err != nil {
		t.Fatal(err)
	}
	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("no room update")
	}
	if err := c.SaveMemory(room.ID, store.Memory{ID: "note", Text: "shared"}); err != nil {
		t.Fatal(err)
	}
	select {
	case u := <-events:
		if !u.Metadata || u.RoomID != room.ID || u.ThreadID != "" {
			t.Fatalf("room memory route %+v", u)
		}
	case <-time.After(time.Second):
		t.Fatal("no room memory update")
	}
}
