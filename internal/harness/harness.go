// Package harness owns the bots' turns: it runs each on the bot's driver,
// folds the driver's events into the transcript, and tells the UI. It also
// serves the bqbot MCP tools, through which a bot asks another bot.
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

// Update is a change to a bot's conversation, for the UI to fold.
type Update struct {
	BotID string
	// Reply is the assistant message in progress (or finished); nil when only
	// Busy changed, as for a turn answering another bot, which is not part
	// of the bot's conversation with the person.
	Reply *store.Message
	Busy  bool
	// Answering is the name of the bot whose question the turn answers, ""
	// for a turn for the person.
	Answering string
	// Done is set once the turn has ended; Reply then holds the final message.
	Done bool
	Err  error
}

// Harness runs turns. Its methods are safe for concurrent use.
type Harness struct {
	Store   *store.Store
	Drivers map[string]driver.Driver
	// OnUpdate is called from the turn's goroutine.
	OnUpdate func(Update)

	mu      sync.Mutex
	running map[string]*turn
	hand    *handoff // nil until Listen
}

// New returns a harness over st with the given drivers.
func New(st *store.Store, drivers ...driver.Driver) *Harness {
	h := &Harness{Store: st, Drivers: map[string]driver.Driver{}, running: map[string]*turn{}}
	for _, d := range drivers {
		h.Drivers[d.Name()] = d
	}
	return h
}

// ErrBusy is returned for a bot whose previous turn has not ended.
var ErrBusy = errors.New("the bot is still answering")

// turn is a running turn.
type turn struct {
	bot       store.Bot
	depth     int    // 0 for the person's message, 1 for another bot's question
	asker     string // the asking bot's ID, "" for the person
	askerName string
	cancel    context.CancelFunc
	done      chan struct{} // closed when the turn has ended
	result    store.Message // the final reply, once done
	err       error

	mu    sync.Mutex
	reply store.Message
	text  strings.Builder
	brk   bool
}

// Busy reports whether the bot has a turn running.
func (h *Harness) Busy(botID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.running[botID]
	return ok
}

// Send records the person's message and starts a turn for it. It returns the
// message as recorded; the reply comes through OnUpdate.
func (h *Harness) Send(bot store.Bot, text string) (store.Message, error) {
	m, _, err := h.start(bot, text, nil)
	return m, err
}

// start records the message and starts the turn. from is the asking turn,
// nil for the person.
func (h *Harness) start(bot store.Bot, text string, from *turn) (store.Message, *turn, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return store.Message{}, nil, errors.New("empty message")
	}
	d, ok := h.Drivers[bot.Engine]
	if !ok {
		return store.Message{}, nil, fmt.Errorf("engine %q is not available", bot.Engine)
	}
	dir, err := h.Store.WorkDir(bot.ID)
	if err != nil {
		return store.Message{}, nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	t := &turn{bot: bot, cancel: cancel, done: make(chan struct{})}
	if from != nil {
		t.depth, t.asker, t.askerName = from.depth+1, from.bot.ID, from.bot.Name
	}
	h.mu.Lock()
	if _, ok := h.running[bot.ID]; ok {
		h.mu.Unlock()
		cancel()
		return store.Message{}, nil, ErrBusy
	}
	h.running[bot.ID] = t
	hand := h.hand
	h.mu.Unlock()

	user := store.Message{ID: store.NewID(), Role: "user", Text: text, At: time.Now()}
	prompt := text
	if from != nil {
		user.From = from.bot.Name
		prompt = fmt.Sprintf("%s, another of the person's bots, asks you this. Your answer goes back to %s, not to the person.\n\n%s",
			from.bot.Name, from.bot.Name, text)
	}
	if err := h.record(t, user); err != nil {
		h.finish(t)
		return store.Message{}, nil, err
	}
	in := driver.TurnInput{Text: prompt, Model: bot.Model, Cursor: t.cursor(), Dir: dir}
	// Only a turn for the person may ask other bots: an answer to another
	// bot gets no bqbot tools, so handoffs go one level deep.
	var revoke func()
	if t.depth == 0 && hand != nil {
		srv, rv, err := hand.grant(t)
		if err != nil {
			h.finish(t)
			return store.Message{}, nil, err
		}
		in.MCP, revoke = srv, rv
	}
	in.System = h.persona(bot, in.MCP != nil)
	go h.run(ctx, d, t, in, revoke)
	return user, t, nil
}

// Stop interrupts the bot's running turn, if any.
func (h *Harness) Stop(botID string) {
	h.mu.Lock()
	t := h.running[botID]
	h.mu.Unlock()
	if t != nil {
		t.cancel()
	}
}

func (h *Harness) finish(t *turn) {
	t.cancel()
	h.mu.Lock()
	if h.running[t.bot.ID] == t {
		delete(h.running, t.bot.ID)
	}
	h.mu.Unlock()
	close(t.done)
}

