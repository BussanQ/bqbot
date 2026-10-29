// Package harness schedules turns and mediates permissions and durable events.
package harness

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"bqbot/internal/driver"
	"bqbot/internal/mcp"
	"bqbot/internal/store"
)

// Update is the service-facing runtime event, routed by ThreadID or RoomID.
type Update struct {
	ThreadID         string                   `json:"thread_id,omitempty"`
	RoomID           string                   `json:"room_id,omitempty"`
	Thread           *store.Thread            `json:"thread,omitempty"`
	Room             *store.Room              `json:"room,omitempty"`
	Metadata         bool                     `json:"metadata,omitempty"`
	Sequence         uint64                   `json:"sequence,omitempty"`
	Sync             bool                     `json:"sync,omitempty"`
	Bot              *store.Bot               `json:"bot,omitempty"`
	Messages         []store.Message          `json:"messages,omitempty"`
	QueuedMessages   []store.Message          `json:"queued_messages,omitempty"`
	Approvals        []driver.ApprovalRequest `json:"approvals,omitempty"`
	BotID            string                   `json:"bot_id"`
	Reply            *store.Message           `json:"reply,omitempty"`
	User             *store.Message           `json:"user,omitempty"`
	Busy             bool                     `json:"busy"`
	Answering        string                   `json:"answering,omitempty"`
	Done             bool                     `json:"done"`
	Error            string                   `json:"error,omitempty"`
	Err              error                    `json:"-"`
	Approval         *driver.ApprovalRequest  `json:"approval,omitempty"`
	ResolvedApproval string                   `json:"resolved_approval,omitempty"`
	Event            *driver.Event            `json:"event,omitempty"`
	Decision         *driver.ApprovalDecision `json:"decision,omitempty"`
	QueuedUser       *store.Message           `json:"queued_user,omitempty"`
	Queued           int                      `json:"queued"`
}

// Harness runs turns. Configure Drivers and OnUpdate before submitting work;
// use Subscribe for concurrently attaching and detaching consumers.
type Harness struct {
	Store         *store.Store
	Drivers       map[string]driver.Driver
	OnUpdate      func(Update)
	mu            sync.Mutex
	running       map[string]*turn
	queues        map[string][]*turn
	deleting      map[string]bool
	subscribers   map[*subscription]bool
	legacy        *subscription
	hand          *handoff
	closed        bool
	sequence      uint64
	closingScopes map[string]bool
	jobs          map[*asyncJob]bool
	routing       map[*roomRoute]bool
}

func New(st *store.Store, drivers ...driver.Driver) *Harness {
	h := &Harness{Store: st, Drivers: map[string]driver.Driver{}, running: map[string]*turn{}, queues: map[string][]*turn{}, deleting: map[string]bool{}, subscribers: map[*subscription]bool{}, closingScopes: map[string]bool{}, jobs: map[*asyncJob]bool{}, routing: map[*roomRoute]bool{}}
	for _, d := range drivers {
		h.Drivers[d.Name()] = d
	}
	return h
}

// ErrBusy remains for callers that used the phase-one API. Submit now queues.
var ErrBusy = errors.New("the bot is still answering")

type turn struct {
	thread           store.Thread
	roomID           string
	roomUserRecorded bool
	job              *asyncJob
	wake             bool
	dispatched       int
	bot              store.Bot
	depth            int
	asker, askerName string
	ctx              context.Context
	requestCtx       context.Context
	cancel           context.CancelFunc
	done             chan struct{}
	result           store.Message
	err              error
	reply            store.Message
	user             store.Message
	text             strings.Builder
	brk              bool
	cursorValue      string
	output           bool
	recorded         bool
	prepared         bool
	pending          map[string]*pendingApproval
	approvalIDs      map[string]bool
	steer            chan string
	waiting          map[*turn]bool
}

func (h *Harness) Busy(id string) bool { h.mu.Lock(); defer h.mu.Unlock(); return h.running[id] != nil }

// Bot returns the latest persisted bot, including session cursors.
func (h *Harness) Bot(id string) (store.Bot, error) {
	bots, err := h.Store.Bots()
	if err != nil {
		return store.Bot{}, err
	}
	for _, b := range bots {
		if b.ID == id {
			return b, nil
		}
	}
	return store.Bot{}, fmt.Errorf("bot %q not found", id)
}

