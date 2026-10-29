package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"bqbot/internal/driver"
	"bqbot/internal/mcp"
	"bqbot/internal/store"
)

func TestThreadContextMemoryCompactionIsolation(t *testing.T) {
	inputs := make(chan driver.TurnInput, 8)
	h := testHarness(t, &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		inputs <- in
		emit(driver.Event{Kind: driver.EvSession, Cursor: "session-" + in.Text})
		emit(driver.Event{Kind: driver.EvText, Text: "answer"})
		return nil
	}})
	b := savedBot(t, h)
	a, err := h.CreateThread(b.ID, "A")
	if err != nil {
		t.Fatal(err)
	}
	other, err := h.CreateThread(b.ID, "B")
	if err != nil {
		t.Fatal(err)
	}
	if err = h.Store.SaveMemory(a.ID, store.Memory{ID: "memory", Text: "project secret for A"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 16; i++ {
		if err = h.Store.AppendThread(a.ID, store.Message{ID: fmt.Sprintf("m%d", i), Role: "user", Text: fmt.Sprintf("history %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	compact, err := h.CompactThread(a.ID)
	if err != nil || compact.Summary == "" || compact.SummaryThrough == "" {
		t.Fatalf("compact %+v %v", compact, err)
	}
	h.SendThread(a.ID, "project", nil, "")
	first := receive(t, inputs)
	eventually(t, func() bool { return !h.Busy(b.ID) })
	if first.Cursor != "" || len(first.History) != 13 || !strings.Contains(first.History[0].Text, compact.Summary) || !strings.Contains(first.System, "project secret for A") {
		t.Fatalf("context %+v", first)
	}
	h.SendThread(other.ID, "other", nil, "")
	second := receive(t, inputs)
	eventually(t, func() bool { return !h.Busy(b.ID) })
	if second.Cursor != "" || len(second.History) != 0 || strings.Contains(second.System, "secret") {
		t.Fatalf("thread context leak %+v", second)
	}
	h.SendThread(a.ID, "again", nil, "")
	third := receive(t, inputs)
	eventually(t, func() bool { return !h.Busy(b.ID) })
	if third.Cursor != "session-project" {
		t.Fatalf("thread session lost: %q", third.Cursor)
	}
	main, _ := h.Store.Transcript(b.ID)
	if len(main) != 0 {
		t.Fatal("named thread leaked into main")
	}
	all, err := h.FullSnapshot()
	if err != nil || len(all) != 3 {
		t.Fatalf("snapshot %+v %v", all, err)
	}
	if err = h.CloseThread(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = h.SendThread(a.ID, "closed", nil, ""); err == nil {
		t.Fatal("closed thread accepted send")
	}
}

func TestRoomSharedContextMentionsAndIsolation(t *testing.T) {
	inputs := make(chan driver.TurnInput, 8)
	h := testHarness(t, &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		inputs <- in
		emit(driver.Event{Kind: driver.EvSession, Cursor: "room-session"})
		emit(driver.Event{Kind: driver.EvText, Text: "room reply"})
		return nil
	}})
	a := savedBot(t, h)
	a.Name = "Ada"
	h.Store.SaveBot(a)
	b := store.Bot{ID: "bo", Name: "Bo", Engine: "test"}
	h.Store.SaveBot(b)
	room := store.Room{ID: "room", Name: "Team", Instructions: "shared rules", WorkDir: t.TempDir(), Members: []string{a.ID, b.ID}, LeadID: a.ID, ReplyMode: "lead"}
	if err := h.SaveRoom(room); err != nil {
		t.Fatal(err)
	}
	h.Store.SaveMemory(room.ID, store.Memory{ID: "memory", Text: "channel memory"})
	if _, err := h.SendRoom(room.ID, "hello", nil); err != nil {
		t.Fatal(err)
	}
	first := receive(t, inputs)
	waitJobs(t, h)
	if !strings.Contains(first.System, "shared rules") || !strings.Contains(first.System, "channel memory") || first.Dir != room.WorkDir {
		t.Fatalf("channel context %+v", first)
	}
	if _, err := h.SendRoom(room.ID, "@Bo follow up", nil); err != nil {
		t.Fatal(err)
	}
	second := receive(t, inputs)
	waitJobs(t, h)
	if !strings.Contains(second.System, "You are Bo,") || second.Cursor != "" || len(second.History) != 2 || !strings.Contains(second.History[1].Text, "Ada: room reply") {
		t.Fatalf("shared history %+v", second)
	}
	if _, err := h.SendRoom(room.ID, "@Ada @Bo both", nil); err != nil {
		t.Fatal(err)
	}
	receive(t, inputs)
	receive(t, inputs)
	waitJobs(t, h)
	transcript, _ := h.Store.RoomTranscript(room.ID)
	if len(transcript) != 7 {
		t.Fatalf("duplicate shared entries: %+v", transcript)
	}
	for _, m := range transcript {
		if m.Role == "assistant" && (m.BotID == "" || m.ThreadID == "" || m.From == "") {
			t.Fatalf("missing speaker %+v", m)
		}
	}
	for _, id := range []string{a.ID, b.ID} {
		main, _ := h.Store.Transcript(id)
		if len(main) != 0 {
			t.Fatal("room leaked into main")
		}
	}
	if got := roomMentions(room, []store.Bot{a, b}, "email@Bo @Bob"); len(got) != 0 {
		t.Fatalf("partial mention: %v", got)
	}
	snapshots, err := h.FullSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, u := range snapshots {
		if u.RoomID == room.ID && u.Room != nil && len(u.Messages) == 7 {
			found = true
		}
	}
	if !found {
		t.Fatal("room missing from snapshot")
	}
}

func TestAutoRouterLocalProviderAndFallback(t *testing.T) {
	var answer atomic.Value
	answer.Store(`{"bot_id":"bo"}`)
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer local-test-key" {
			t.Error("invalid decision request")
		}
		var req struct {
			Model    string
			Messages []map[string]string
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Model != "local-model" {
			t.Error("invalid model body")
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": answer.Load().(string)}}}})
	}))
	defer srv.Close()
	t.Setenv("BQBOT_TEST_DECISION_KEY", "local-test-key")
	h := testHarness(t, &driver.Echo{})
	a := store.Bot{ID: "ada", Name: "Ada", Engine: "echo"}
	b := store.Bot{ID: "bo", Name: "Bo", Engine: "echo"}
	h.Store.SaveBot(a)
	h.Store.SaveBot(b)
	room := store.Room{ID: "room", Name: "Auto", Members: []string{a.ID, b.ID}, LeadID: a.ID, ReplyMode: "auto"}
	h.SaveRoom(room)
	h.Store.SaveDecisionSettings(store.DecisionSettings{BaseURL: srv.URL, APIKeyEnv: "BQBOT_TEST_DECISION_KEY", Model: "local-model"})
	for _, tc := range []struct{ response, text, want string }{{`{"bot_id":"bo"}`, "choose", b.ID}, {`{}`, "uncertain", a.ID}, {`{"bot_id":"stranger"}`, "unknown", a.ID}, {`not json`, "malformed", a.ID}, {`{"bot_id":"bo"}`, "@Ada mention", a.ID}} {
		answer.Store(tc.response)
		if _, err := h.SendRoom(room.ID, tc.text, nil); err != nil {
			t.Fatal(err)
		}
		waitJobs(t, h)
		msgs, _ := h.Store.RoomTranscript(room.ID)
		if msgs[len(msgs)-1].BotID != tc.want {
			t.Fatalf("route %s: %+v", tc.text, msgs[len(msgs)-1])
		}
	}
	if count.Load() != 4 {
		t.Fatalf("mentions invoked decision provider: %d", count.Load())
	}
	t.Setenv("BQBOT_TEST_DECISION_KEY", "")
	if got := h.routeRoom(room, []store.Bot{a, b}, "no key"); got != a.ID {
		t.Fatal(got)
	}
	t.Setenv("BQBOT_TEST_DECISION_KEY", "local-test-key")
	srv.Close()
	if got := h.routeRoom(room, []store.Bot{a, b}, "offline"); got != a.ID {
		t.Fatal(got)
	}
}

func TestAsyncReceiptBeforeTargetAndWakeOriginalThread(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	target := &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		started <- struct{}{}
		select {
		case <-release:
			emit(driver.Event{Kind: driver.EvText, Text: "target answer"})
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	r := newRig(t, &asker{}, target)
	a := r.bot("Ada", "asker", "")
	b := r.bot("Bo", "test", "")
	thread, err := r.h.CreateThread(a.ID, "Original")
	if err != nil {
		t.Fatal(err)
	}
	r.h.SendThread(thread.ID, "ask Bo question", nil, "")
	receipt := r.wait(a.ID)
	if receipt.ThreadID != thread.ID || !strings.Contains(receipt.Reply.Text, `"status":"accepted"`) {
		t.Fatalf("receipt %+v", receipt)
	}
	receive(t, started)
	if !r.h.Busy(b.ID) {
		t.Fatal("tool waited for completion")
	}
	close(release)
	waitJobs(t, r.h)
	transcript, _ := r.st.ThreadTranscript(thread.ID)
	if len(transcript) != 4 || !strings.Contains(transcript[3].Text, "target answer") {
		t.Fatalf("wake %+v", transcript)
	}
	main, _ := r.st.Transcript(a.ID)
	if len(main) != 0 {
		t.Fatal("woke main instead of original")
	}
}

func TestAsyncStopCloseDeleteSuppressWake(t *testing.T) {
	for _, action := range []string{"stop", "close", "delete-asker", "delete-target", "close-target", "close-harness"} {
		t.Run(action, func(t *testing.T) {
			started := make(chan struct{}, 1)
			r := newRig(t, &asker{}, &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
				started <- struct{}{}
				<-ctx.Done()
				return ctx.Err()
			}})
			a := r.bot("Ada", "asker", "")
			b := r.bot("Bo", "test", "")
			thread, _ := r.h.CreateThread(a.ID, "Original")
			r.h.SendThread(thread.ID, "ask Bo question", nil, "")
			r.wait(a.ID)
			receive(t, started)
			var err error
			switch action {
			case "stop":
				r.h.Stop(a.ID)
			case "close":
				err = r.h.CloseThread(thread.ID)
			case "delete-asker":
				err = r.h.DeleteBot(a.ID)
			case "delete-target":
				err = r.h.DeleteBot(b.ID)
			case "close-target":
				target, _ := r.st.HandoffThread(b.ID, a.ID)
				err = r.h.CloseThread(target.ID)
			case "close-harness":
				err = r.h.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			waitJobs(t, r.h)
			if action != "delete-asker" {
				msgs, e := r.st.ThreadTranscript(thread.ID)
				if e != nil || len(msgs) != 2 {
					t.Fatalf("unexpected wake: %+v %v", msgs, e)
				}
			}
		})
	}
}

func TestThreadDiscoveryGrantScope(t *testing.T) {
	grants := make(chan mcp.Grant, 1)
	release := make(chan struct{})
	d := &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		data, e := os.ReadFile(in.MCP.Args[1])
		if e != nil {
			return e
		}
		var g mcp.Grant
		if e = json.Unmarshal(data, &g); e != nil {
			return e
		}
		grants <- g
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	r := newRig(t, d)
	a := r.bot("Ada", "test", "")
	b := r.bot("Bo", "test", "")
	own, _ := r.h.CreateThread(a.ID, "own")
	other, _ := r.h.CreateThread(b.ID, "private")
	delegated, _ := r.st.HandoffThread(b.ID, a.ID)
	r.h.Send(a, "discover")
	g := receive(t, grants)
	client := &mcp.HTTPBackend{URL: g.URL, Token: g.Token}
	listed, err := client.ListThreads(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listed, own.ID) || !strings.Contains(listed, delegated.ID) || strings.Contains(listed, other.ID) {
		t.Fatalf("scope %s", listed)
	}
	if _, err = client.CloseThread(context.Background(), other.ID); err == nil {
		t.Fatal("closed another bot's private thread")
	}
	if _, err = client.CloseThread(context.Background(), a.ID); err == nil {
		t.Fatal("closed caller")
	}
	if _, err = client.CloseThread(context.Background(), own.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	r.wait(a.ID)
	if _, err = client.ListThreads(context.Background()); err == nil {
		t.Fatal("revoked grant still works")
	}
}

func TestCloseThreadRacesWithSendAndSnapshot(t *testing.T) {
	h := testHarness(t, &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		<-ctx.Done()
		return ctx.Err()
	}})
	b := savedBot(t, h)
	thread, _ := h.CreateThread(b.ID, "race")
	h.SendThread(thread.ID, "first", nil, "")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 4; j++ {
				h.SendThread(thread.ID, "queued", nil, "")
				h.FullSnapshot()
			}
		}()
	}
	if err := h.CloseThread(thread.ID); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if h.Busy(b.ID) {
		t.Fatal("closed thread still running")
	}
	if _, err := h.SendThread(thread.ID, "late", nil, ""); err == nil {
		t.Fatal("closed thread accepted work")
	}
}

