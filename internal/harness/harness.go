// Package harness schedules turns and mediates permissions and durable events.
package harness

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"bqbot/internal/computer"
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
	// Host is configured before admitting work, only by the owning desktop.
	Host          *computer.Host
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
	roomTurns     map[string][]*turn
}

func New(st *store.Store, drivers ...driver.Driver) *Harness {
	h := &Harness{Store: st, Drivers: map[string]driver.Driver{}, running: map[string]*turn{}, queues: map[string][]*turn{}, deleting: map[string]bool{}, subscribers: map[*subscription]bool{}, closingScopes: map[string]bool{}, jobs: map[*asyncJob]bool{}, routing: map[*roomRoute]bool{}, roomTurns: map[string][]*turn{}}
	for _, d := range drivers {
		h.Drivers[d.Name()] = d
	}
	return h
}

// ErrBusy remains for callers that used the phase-one API. Submit now queues.
var ErrBusy = errors.New("the bot is still answering")

type turn struct {
	external         *externalGroup
	consult          bool
	ancestors        []string
	chain            *coordinationChain
	delivery         *asyncJob
	thread           store.Thread
	roomID           string
	roomUserRecorded bool
	mentionedMembers []string // explicit recipients of the originating room message
	round            *roomRound
	revisit          bool
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
	toolIndexes      map[string]int // provider call ID to Tools index; -1 for calls handled by bqbot
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
		t.mentionedMembers = from.mentionedMembers
		t.ancestors = append(append([]string{}, from.ancestors...), from.bot.ID)
		t.chain = from.chain
		t.consult = from.consult
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
	if t.round == nil && h.Drivers[t.bot.Engine] == nil {
		t.cancel()
		return fmt.Errorf("engine %q is not available", t.bot.Engine)
	}
	if t.round != nil {
		h.roomTurns[t.roomID] = append(h.roomTurns[t.roomID], t)
	}
	h.queues[id] = append(h.queues[id], t)
	queued := Update{BotID: id, Busy: true}
	if !t.roomUserRecorded {
		queued.QueuedUser = clone(t.user)
	}
	if err := h.publishLocked(t, queued); err != nil {
		h.queues[id] = h.queues[id][:len(h.queues[id])-1]
		h.removeRoomTurnLocked(t)
		t.cancel()
		return err
	}
	h.nextLocked(id)
	return nil
}