// Send is Submit with no attachments and queue mode.
func (h *Harness) Send(bot store.Bot, text string) (store.Message, error) {
	return h.Submit(bot, text, nil, "queue")
}

// Submit accepts a message into the bot's FIFO. A queued message is added to
// the transcript only when execution starts, so cancellation leaves no ghost.
// Engines may implement SupportsSteering() bool to receive text through Steer;
// unsupported steering, image steering and a full steer buffer fall back to FIFO.
func (h *Harness) Submit(bot store.Bot, text string, attachments []driver.Attachment, mode string) (store.Message, error) {
	return h.SendThread(bot.ID, text, attachments, mode)
}

func newTurn(parent context.Context, bot store.Bot, user store.Message, from *turn) *turn {
	ctx, cancel := context.WithCancel(parent)
	t := &turn{bot: bot, user: user, ctx: ctx, cancel: cancel, done: make(chan struct{}), pending: map[string]*pendingApproval{}, approvalIDs: map[string]bool{}, steer: make(chan string, 32), waiting: map[*turn]bool{}, reply: store.Message{ID: store.NewID(), Role: "assistant", At: time.Now()}}
	if from != nil {
		t.depth = from.depth + 1
		t.asker = from.bot.ID
		t.askerName = from.bot.Name
		t.bot.ApprovalMode = from.bot.ApprovalMode
		t.user.From = from.bot.Name
	}
	return t
}

// enqueueLocked serializes queue admission and its durable event.
func (h *Harness) enqueueLocked(t *turn) error {
	id := t.bot.ID
	if t.thread.ID == "" {
		var err error
		if t.asker != "" {
			t.thread, err = h.Store.HandoffThread(id, t.asker)
		} else {
			t.thread, err = h.Store.Thread(id)
		}
		if err != nil {
			t.cancel()
			return err
		}
	}
	t.user.ThreadID, t.reply.ThreadID = t.thread.ID, t.thread.ID
	t.reply.BotID = id
	if t.roomID != "" {
		t.reply.From = t.bot.Name
	}
	if t.thread.Closed || h.closingScopes[t.thread.ID] || h.closingScopes[t.roomID] {
		t.cancel()
		return errors.New("conversation is closing")
	}
	if h.closed || h.deleting[id] || h.deleting[t.asker] {
		t.cancel()
		return errors.New("bot is closing")
	}
	if h.Drivers[t.bot.Engine] == nil {
		t.cancel()
		return fmt.Errorf("engine %q is not available", t.bot.Engine)
	}
	h.queues[id] = append(h.queues[id], t)
	if err := h.publishLocked(t, Update{BotID: id, Busy: true, QueuedUser: clone(t.user)}); err != nil {
		h.queues[id] = h.queues[id][:len(h.queues[id])-1]
		t.cancel()
		return err
	}
	h.nextLocked(id)
	return nil
}

func (h *Harness) nextLocked(id string) {
	if h.running[id] != nil {
		return
	}
	for len(h.queues[id]) > 0 {
		t := h.queues[id][0]
		h.queues[id] = h.queues[id][1:]
		if err := t.contextErr(); err != nil {
			t.err = err
			t.cancel()
			h.publishLocked(t, Update{BotID: id, Busy: len(h.queues[id]) > 0})
			close(t.done)
			continue
		}
		h.running[id] = t
		go h.run(t)
		return
	}
	delete(h.queues, id)
}

// Stop cancels the active turn and discards all queued work for the bot.
func (h *Harness) Stop(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopLocked(id)
}
func (h *Harness) stopLocked(id string) {
	h.cancelRoutesLocked(func(r *roomRoute) bool { return member(r.room, id) })
	h.cancelJobsLocked(func(j *asyncJob) bool { return j.botID == id || j.target.bot.ID == id })
	for _, t := range append([]*turn(nil), h.queues[id]...) {
		h.cancelQueuedLocked(t)
	}
	if t := h.running[id]; t != nil {
		t.cancel()
		h.cancelApprovalsLocked(t)
	}
}
func (h *Harness) cancelQueuedLocked(t *turn) {
	q := h.queues[t.bot.ID]
	for i, candidate := range q {
		if candidate == t {
			h.queues[t.bot.ID] = append(q[:i], q[i+1:]...)
			t.cancel()
			t.err = context.Canceled
			if err := h.publishLocked(t, Update{BotID: t.bot.ID, Busy: h.running[t.bot.ID] != nil}); err != nil {
				t.err = err
			}
			close(t.done)
			return
		}
	}
}