func TestAsyncFailureWakesOnce(t *testing.T) {
	r := newRig(t, &asker{}, &testDriver{run: func(context.Context, driver.TurnInput, func(driver.Event)) error { return errors.New("local failure") }})
	a := r.bot("Ada", "asker", "")
	r.bot("Bo", "test", "")
	r.h.Send(a, "ask Bo fail")
	waitJobs(t, r.h)
	msgs, _ := r.st.Transcript(a.ID)
	if len(msgs) != 4 || !strings.Contains(msgs[3].Text, "local failure") {
		t.Fatalf("failure wake %+v", msgs)
	}
}

func TestCloseAwaitsCanceledDelegation(t *testing.T) {
	for _, action := range []string{"close-thread", "delete-bot", "delete-room"} {
		t.Run(action, func(t *testing.T) {
			started := make(chan struct{}, 1)
			canceled := make(chan struct{})
			release := make(chan struct{})
			r := newRig(t, &asker{}, &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
				started <- struct{}{}
				<-ctx.Done()
				close(canceled)
				<-release
				return ctx.Err()
			}})
			a := r.bot("Ada", "asker", "")
			b := r.bot("Bo", "test", "")
			thread, _ := r.h.CreateThread(a.ID, "origin")
			room := store.Room{ID: "room", Name: "Team", Members: []string{a.ID, b.ID}, LeadID: a.ID, ReplyMode: "lead"}
			if action == "delete-room" {
				if err := r.h.SaveRoom(room); err != nil {
					t.Fatal(err)
				}
				r.h.SendRoom(room.ID, "ask Bo question", nil)
			} else {
				r.h.SendThread(thread.ID, "ask Bo question", nil, "")
			}
			r.wait(a.ID)
			receive(t, started)
			finished := make(chan error, 1)
			go func() {
				switch action {
				case "close-thread":
					finished <- r.h.CloseThread(thread.ID)
				case "delete-bot":
					finished <- r.h.DeleteBot(a.ID)
				case "delete-room":
					finished <- r.h.DeleteRoom(room.ID)
				}
			}()
			receive(t, canceled)
			select {
			case err := <-finished:
				close(release)
				t.Fatalf("returned before target exited: %v", err)
			default:
			}
			close(release)
			if err := receive(t, finished); err != nil {
				t.Fatal(err)
			}
			waitJobs(t, r.h)
		})
	}
}

