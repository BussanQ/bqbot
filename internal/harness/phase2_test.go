package harness

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"bqbot/internal/driver"
	"bqbot/internal/store"
)

type testDriver struct {
	run   func(context.Context, driver.TurnInput, func(driver.Event)) error
	steer bool
}

func (*testDriver) Name() string             { return "test" }
func (*testDriver) Binary() string           { return "" }
func (d *testDriver) SupportsSteering() bool { return d.steer }
func (d *testDriver) Run(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
	return d.run(ctx, in, emit)
}
func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
		var zero T
		return zero
	}
}
func eventually(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for !f() {
		select {
		case <-deadline:
			t.Fatal("timed out")
		case <-time.After(time.Millisecond):
		}
	}
}
func savedBot(t *testing.T, h *Harness) store.Bot {
	t.Helper()
	b := store.Bot{ID: store.NewID(), Name: "Test", Engine: "test"}
	if err := h.Store.SaveBot(b); err != nil {
		t.Fatal(err)
	}
	return b
}
func testHarness(t *testing.T, d driver.Driver) *Harness {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := New(st, d)
	t.Cleanup(func() { h.Close() })
	return h
}
func doneUpdate(t *testing.T, ch <-chan Update, id string) Update {
	t.Helper()
	for {
		u := receive(t, ch)
		if u.BotID == id && u.Done {
			return u
		}
	}
}