// DeleteBot prevents admission, cancels and awaits all work involving the bot,
// then removes its data. No worker can recreate deleted transcripts afterward.
func (h *Harness) DeleteBot(id string) error {
	h.mu.Lock()
	if h.deleting[id] {
		h.mu.Unlock()
		return errors.New("bot is already being deleted")
	}
	h.deleting[id] = true
	waits := h.cancelJobsLocked(func(j *asyncJob) bool { return j.botID == id || j.target.bot.ID == id })
	waits = append(waits, h.cancelRoutesLocked(func(r *roomRoute) bool { return member(r.room, id) })...)
	for _, t := range h.running {
		if t.bot.ID == id || t.asker == id {
			t.cancel()
			h.cancelApprovalsLocked(t)
			waits = append(waits, t.done)
		}
	}
	for _, q := range h.queues {
		for _, t := range append([]*turn(nil), q...) {
			if t.bot.ID == id || t.asker == id {
				h.cancelQueuedLocked(t)
			}
		}
	}
	h.mu.Unlock()
	for _, done := range waits {
		<-done
	}
	err := h.Store.DeleteBot(id)
	h.mu.Lock()
	delete(h.deleting, id)
	h.metadataLocked(Update{BotID: id})
	h.mu.Unlock()
	return err
}

func (t *turn) contextErr() error {
	if err := t.ctx.Err(); err != nil {
		return err
	}
	if t.requestCtx != nil {
		return t.requestCtx.Err()
	}
	return nil
}

func (t *turn) cursor() string { return t.thread.Cursor }
func (h *Harness) record(t *turn, m store.Message) error {
	m.ThreadID = t.thread.ID
	if m.Role != "user" {
		m.BotID = t.bot.ID
	}
	if t.roomID != "" {
		if m.Role == "user" && t.roomUserRecorded {
			return nil
		}
		if m.Role != "user" {
			m.From = t.reply.From
		}
		return h.Store.AppendRoom(t.roomID, m)
	}
	return h.Store.AppendThread(t.thread.ID, m)
}
func (h *Harness) transcript(t *turn) ([]store.Message, error) {
	if t.roomID != "" {
		return h.Store.RoomTranscript(t.roomID)
	}
	return h.Store.ThreadTranscript(t.thread.ID)
}

func (h *Harness) note(t *turn, line string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if t.ctx.Err() != nil {
		return
	}
	t.reply.Tools = append(t.reply.Tools, line)
	t.output = true
	h.publishLocked(t, Update{BotID: t.bot.ID, Reply: clone(t.reply), Busy: true})
}

func (h *Harness) run(t *turn) {
	in, revoke, err := h.prepare(t)
	if revoke != nil {
		defer revoke()
	}
	if err == nil {
		d := h.Drivers[t.bot.Engine]
		emit := func(e driver.Event) { h.emit(t, e) }
		err = d.Run(t.ctx, in, emit)
		h.mu.Lock()
		retry := errors.Is(err, driver.ErrSessionExpired) && !t.output && t.err == nil && t.ctx.Err() == nil
		h.mu.Unlock()
		if retry {
			in.Cursor = ""
			h.mu.Lock()
			t.cursorValue = ""
			h.mu.Unlock()
			err = d.Run(t.ctx, in, emit)
		}
	}
	if revoke != nil {
		revoke()
	} // revoke before completion becomes observable
	h.mu.Lock()
	defer h.mu.Unlock()
	if err == nil {
		err = t.contextErr()
	}
	t.cancel()
	h.cancelApprovalsLocked(t)
	if t.err != nil {
		err = errors.Join(err, t.err)
	}
	stopped := errors.Is(err, context.Canceled) && t.err == nil
	if stopped {
		if t.reply.Text != "" {
			t.reply.Text += "\n\n"
		}
		t.reply.Text += "*(stopped)*"
		err = nil
	}
	if err != nil && t.reply.Text == "" {
		t.reply.Role = "error"
		t.reply.Text = err.Error()
	}
	if t.recorded && (t.reply.Text != "" || len(t.reply.Tools) > 0) {
		err = errors.Join(err, h.record(t, t.reply))
	}
	if t.cursorValue != t.cursor() {
		err = errors.Join(err, h.Store.SaveThreadCursor(t.thread, t.cursorValue))
	}
	t.result = *clone(t.reply)
	t.err = err
	if stopped {
		t.err = context.Canceled
	}
	delete(h.running, t.bot.ID)
	if e := h.publishLocked(t, Update{BotID: t.bot.ID, Reply: clone(t.reply), Done: true, Err: err, Busy: len(h.queues[t.bot.ID]) > 0}); e != nil {
		t.err = errors.Join(t.err, e)
	}
	close(t.done)
	h.completeJobLocked(t)
	h.nextLocked(t.bot.ID)
}