func TestRoomMemberRemovalCancelsQueuedAndRunningWork(t *testing.T) {
	starts := make(chan string, 4)
	h := testHarness(t, &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		starts <- in.Text
		<-ctx.Done()
		return ctx.Err()
	}})
	a := savedBot(t, h)
	a.Name = "Ada"
	h.Store.SaveBot(a)
	b := store.Bot{ID: "bo", Name: "Bo", Engine: "test"}
	h.Store.SaveBot(b)
	room := store.Room{ID: "room", Name: "Team", Members: []string{a.ID, b.ID}, LeadID: a.ID, ReplyMode: "lead"}
	h.SaveRoom(room)
	h.Send(b, "private block")
	receive(t, starts)
	h.SendRoom(room.ID, "@Bo queued", nil)
	h.mu.Lock()
	queued := len(h.queues[b.ID])
	h.mu.Unlock()
	if queued != 1 {
		t.Fatal("room task not queued")
	}
	room.Members = []string{a.ID}
	if err := h.SaveRoom(room); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	queued = len(h.queues[b.ID])
	h.mu.Unlock()
	if queued != 0 {
		t.Fatal("removed member still queued")
	}
	h.Stop(b.ID)
	eventually(t, func() bool { return !h.Busy(b.ID) })
	select {
	case text := <-starts:
		t.Fatalf("removed member ran: %q", text)
	default:
	}
	room.Members = append(room.Members, b.ID)
	h.SaveRoom(room)
	h.SendRoom(room.ID, "@Bo active", nil)
	receive(t, starts)
	room.Members = []string{a.ID}
	if err := h.SaveRoom(room); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return !h.Busy(b.ID) })
}