func TestFIFOAndSteerFallback(t *testing.T) {
	started := make(chan string, 4)
	release := make(chan struct{}, 4)
	h := testHarness(t, &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		started <- in.Text
		select {
		case <-release:
			emit(driver.Event{Kind: driver.EvText, Text: in.Text})
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	b := savedBot(t, h)
	updates, unsub := h.Subscribe()
	defer unsub()
	h.Send(b, "first")
	if receive(t, started) != "first" {
		t.Fatal("first")
	}
	h.Submit(b, "second", nil, "steer")
	h.Send(b, "third")
	if s := h.Snapshot(); len(s) != 1 || s[0].Queued != 2 {
		t.Fatalf("snapshot %+v", s)
	}
	if msgs, _ := h.Store.Transcript(b.ID); len(msgs) != 1 {
		t.Fatalf("pending work entered transcript: %+v", msgs)
	}
	for _, want := range []string{"second", "third"} {
		release <- struct{}{}
		if got := receive(t, started); got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	}
	release <- struct{}{}
	for i := 0; i < 3; i++ {
		doneUpdate(t, updates, b.ID)
	}
	msgs, _ := h.Store.Transcript(b.ID)
	var texts []string
	for _, m := range msgs {
		if m.Role == "user" {
			texts = append(texts, m.Text)
		}
	}
	if !reflect.DeepEqual(texts, []string{"first", "second", "third"}) {
		t.Fatal(texts)
	}
}

func TestSteerRunsInCurrentTurn(t *testing.T) {
	started := make(chan struct{})
	seen := make(chan string, 1)
	h := testHarness(t, &testDriver{steer: true, run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		close(started)
		select {
		case msg := <-in.Steer:
			seen <- msg
			emit(driver.Event{Kind: driver.EvText, Text: msg})
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	b := savedBot(t, h)
	updates, unsub := h.Subscribe()
	defer unsub()
	h.Send(b, "first")
	receive(t, started)
	if _, err := h.Submit(b, "correction", nil, "steer"); err != nil {
		t.Fatal(err)
	}
	if receive(t, seen) != "correction" {
		t.Fatal("steer lost")
	}
	doneUpdate(t, updates, b.ID)
	msgs, _ := h.Store.Transcript(b.ID)
	if len(msgs) != 3 || msgs[1].Text != "correction" {
		t.Fatal(msgs)
	}
}

func TestApprovalsSnapshotStopAndBroadcast(t *testing.T) {
	h := testHarness(t, &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		_, err := in.Approve(ctx, driver.ApprovalRequest{ID: "question", Kind: "question", Question: "Choose?", Options: []string{"A", "B"}})
		return err
	}})
	b := savedBot(t, h)
	b.ApprovalMode = "full-auto"
	h.Store.SaveBot(b)
	a, ua := h.Subscribe()
	defer ua()
	slow, us := h.Subscribe()
	defer us()
	h.Send(b, "start")
	for {
		if u := receive(t, a); u.Approval != nil {
			u.Approval.Options[0] = "mutated"
			break
		}
	}
	snap := h.Snapshot()
	if len(snap) != 2 || snap[1].Approval.Options[0] != "A" {
		t.Fatalf("snapshot %+v", snap)
	}
	if err := h.Approve(b.ID, "question", driver.ApprovalDecision{Outcome: "allow"}); err == nil {
		t.Fatal("question automatically allowed")
	}
	h.Send(b, "queued")
	h.Stop(b.ID)
	u := doneUpdate(t, a, b.ID)
	if u.Err != nil {
		t.Fatal(u.Err)
	}
	if err := h.Approve(b.ID, "question", driver.ApprovalDecision{Outcome: "answer", Answer: "A"}); err == nil {
		t.Fatal("stale approval accepted")
	}
	if len(h.Snapshot()) != 0 {
		t.Fatal("pending state survived stop")
	}
	for {
		u := receive(t, slow)
		if u.Approval != nil {
			if u.Approval.Options[0] != "A" {
				t.Fatal("subscriber alias")
			}
			break
		}
	}
	msgs, _ := h.Store.Transcript(b.ID)
	if len(msgs) != 2 {
		t.Fatalf("queued message survived stop: %+v", msgs)
	}
}

func TestApprovalPoliciesAndDurability(t *testing.T) {
	for _, tc := range []struct {
		mode, kind, tool string
		auto             bool
	}{
		{"ask", "tool", "Edit", false}, {"auto-edit", "tool", "Edit", true}, {"auto-edit", "tool", "Bash", false},
		{"auto-edit", "tool", "mcp__evil__Edit", false}, {"full-auto", "tool", "Bash", true}, {"full-auto", "question", "", false},
	} {
		t.Run(tc.mode+tc.kind+tc.tool, func(t *testing.T) {
			result := make(chan driver.ApprovalDecision, 1)
			h := testHarness(t, &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
				d, err := in.Approve(ctx, driver.ApprovalRequest{ID: "request", Kind: tc.kind, Tool: tc.tool})
				result <- d
				return err
			}})
			b := savedBot(t, h)
			b.ApprovalMode = tc.mode
			h.Store.SaveBot(b)
			updates, unsub := h.Subscribe()
			defer unsub()
			h.Send(b, "go")
			for {
				u := receive(t, updates)
				if u.Approval != nil {
					break
				}
			}
			if !tc.auto {
				decision := driver.ApprovalDecision{Outcome: "deny"}
				if tc.kind == "question" {
					decision = driver.ApprovalDecision{Outcome: "answer", Answer: "yes"}
				}
				if err := h.Approve(b.ID, "request", decision); err != nil {
					t.Fatal(err)
				}
			}
			got := receive(t, result)
			if tc.auto && got.Outcome != "allow" {
				t.Fatal(got)
			}
			doneUpdate(t, updates, b.ID)
			events, err := h.Store.Events(b.ID, "")
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, e := range events {
				var u Update
				json.Unmarshal(e.Data, &u)
				if u.ResolvedApproval == "request" && u.Decision != nil {
					found = true
				}
			}
			if !found {
				t.Fatal("approval decision was not durable")
			}
		})
	}
}

func TestExpiredReplayAndLatestCursor(t *testing.T) {
	inputs := make(chan driver.TurnInput, 4)
	calls := 0
	h := testHarness(t, &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		inputs <- in
		calls++
		if calls == 1 {
			return driver.ErrSessionExpired
		}
		emit(driver.Event{Kind: driver.EvSession, Cursor: "fresh"})
		emit(driver.Event{Kind: driver.EvText, Text: "ok"})
		return nil
	}})
	b := savedBot(t, h)
	b.Cursor = "expired"
	b.Effort = "high"
	h.Store.SaveBot(b)
	// Effort edit invalidates the old session, then explicitly seed an expired one.
	b, _ = h.Bot(b.ID)
	b.Cursor = "expired"
	h.Store.SaveBot(b)
	h.Store.SaveSettings(map[string]driver.EngineSettings{"test": {Path: "/custom/cli"}})
	h.Store.Append(b.ID, store.Message{Role: "user", Text: "prior"})
	h.Store.Append(b.ID, store.Message{Role: "assistant", Text: "old answer"})
	updates, unsub := h.Subscribe()
	defer unsub()
	stale := b
	stale.Cursor = "stale"
	h.Send(stale, "current")
	first, second := receive(t, inputs), receive(t, inputs)
	if first.Cursor != "expired" || second.Cursor != "" || second.Effort != "high" || second.Settings.Path != "/custom/cli" {
		t.Fatalf("inputs %+v %+v", first, second)
	}
	if len(second.History) != 2 || second.History[0].Text != "prior" || second.Text != "current" {
		t.Fatalf("replay %+v", second)
	}
	doneUpdate(t, updates, b.ID)
	latest, _ := h.Bot(b.ID)
	if latest.Cursor != "fresh" {
		t.Fatal(latest)
	}
}