func (h *Harness) prepare(t *turn) (driver.TurnInput, func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var in driver.TurnInput
	// Refresh queued jobs so edits and the preceding turn's cursor take effect.
	b, err := h.Bot(t.bot.ID)
	if err != nil {
		return in, nil, err
	}
	if t.asker != "" || t.wake {
		b.ApprovalMode = t.bot.ApprovalMode
	}
	switch b.ApprovalMode {
	case "ask", "auto-edit", "full-auto":
	default:
		b.ApprovalMode = "ask"
	}
	t.bot = b
	thread, err := h.Store.Thread(t.thread.ID)
	if err != nil {
		return in, nil, err
	}
	if thread.Closed {
		return in, nil, errors.New("thread is closed")
	}
	t.thread = thread
	t.cursorValue = t.cursor()
	if err := t.contextErr(); err != nil {
		return in, nil, err
	}
	if t.roomID != "" {
		room, e := h.Store.Room(t.roomID)
		if e != nil {
			return in, nil, e
		}
		if !member(room, b.ID) {
			return in, nil, errors.New("bot is no longer a channel member")
		}
	}
	history, err := h.transcript(t)
	if err != nil {
		return in, nil, err
	}
	if err = h.record(t, t.user); err != nil {
		return in, nil, err
	}
	t.recorded = true
	if err = h.publishLocked(t, Update{BotID: b.ID, User: clone(t.user), Reply: clone(t.reply), Busy: true}); err != nil {
		return in, nil, err
	}
	dir, err := h.Store.WorkDir(b.ID)
	if err != nil {
		return in, nil, err
	}
	if t.roomID != "" {
		dir, err = h.Store.RoomWorkDir(t.roomID)
		if err != nil {
			return in, nil, err
		}
	}
	settings, err := h.Store.Settings()
	if err != nil {
		return in, nil, err
	}
	in = driver.TurnInput{Text: t.user.Text, Model: b.Model, Effort: b.Effort, Cursor: t.cursor(), Dir: dir, Settings: settings[b.Engine], ApprovalMode: b.ApprovalMode, Attachments: append([]driver.Attachment(nil), t.user.Attachments...), Steer: t.steer}
	if t.roomID != "" {
		in.Cursor = ""
	}
	if t.thread.Summary != "" && t.roomID == "" {
		in.History = append(in.History, driver.HistoryMessage{Role: "user", Text: "Earlier conversation summary:\n" + t.thread.Summary})
		for i, m := range history {
			if m.ID == t.thread.SummaryThrough {
				history = history[i+1:]
				break
			}
		}
	}
	for _, m := range history {
		if m.ID == t.user.ID {
			continue
		}
		text := m.Text
		if t.roomID != "" && m.From != "" {
			text = m.From + ": " + text
		}
		if m.Role == "user" || m.Role == "assistant" {
			in.History = append(in.History, driver.HistoryMessage{Role: m.Role, Text: text, Attachments: append([]driver.Attachment(nil), m.Attachments...)})
		}
	}
	if t.asker != "" {
		in.Text = fmt.Sprintf("%s, another of the person's bots, asks you this. Your answer goes back to %s, not to the person.\n\n%s", t.askerName, t.askerName, t.user.Text)
	}
	in.Approve = func(ctx context.Context, req driver.ApprovalRequest) (driver.ApprovalDecision, error) {
		return h.approval(ctx, t, req)
	}
	var revokes []func()
	revoke := func() {
		for _, rv := range revokes {
			rv()
		}
	}
	if h.hand != nil && b.Engine != "openai" {
		srv, rv, e := h.hand.grant(t, true)
		if e != nil {
			return in, revoke, e
		}
		revokes = append(revokes, rv)
		in.ApprovalMCP = srv
		if t.depth == 0 {
			srv, rv, e = h.hand.grant(t, false)
			if e != nil {
				return in, revoke, e
			}
			revokes = append(revokes, rv)
			in.MCP = srv
		}
	}
	in.System = h.persona(b, in.MCP != nil)
	scope := t.thread.ID
	if t.roomID != "" {
		room, e := h.Store.Room(t.roomID)
		if e != nil {
			return in, revoke, e
		}
		in.System = h.persona(b, false) + "\n\nShared channel instructions:\n" + room.Instructions
		in.System += "\nChannel members:"
		for _, id := range room.Members {
			other, e := h.Bot(id)
			if e == nil {
				in.System += "\n- " + other.Name + ": " + clip(other.Persona, 200)
			}
		}
		if in.MCP != nil {
			in.System += "\nUse ask_bot to delegate to a channel member. It returns a receipt immediately; finish the turn without polling. You will be resumed when the result arrives."
		}
		scope = t.roomID
	}
	memories, e := h.Store.Recall(scope, t.user.Text)
	if e != nil {
		return in, revoke, e
	}
	for _, memory := range memories {
		in.System += "\nRecalled memory: " + memory.Text
	}
	if h.Drivers[b.Engine] == nil {
		return in, revoke, fmt.Errorf("engine %q is not available", b.Engine)
	}
	t.prepared = true
	return in, revoke, nil
}