func (h *Harness) nextLocked(id string) {
	if h.closed || h.running[id] != nil {
		return
	}
	for len(h.queues[id]) > 0 {
		// A member waiting for a peer must remain available for private work
		// and delegated questions, including questions from that peer.
		i := slices.IndexFunc(h.queues[id], func(t *turn) bool {
			return t.round == nil || h.roomTurns[t.roomID][0] == t
		})
		if i < 0 {
			return
		}
		t := h.queues[id][i]
		h.queues[id] = slices.Delete(h.queues[id], i, i+1)
		if err := t.contextErr(); err != nil {
			h.removeRoomTurnLocked(t)
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
	h.startRoomHeadsLocked()
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
			h.removeRoomTurnLocked(t)
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
	h.startRoomHeadsLocked()
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
	h.publishLocked(t, Update{BotID: t.bot.ID, Reply: t.visibleReply(), Busy: true})
}

func (h *Harness) run(t *turn) {
	in, revoke, err := h.prepare(t)
	var closeExternal func()
	if err == nil {
		var externalErr error
		closeExternal, externalErr = h.prepareExternal(t, &in)
		if closeExternal != nil {
			defer closeExternal()
		}
		err = externalErr
	}
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
	if closeExternal != nil {
		closeExternal()
	}
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
	if t.round != nil && t.reply.Text == "" && err == nil && strings.TrimSpace(t.text.String()) != roomPass {
		t.reply.Text = strings.TrimSpace(t.text.String())
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
	for i, result := range t.reply.ToolResults {
		if result.Status == driver.ToolRunning {
			result.Status = driver.ToolUnconfirmed
			t.reply.ToolResults[i] = result
		}
	}
	if t.recorded && (t.reply.Text != "" || len(t.reply.Tools) > 0) {
		err = errors.Join(err, h.record(t, t.reply))
	}
	if t.cursorValue != t.cursor() {
		err = errors.Join(err, h.Store.SaveThreadCursor(t.thread, t.cursorValue))
	}
	t.result = *clone(t.reply)
	if t.chain != nil && t.depth == 0 {
		t.chain.latest = t
	}
	t.err = err
	if stopped {
		t.err = context.Canceled
	}
	h.advanceDiscussionLocked(t)
	delete(h.running, t.bot.ID)
	if e := h.publishLocked(t, Update{BotID: t.bot.ID, Reply: t.visibleReply(), Done: true, Err: err, Busy: len(h.queues[t.bot.ID]) > 0}); e != nil {
		t.err = errors.Join(t.err, e)
	}
	h.completeJobLocked(t)
	close(t.done)
	h.cleanCanceledJobsLocked()
	h.nextLocked(t.bot.ID)
	h.startRoomHeadsLocked()
}

func (h *Harness) prepare(t *turn) (driver.TurnInput, func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var in driver.TurnInput
	// Refresh queued jobs so edits and the preceding turn's cursor take effect.
	if t.job != nil {
		t.job.assignment.Status = "running"
		if err := h.Store.SaveAssignment(t.job.assignment); err != nil {
			return in, nil, err
		}
	}
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
	if t.round != nil {
		t.reply.At = time.Now()
	}
	started := Update{BotID: b.ID, Reply: t.visibleReply(), Busy: true}
	if !t.roomUserRecorded {
		started.User = clone(t.user)
	}
	if err = h.publishLocked(t, started); err != nil {
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
	if t.round != nil {
		history = discussionHistory(t, history, &in)
	}
	for _, m := range history {
		if m.ID == t.user.ID && !in.RoomReview {
			continue
		}
		text := m.Text
		if t.roomID != "" && m.From != "" {
			text = m.From + ": " + text
		}
		if m.Role == "user" || m.Role == "assistant" {
			role := m.Role
			if t.round != nil && role == "assistant" && m.BotID != "" && m.BotID != b.ID {
				role = "user" // an attributed peer message, not this model's previous answer
			}
			in.History = append(in.History, driver.HistoryMessage{Role: role, Text: text, Attachments: append([]driver.Attachment(nil), m.Attachments...)})
		}
	}
	if t.asker != "" && !t.wake {
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
		if t.depth <= 4 {
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
				in.System += "\n- " + other.Name + " (@" + id + "): " + clip(other.Persona, 200)
			}
		}
		if t.round != nil {
			in.System += "\nThis conversation is shared with the room. Decide for yourself whether to respond as " + b.Name + ". Reply when addressed or when you have a useful contribution. Consider what others already said. When you have nothing to add, use no tools and output exactly " + roomPass + "."
		}
		if len(t.mentionedMembers) > 0 {
			in.System += "\nOnly members mentioned by the user participate in this turn."
		}
		if in.MCP != nil && len(t.mentionedMembers) != 1 {
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
		previous := t.reply.Text
		t.reply.Text = t.text.String()
		if t.round != nil && strings.HasPrefix(roomPass, strings.TrimSpace(t.reply.Text)) {
			t.reply.Text = ""
			return
		}
		if t.round != nil && previous == "" {
			e.Text = t.reply.Text // include any buffered prefix of an ordinary reply
		}
	case driver.EvTool, driver.EvToolResult, driver.EvToolDelta:
		t.output = true
		e.Output = driver.LimitToolOutput(e.Output)
		if !t.applyToolEvent(e) {
			return
		}
	}
	h.publishLocked(t, Update{BotID: t.bot.ID, Reply: t.visibleReply(), Busy: true, Event: &e})
}

// Approvals, delegation and external tools have their own harness events.
func handledTool(label string) bool {
	name, _, _ := strings.Cut(label, ":")
	if name == mcp.ApprovalToolName || name == mcp.ApprovalServer+"."+mcp.ToolApprove {
		return true
	}
	tool, ok := strings.CutPrefix(name, mcp.ToolPrefix)
	if !ok {
		tool, ok = strings.CutPrefix(name, mcp.Server+".")
	}
	return ok && (tool == mcp.ToolAsk || tool == mcp.ToolCoordinate || strings.HasPrefix(tool, "external_"))
}

func (t *turn) applyToolEvent(e driver.Event) bool {
	if e.ToolID == "" {
		// Older protocols may provide only a label. Never pair anonymous calls by name.
		if e.Kind != driver.EvTool || handledTool(e.Text) || e.Text == "" {
			return false
		}
		t.reply.Tools = append(t.reply.Tools, e.Text)
		return true
	}
	if t.toolIndexes == nil {
		t.toolIndexes = make(map[string]int)
	}
	i, known := t.toolIndexes[e.ToolID]
	if !known {
		if handledTool(e.Text) {
			t.toolIndexes[e.ToolID] = -1
			return false
		}
		i = len(t.reply.Tools)
		t.toolIndexes[e.ToolID] = i
		label := e.Text
		if label == "" {
			label = "工具"
		}
		t.reply.Tools = append(t.reply.Tools, label)
		if t.reply.ToolResults == nil {
			t.reply.ToolResults = make(map[int]store.ToolResult)
		}
	} else if i < 0 {
		return false
	}
	before := t.reply.ToolResults[i]
	after := before
	if after.Status == "" {
		after.Status = driver.ToolRunning
	}
	// A late progress notification must not reopen a finished call.
	if before.Status != "" && before.Status != driver.ToolRunning && (e.Status == driver.ToolRunning || e.Kind == driver.EvToolDelta) {
		return false
	}
	if e.Status != "" {
		after.Status = e.Status
	}
	if e.Kind == driver.EvToolDelta {
		after.Output = driver.AppendToolOutput(after.Output, e.Output)
	} else if e.Output != "" {
		after.Output = e.Output
	}
	renamed := e.Text != "" && t.reply.Tools[i] == "工具"
	if renamed {
		t.reply.Tools[i] = e.Text
	}
	t.reply.ToolResults[i] = after
	return !known || before != after || renamed
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
	prefix := mcp.ToolPrefix
	if b.Engine == "bqbot" {
		prefix = ""
	} // the built-in model sees the grant's MCP names directly
	return s + "\n\nThe person's other bots are below. When one of them is better placed to answer part of a request, " +
		"ask it with the " + prefix + mcp.ToolAsk + " tool, giving it the context it needs. The tool returns a receipt immediately; finish this turn without polling. You will be automatically resumed when the answer arrives. " + prefix + mcp.ToolList + " shows the current list." + roster.String()
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
	m.ToolResults = maps.Clone(m.ToolResults)
	m.Attachments = append([]driver.Attachment(nil), m.Attachments...)
	return &m
}