func TestExpiredRetryLimits(t *testing.T) {
	for _, output := range []bool{false, true} {
		t.Run(map[bool]string{false: "once", true: "no_retry_after_output"}[output], func(t *testing.T) {
			calls := 0
			h := testHarness(t, &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
				calls++
				if output {
					emit(driver.Event{Kind: driver.EvText, Text: "partial"})
				}
				return driver.ErrSessionExpired
			}})
			b := savedBot(t, h)
			b.Cursor = "expired"
			h.Store.SaveBot(b)
			updates, u := h.Subscribe()
			defer u()
			h.Send(b, "go")
			doneUpdate(t, updates, b.ID)
			want := 2
			if output {
				want = 1
			}
			if calls != want {
				t.Fatalf("calls %d want %d", calls, want)
			}
		})
	}
}

func TestImagesValidationAndInput(t *testing.T) {
	var imageBytes bytes.Buffer
	png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	valid := driver.Attachment{Name: "one.png", MIME: "image/png", Data: base64.StdEncoding.EncodeToString(imageBytes.Bytes())}
	received := make(chan driver.TurnInput, 1)
	h := testHarness(t, &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		received <- in
		return nil
	}})
	b := savedBot(t, h)
	for _, bad := range []driver.Attachment{{MIME: "image/png", Data: "bad base64"}, {MIME: "image/jpeg", Data: valid.Data}, {MIME: "image/svg+xml", Data: base64.StdEncoding.EncodeToString([]byte("<svg/>"))}} {
		if _, err := h.Submit(b, "", []driver.Attachment{bad}, "queue"); err == nil {
			t.Fatal("invalid image accepted")
		}
	}
	updates, u := h.Subscribe()
	defer u()
	if _, err := h.Submit(b, "", []driver.Attachment{valid}, "queue"); err != nil {
		t.Fatal(err)
	}
	if in := receive(t, received); len(in.Attachments) != 1 || in.Attachments[0] != valid {
		t.Fatal(in)
	}
	doneUpdate(t, updates, b.ID)
	msgs, _ := h.Store.Transcript(b.ID)
	if len(msgs) != 1 || len(msgs[0].Attachments) != 1 {
		t.Fatal(msgs)
	}
}

func TestDeleteWaitsAndPreventsResurrection(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	h := testHarness(t, &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return ctx.Err()
	}})
	b := savedBot(t, h)
	h.Send(b, "active")
	receive(t, started)
	h.Send(b, "pending")
	deleted := make(chan error, 1)
	go func() { deleted <- h.DeleteBot(b.ID) }()
	receive(t, canceled)
	select {
	case <-deleted:
		t.Fatal("delete did not await running driver")
	default:
	}
	if _, err := h.Send(b, "late"); err == nil {
		t.Fatal("admitted while deleting")
	}
	close(release)
	if err := receive(t, deleted); err != nil {
		t.Fatal(err)
	}
	msgs, _ := h.Store.Transcript(b.ID)
	events, _ := h.Store.Events(b.ID, "")
	if len(msgs) != 0 || len(events) != 0 {
		t.Fatal("data recreated after delete")
	}
	if _, err := h.Bot(b.ID); err == nil {
		t.Fatal("bot survived")
	}
}