func (h *Harness) emit(t *turn, e driver.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if t.ctx.Err() != nil {
		return
	}
	switch e.Kind {
	case driver.EvSession:
		t.cursorValue = e.Cursor
	case driver.EvBreak:
		t.brk = t.text.Len() > 0
	case driver.EvText:
		if e.Text != "" {
			t.output = true
		}
		if t.brk {
			t.text.WriteString("\n\n")
			t.brk = false
		}
		t.text.WriteString(e.Text)
		t.reply.Text = t.text.String()
	case driver.EvTool:
		t.output = true
		if !bqbotTool(e.Text) {
			t.reply.Tools = append(t.reply.Tools, e.Text)
		}
	}
	h.publishLocked(t, Update{BotID: t.bot.ID, Reply: clone(t.reply), Busy: true, Event: &e})
}
func bqbotTool(label string) bool {
	return strings.HasPrefix(label, mcp.ToolPrefix) || strings.HasPrefix(label, mcp.Server+".")
}

// persona is the bot's system prompt. With handoff, it lists the other bots
// that can be asked.
func (h *Harness) persona(b store.Bot, handoff bool) string {
	s := "You are " + b.Name + ", one of the person's bots in the bqbot chat app."
	if p := strings.TrimSpace(b.Persona); p != "" {
		s += "\n\n" + p
	}
	if !handoff {
		return s
	}
	bots, _ := h.Store.Bots()
	var roster strings.Builder
	for _, o := range bots {
		if o.ID == b.ID {
			continue
		}
		roster.WriteString("\n- " + o.Name)
		if p := strings.TrimSpace(o.Persona); p != "" {
			roster.WriteString(": " + clip(p, 200))
		}
	}
	if roster.Len() == 0 {
		return s
	}
	return s + "\n\nThe person's other bots are below. When one of them is better placed to answer part of a request, " +
		"ask it with the " + mcp.ToolPrefix + mcp.ToolAsk + " tool, giving it the context it needs. The tool returns a receipt immediately; finish this turn without polling. You will be automatically resumed when the answer arrives. " + mcp.ToolPrefix + mcp.ToolList + " shows the current list." + roster.String()
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func clone(m store.Message) *store.Message {
	m.Tools = append([]string(nil), m.Tools...)
	m.Attachments = append([]driver.Attachment(nil), m.Attachments...)
	return &m
}