func TestRoomGrantScope(t *testing.T) {
	grants := make(chan mcp.Grant, 1)
	release := make(chan struct{})
	r := newRig(t, &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		data, _ := os.ReadFile(in.MCP.Args[1])
		var g mcp.Grant
		json.Unmarshal(data, &g)
		grants <- g
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}, &driver.Echo{})
	a := r.bot("Ada", "test", "")
	b := r.bot("Bo", "echo", "")
	outside := r.bot("Outside", "echo", "")
	room := store.Room{ID: "room", Name: "Team", Members: []string{a.ID, b.ID}, LeadID: a.ID, ReplyMode: "lead"}
	r.h.SaveRoom(room)
	r.h.SendRoom(room.ID, "discover", nil)
	g := receive(t, grants)
	client := &mcp.HTTPBackend{URL: g.URL, Token: g.Token}
	targets, err := client.ListRoomTargets(context.Background())
	if err != nil || !strings.Contains(targets, b.ID) || strings.Contains(targets, outside.ID) {
		t.Fatalf("room targets %s %v", targets, err)
	}
	if _, err = client.AskBot(context.Background(), "Outside", "question"); err == nil {
		t.Fatal("room grant delegated outside room")
	}
	threads, err := client.ListThreads(context.Background())
	if err != nil || strings.Contains(threads, `"id":"`+a.ID+`"`) {
		t.Fatalf("room grant exposed private main %s %v", threads, err)
	}
	close(release)
	waitJobs(t, r.h)
}