func TestPersistenceFailureSurfaced(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(dir)
	h := New(st, &driver.Echo{})
	defer h.Close()
	b := store.Bot{ID: store.NewID(), Engine: "echo"}
	st.SaveBot(b)
	if err := os.WriteFile(filepath.Join(dir, "events"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	updates, u := h.Subscribe()
	defer u()
	if _, err := h.Send(b, "go"); err == nil {
		t.Fatal("persistence error hidden")
	}
	update := receive(t, updates)
	if update.Error == "" || update.Err == nil {
		t.Fatal(update)
	}
	if h.Busy(b.ID) {
		t.Fatal("failed admission left running turn")
	}
}

func TestApprovalHTTPGrantsAllDepths(t *testing.T) {
	var mu sync.Mutex
	var modes []string
	d := &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		if in.ApprovalMCP == nil {
			return errors.New("missing approval grant")
		}
		b, err := os.ReadFile(in.ApprovalMCP.Args[1])
		if err != nil {
			return err
		}
		var grant struct {
			URL, Token   string
			ApprovalOnly bool `json:"approval_only"`
		}
		if err = json.Unmarshal(b, &grant); err != nil {
			return err
		}
		if !grant.ApprovalOnly {
			return errors.New("grant scope")
		}
		for _, route := range []string{"/ask_bot", "/list_bots", "/list_threads", "/close_thread", "/list_room_targets"} {
			req, _ := http.NewRequestWithContext(ctx, "POST", grant.URL+route, strings.NewReader(`{}`))
			req.Header.Set("Authorization", "Bearer "+grant.Token)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				return errors.New("approval grant allowed handoff tools")
			}
		}
		body := `{"id":"edit","kind":"tool","tool":"Edit"}`
		req, _ := http.NewRequestWithContext(ctx, "POST", grant.URL+"/approve", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+grant.Token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		var decision driver.ApprovalDecision
		if err = json.NewDecoder(resp.Body).Decode(&decision); err != nil {
			return err
		}
		if decision.Outcome != "allow" {
			return fmtError("unexpected decision", decision)
		}
		mu.Lock()
		modes = append(modes, in.ApprovalMode)
		mu.Unlock()
		if in.MCP != nil {
			return errors.New("handoff turn received delegation tools")
		}
		emit(driver.Event{Kind: driver.EvText, Text: "approved"})
		return nil
	}}
	r := newRig(t, &asker{}, d)
	a := r.bot("Ada", "asker", "")
	a.ApprovalMode = "auto-edit"
	r.st.SaveBot(a)
	b := r.bot("Bo", "test", "")
	b.ApprovalMode = "ask"
	r.st.SaveBot(b)
	r.h.Send(a, "ask Bo edit")
	u := r.wait(a.ID)
	if !strings.Contains(u.Reply.Text, `"status":"accepted"`) {
		t.Fatal(u.Reply)
	}
	waitJobs(t, r.h)
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(modes, []string{"auto-edit"}) {
		t.Fatal(modes)
	}
	thread, _ := r.st.HandoffThread(b.ID, a.ID)
	events, _ := r.st.Events(thread.ID, "")
	if len(events) == 0 {
		t.Fatal("missing handoff events")
	}
	main, _ := r.st.Events(b.ID, "")
	if len(main) != 0 {
		t.Fatal("handoff leaked into main runtime log")
	}
}
func fmtError(message string, value any) error {
	b, _ := json.Marshal(value)
	return errors.New(message + ": " + string(b))
}

func TestQueuedHandoffCancellationNoGhost(t *testing.T) {
	started := make(chan struct{}, 1)
	r := newRig(t, &asker{}, &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}})
	a := r.bot("Ada", "asker", "")
	b := r.bot("Bo", "test", "")
	r.h.Send(b, "busy")
	receive(t, started)
	r.h.Send(a, "ask Bo queued")
	eventually(t, func() bool { r.h.mu.Lock(); defer r.h.mu.Unlock(); return len(r.h.queues[b.ID]) == 1 })
	r.h.Stop(a.ID)
	r.wait(a.ID)
	eventually(t, func() bool { r.h.mu.Lock(); defer r.h.mu.Unlock(); return len(r.h.queues[b.ID]) == 0 })
	msgs, _ := r.st.HandoffTranscript(b.ID, a.ID)
	if len(msgs) != 0 {
		t.Fatalf("ghost handoff: %+v", msgs)
	}
	r.h.Stop(b.ID)
	r.wait(b.ID)
}

