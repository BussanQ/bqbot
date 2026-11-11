package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"bqbot/internal/driver"
	"bqbot/internal/store"
)

func TestRoomDiscussionPeerRepliesAndSilentPass(t *testing.T) {
	// Exercise the same streamed chat-completions path used by configured API bots.
	type message struct{ Role, Content string }
	type request struct {
		Model    string
		Messages []message
	}
	requests := make(chan request, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			http.Error(w, "bad request", 400)
			return
		}
		requests <- req
		chunks := []string{" [[", "PASS", "]] "}
		has := func(content string) bool {
			return slices.Contains(req.Messages, message{"user", content})
		}
		switch {
		case req.Model == "bo" && !has("Ada: Friday works."):
			chunks = []string{"Ada, does Friday work?"}
		case req.Model == "ada" && has("Bo: Ada, does Friday work?"):
			chunks = []string{"Friday works."}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range chunks {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", chunk)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv("OPENAI_API_KEY", "")
	h := testHarness(t, &driver.OpenAI{})
	if err := h.Store.SaveSettings(map[string]driver.EngineSettings{"openai": {BaseURL: server.URL}}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Ada", "Bo", "Cy"} {
		id := strings.ToLower(name)
		if err := h.Store.SaveBot(store.Bot{ID: id, Name: name, Engine: "openai", Model: id}); err != nil {
			t.Fatal(err)
		}
	}
	room := store.Room{ID: "room", Name: "Team", Members: []string{"cy", "bo", "ada"}, LeadID: "bo", ReplyMode: "lead"}
	if err := h.SaveRoom(room); err != nil {
		t.Fatal(err)
	}
	events, unsubscribe := h.Subscribe()
	defer unsubscribe()
	if _, err := h.SendRoom(room.ID, "plan the release", nil); err != nil {
		t.Fatal(err)
	}
	waitJobs(t, h)
	transcript, err := h.Store.RoomTranscript(room.ID)
	if err != nil || len(transcript) != 3 || transcript[1].From != "Bo" || transcript[2].From != "Ada" {
		t.Fatalf("shared transcript %+v, %v", transcript, err)
	}
	// Bo spoke first, Cy passed, Ada answered Bo. Both previous readers
	// get Ada's answer once, and the two passes end the round.
	if got := len(requests); got != 5 {
		t.Fatalf("got %d model calls", got)
	}
	for i, id := range []string{"bo", "cy", "ada", "bo", "cy"} {
		req := <-requests
		if req.Model != id || !strings.Contains(req.Messages[0].Content, roomPass) {
			t.Fatalf("model request %d: %+v", i, req)
		}
		if i >= 3 && !slices.Contains(req.Messages, message{"user", "Ada: Friday works."}) {
			t.Fatalf("earlier member missed later reply: %+v", req)
		}
	}
	users, finished := 0, 0
	for finished < 5 {
		u := receive(t, events)
		if u.User != nil {
			users++
		}
		if u.Done {
			finished++
		}
		if u.Reply != nil && (u.Reply.Text == "" || strings.Contains(u.Reply.Text, "[[")) || u.Event != nil && strings.Contains(u.Event.Text, "[[") {
			t.Fatalf("silent decision leaked: %+v", u)
		}
	}
	if users != 1 {
		t.Fatalf("%d shared user events", users)
	}
}

func TestRoomQueuedMessageIsolationAndStop(t *testing.T) {
	inputs := make(chan driver.TurnInput, 8)
	output := make(chan string)
	h := testHarness(t, &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		inputs <- in
		select {
		case <-ctx.Done():
			return ctx.Err()
		case reply := <-output:
			emit(driver.Event{Kind: driver.EvText, Text: reply})
			return nil
		}
	}})
	for _, id := range []string{"ada", "bo"} {
		if err := h.Store.SaveBot(store.Bot{ID: id, Name: id, Engine: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	room := store.Room{ID: "studio", Name: "Studio", LeadID: "ada", Members: []string{"ada", "bo"}}
	if err := h.SaveRoom(room); err != nil {
		t.Fatal(err)
	}
	if _, err := h.SendRoom(room.ID, "first", nil); err != nil {
		t.Fatal(err)
	}
	receive(t, inputs)
	if _, err := h.SendRoom(room.ID, "second", nil); err != nil {
		t.Fatal(err)
	}
	output <- "Friday"
	peer := receive(t, inputs)
	if !peer.RoomReview || len(peer.History) != 2 || peer.History[0].Text != "first" || peer.History[1].Text != "ada: Friday" {
		t.Fatalf("mixed overlapping submissions: %+v", peer)
	}
	output <- "agreed"
	next := receive(t, inputs)
	if next.Text != "second" || len(next.History) != 3 || next.History[2].Text != "bo: agreed" {
		t.Fatalf("next round lost the peer's answer: %+v", next)
	}
	h.StopThread(room.ID)
	waitJobs(t, h)
	if len(inputs) != 0 {
		t.Fatal("a waiting member ran after stop")
	}
	snapshots, err := h.FullSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range snapshots {
		if u.RoomID == room.ID && (u.Busy || u.Queued != 0 || u.Reply != nil) {
			t.Fatalf("room still active after stop: %+v", u)
		}
	}
}