// cursor is the session the turn resumes: the bot's conversation with the
// person, or its own session with the asking bot.
func (t *turn) cursor() string {
	if t.asker != "" {
		return t.bot.Handoffs[t.asker]
	}
	return t.bot.Cursor
}

// record adds m to the transcript of the turn's session.
func (h *Harness) record(t *turn, m store.Message) error {
	if t.asker != "" {
		return h.Store.AppendHandoff(t.bot.ID, t.asker, m)
	}
	return h.Store.Append(t.bot.ID, m)
}

// update tells the UI of the turn; a turn answering another bot shows only
// as busy.
func (h *Harness) update(t *turn, u Update) {
	if t.asker != "" {
		u.Reply, u.Err, u.Answering = nil, nil, t.askerName
	}
	h.notify(u)
}

func (h *Harness) notify(u Update) {
	if h.OnUpdate != nil {
		h.OnUpdate(u)
	}
}

// fold applies fn to the turn's reply and tells the UI.
func (h *Harness) fold(t *turn, fn func()) {
	t.mu.Lock()
	fn()
	t.reply.Text = t.text.String()
	u := Update{BotID: t.bot.ID, Busy: true, Reply: clone(t.reply)}
	t.mu.Unlock()
	h.update(t, u)
}

// note adds a line to the turn's activity, as a tool call shows.
func (h *Harness) note(t *turn, line string) {
	h.fold(t, func() { t.reply.Tools = append(t.reply.Tools, line) })
}

func (h *Harness) run(ctx context.Context, d driver.Driver, t *turn, in driver.TurnInput, revoke func()) {
	bot := t.bot
	t.reply = store.Message{ID: store.NewID(), Role: "assistant", At: time.Now()}
	cursor := t.cursor()
	h.update(t, Update{BotID: bot.ID, Busy: true, Reply: clone(t.reply)})

	err := d.Run(ctx, in, func(e driver.Event) {
		switch e.Kind {
		case driver.EvSession:
			cursor = e.Cursor
		case driver.EvBreak:
			t.mu.Lock()
			t.brk = t.text.Len() > 0
			t.mu.Unlock()
		case driver.EvText:
			h.fold(t, func() {
				if t.brk {
					t.text.WriteString("\n\n")
					t.brk = false
				}
				t.text.WriteString(e.Text)
			})
		case driver.EvTool:
			if bqbotTool(e.Text) {
				return // the handoff adds its own line
			}
			h.fold(t, func() { t.reply.Tools = append(t.reply.Tools, e.Text) })
		}
	})
	if revoke != nil {
		revoke()
	}

	t.mu.Lock()
	reply := t.reply
	t.mu.Unlock()
	stopped := errors.Is(err, context.Canceled)
	if stopped {
		err = nil
		if reply.Text != "" {
			reply.Text += "\n\n*(stopped)*"
		} else {
			reply.Text = "*(stopped)*"
		}
	}
	if err != nil && reply.Text == "" {
		reply.Role, reply.Text = "error", err.Error()
	}
	if reply.Text != "" || len(reply.Tools) > 0 {
		if serr := h.record(t, reply); serr != nil && err == nil {
			err = serr
		}
	}
	if cursor != t.cursor() {
		h.saveCursor(bot.ID, t.asker, cursor)
	}
	t.result, t.err = reply, err
	if stopped {
		t.err = context.Canceled
	}
	h.finish(t)
	h.update(t, Update{BotID: bot.ID, Reply: clone(reply), Done: true, Err: err})
}

// bqbotTool reports whether a driver's tool label is a call of the bqbot
// MCP server: mcp__bqbot__ask_bot (claude, pi) or bqbot.ask_bot (codex).
func bqbotTool(label string) bool {
	return strings.HasPrefix(label, mcp.ToolPrefix) || strings.HasPrefix(label, mcp.Server+".")
}

// saveCursor stores the session to resume, the one with the asking bot for
// an asker, re-reading the bot so an edit made during the turn is kept.
func (h *Harness) saveCursor(botID, asker, cursor string) {
	bots, err := h.Store.Bots()
	if err != nil {
		return
	}
	for _, b := range bots {
		if b.ID == botID {
			if asker == "" {
				b.Cursor = cursor
			} else {
				if b.Handoffs == nil {
					b.Handoffs = map[string]string{}
				}
				b.Handoffs[asker] = cursor
			}
			h.Store.SaveBot(b)
			return
		}
	}
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
		"ask it with the " + mcp.ToolPrefix + mcp.ToolAsk + " tool, giving it the context it needs, then answer the person " +
		"yourself with what it said. " + mcp.ToolPrefix + mcp.ToolList + " shows the current list." + roster.String()
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
	return &m
}