func TestCyclicHandoffsDoNotDeadlock(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	base := &asker{}
	d := &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		if in.MCP != nil {
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return base.Run(ctx, in, emit)
	}}
	r := newRig(t, d)
	a := r.bot("Ada", "test", "")
	b := r.bot("Bo", "test", "")
	r.h.Send(a, "ask Bo hello")
	r.h.Send(b, "ask Ada hello")
	receive(t, started)
	receive(t, started)
	close(release)
	// Drain both completions without discarding the other root's event.
	roots := map[string]bool{}
	for len(roots) < 2 {
		u := receive(t, r.done)
		if u.Answering == "" {
			roots[u.BotID] = true
		}
	}
	waitJobs(t, r.h)
}

func TestUserAndHandoffShareFIFO(t *testing.T) {
	starts := make(chan string, 4)
	release := make(chan struct{}, 4)
	r := newRig(t, &asker{}, &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		starts <- in.Text
		select {
		case <-release:
			emit(driver.Event{Kind: driver.EvText, Text: "ok"})
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	a := r.bot("Ada", "asker", "")
	b := r.bot("Bo", "test", "")
	r.h.Send(b, "active")
	receive(t, starts)
	r.h.Send(b, "user-before")
	r.h.Send(a, "ask Bo delegated")
	eventually(t, func() bool { r.h.mu.Lock(); defer r.h.mu.Unlock(); return len(r.h.queues[b.ID]) == 2 })
	r.h.Send(b, "user-after")
	for _, want := range []string{"user-before", "delegated", "user-after"} {
		release <- struct{}{}
		if got := receive(t, starts); !strings.HasSuffix(got, want) {
			t.Fatalf("got %q want suffix %q", got, want)
		}
	}
	release <- struct{}{}
	eventually(t, func() bool { return !r.h.Busy(a.ID) && !r.h.Busy(b.ID) })
}

func TestQueuedCancellationBeforeExecution(t *testing.T) {
	calls := make(chan string, 2)
	release := make(chan struct{})
	h := testHarness(t, &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		calls <- in.Text
		select {
		case <-release:
			emit(driver.Event{Kind: driver.EvText, Text: "ok"})
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	b := savedBot(t, h)
	h.Send(b, "active")
	receive(t, calls)
	ctx, cancel := context.WithCancel(context.Background())
	h.mu.Lock()
	queued := newTurn(ctx, b, store.Message{ID: store.NewID(), Role: "user", Text: "ghost", At: time.Now()}, nil)
	if err := h.enqueueLocked(queued); err != nil {
		t.Fatal(err)
	}
	h.mu.Unlock()
	cancel()
	close(release)
	receive(t, queued.done)
	eventually(t, func() bool { return !h.Busy(b.ID) })
	msgs, _ := h.Store.Transcript(b.ID)
	if len(msgs) != 2 || msgs[0].Text != "active" {
		t.Fatal(msgs)
	}
	select {
	case got := <-calls:
		t.Fatalf("canceled work executed: %s", got)
	default:
	}
}

func TestEventDurableWhenObservedAndMidTurnFailure(t *testing.T) {
	emitNow := make(chan struct{})
	started := make(chan struct{})
	dir := t.TempDir()
	st, _ := store.Open(dir)
	h := New(st, &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		close(started)
		select {
		case <-emitNow:
			emit(driver.Event{Kind: driver.EvText, Text: "cannot persist"})
			<-ctx.Done()
			return ctx.Err()
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	defer h.Close()
	b := savedBot(t, h)
	updates, u := h.Subscribe()
	defer u()
	h.Send(b, "go")
	receive(t, started)
	// The observed start event already has a matching fsynced runtime record.
	for {
		event := receive(t, updates)
		if event.User != nil {
			events, err := st.Events(b.ID, "")
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, saved := range events {
				var decoded Update
				json.Unmarshal(saved.Data, &decoded)
				if decoded.User != nil && decoded.User.ID == event.User.ID {
					found = true
				}
			}
			if !found {
				t.Fatal("published before persistence")
			}
			break
		}
	}
	// Replace only this thread's runtime file with a directory to force EISDIR.
	path := filepath.Join(dir, "events", b.ID+".ndjson")
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	close(emitNow)
	final := doneUpdate(t, updates, b.ID)
	if final.Error == "" || final.Err == nil {
		t.Fatal("mid-turn persistence failure hidden")
	}
}