func TestStopRoomCancelsInFlightAutoRouting(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	t.Setenv("BQBOT_TEST_DECISION_KEY", "local")
	h := testHarness(t, &driver.Echo{})
	b := store.Bot{ID: "ada", Name: "Ada", Engine: "echo"}
	h.Store.SaveBot(b)
	room := store.Room{ID: "room", Name: "Team", Members: []string{b.ID}, LeadID: b.ID, ReplyMode: "auto"}
	h.SaveRoom(room)
	h.Store.SaveDecisionSettings(store.DecisionSettings{BaseURL: srv.URL, APIKeyEnv: "BQBOT_TEST_DECISION_KEY", Model: "local"})
	result := make(chan error, 1)
	go func() { _, err := h.SendRoom(room.ID, "route", nil); result <- err }()
	receive(t, started)
	h.StopThread(room.ID)
	if err := receive(t, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped router: %v", err)
	}
	messages, _ := h.Store.RoomTranscript(room.ID)
	if len(messages) != 0 || h.Busy(b.ID) {
		t.Fatal("stopped router admitted work")
	}
}

func TestRoomHandoffUsesSharedContextAndWakesOnce(t *testing.T) {
	inputs := make(chan driver.TurnInput, 1)
	r := newRig(t, &asker{}, &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		inputs <- in
		emit(driver.Event{Kind: driver.EvText, Text: "channel answer"})
		return nil
	}})
	a := r.bot("Ada", "asker", "")
	b := r.bot("Bo", "test", "")
	room := store.Room{ID: "room", Name: "Team", Instructions: "channel instruction", WorkDir: t.TempDir(), Members: []string{a.ID, b.ID}, LeadID: a.ID, ReplyMode: "lead"}
	r.h.SaveRoom(room)
	if _, err := r.h.SendRoom(room.ID, "ask Bo review", nil); err != nil {
		t.Fatal(err)
	}
	input := receive(t, inputs)
	waitJobs(t, r.h)
	if input.Dir != room.WorkDir || !strings.Contains(input.System, room.Instructions) || input.MCP != nil {
		t.Fatalf("handoff context %+v", input)
	}
	messages, err := r.st.RoomTranscript(room.ID)
	if err != nil || len(messages) != 6 || !strings.Contains(messages[len(messages)-1].Text, "channel answer") {
		t.Fatalf("room handoff %+v %v", messages, err)
	}
	main, _ := r.st.Transcript(a.ID)
	if len(main) != 0 {
		t.Fatal("room wake leaked into private conversation")
	}
}
