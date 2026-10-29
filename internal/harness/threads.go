package harness

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	"bqbot/internal/driver"
	"bqbot/internal/store"
)

// CreateThread opens a conversation with an independent provider session.
func (h *Harness) CreateThread(botID, title string) (store.Thread, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.deleting[botID] {
		return store.Thread{}, errors.New("bot is closing")
	}
	if _, err := h.Bot(botID); err != nil {
		return store.Thread{}, err
	}
	title = strings.TrimSpace(title)
	if title == "" || len(title) > 256 || strings.IndexFunc(title, unicode.IsControl) >= 0 {
		return store.Thread{}, errors.New("invalid thread title")
	}
	thread := store.Thread{ID: store.NewID(), BotID: botID, Title: title, Created: time.Now()}
	if err := h.Store.SaveThread(thread); err != nil {
		return store.Thread{}, err
	}
	thread, err := h.Store.Thread(thread.ID)
	if err != nil {
		return store.Thread{}, err
	}
	h.metadataLocked(Update{BotID: botID, ThreadID: thread.ID, Thread: &thread})
	return thread, nil
}

// SendThread admits work to the bot FIFO, keeping steering within this thread.
func (h *Harness) SendThread(id, text string, attachments []driver.Attachment, mode string) (store.Message, error) {
	if mode != "" && mode != "queue" && mode != "steer" {
		return store.Message{}, errors.New("unknown submit mode")
	}
	if err := validateAttachments(attachments); err != nil {
		return store.Message{}, err
	}
	text = strings.TrimSpace(text)
	if len(text) > 1<<20 || (text == "" && len(attachments) == 0) {
		return store.Message{}, errors.New("empty or oversized message")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	thread, err := h.Store.Thread(id)
	if err != nil {
		return store.Message{}, err
	}
	if thread.Closed || h.closingScopes[id] {
		return store.Message{}, errors.New("thread is closed")
	}
	if thread.RoomID != "" {
		return store.Message{}, errors.New("send to the channel instead of its internal thread")
	}
	bot, err := h.Bot(thread.BotID)
	if err != nil {
		return store.Message{}, err
	}
	if h.closed || h.deleting[bot.ID] {
		return store.Message{}, errors.New("bot is closing")
	}
	d := h.Drivers[bot.Engine]
	if d == nil {
		return store.Message{}, errors.New("engine is unavailable")
	}
	user := store.Message{ID: store.NewID(), Role: "user", Text: text, ThreadID: id, Attachments: append([]driver.Attachment(nil), attachments...), At: time.Now()}
	if current := h.running[bot.ID]; mode == "steer" && current != nil && current.thread.ID == id && current.asker == "" && current.ctx.Err() == nil && current.prepared && current.bot.Engine == bot.Engine && len(attachments) == 0 && len(h.queues[bot.ID]) == 0 {
		if capable, ok := d.(interface{ SupportsSteering() bool }); ok && capable.SupportsSteering() && len(current.steer) < cap(current.steer) {
			if err := h.record(current, user); err != nil {
				return store.Message{}, err
			}
			if err := h.publishLocked(current, Update{BotID: bot.ID, User: clone(user), Busy: true}); err != nil {
				return store.Message{}, err
			}
			current.steer <- text
			return user, nil
		}
	}
	t := newTurn(context.Background(), bot, user, nil)
	t.thread = thread
	if err := h.enqueueLocked(t); err != nil {
		return store.Message{}, err
	}
	return user, nil
}

// StopThread cancels this conversation and any outstanding delegated work.
func (h *Harness) StopThread(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cancelScopeLocked(id)
}
func (h *Harness) cancelScopeLocked(id string) []<-chan struct{} {
	waits := h.cancelJobsLocked(func(j *asyncJob) bool {
		return j.threadID == id || j.roomID == id || j.target.thread.ID == id || j.target.roomID == id
	})
	waits = append(waits, h.cancelRoutesLocked(func(r *roomRoute) bool { return r.room.ID == id })...)
	for _, t := range h.running {
		if t.thread.ID == id || t.roomID == id {
			t.cancel()
			h.cancelApprovalsLocked(t)
			waits = append(waits, t.done)
		}
	}
	for _, q := range h.queues {
		for _, t := range append([]*turn(nil), q...) {
			if t.thread.ID == id || t.roomID == id {
				h.cancelQueuedLocked(t)
			}
		}
	}
	return waits
}
func (h *Harness) CloseThread(id string) error {
	h.mu.Lock()
	thread, err := h.Store.Thread(id)
	if err != nil {
		h.mu.Unlock()
		return err
	}
	if thread.ID == thread.BotID {
		h.mu.Unlock()
		return errors.New("cannot close main conversation")
	}
	if h.closingScopes[id] {
		h.mu.Unlock()
		return errors.New("thread is closing")
	}
	h.closingScopes[id] = true
	waits := h.cancelScopeLocked(id)
	h.mu.Unlock()
	for _, done := range waits {
		<-done
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	err = h.Store.CloseThread(id)
	delete(h.closingScopes, id)
	if err == nil {
		h.metadataLocked(Update{BotID: thread.BotID, ThreadID: id, RoomID: thread.RoomID})
	}
	return err
}
func (h *Harness) CompactThread(id string) (store.Thread, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	thread, err := h.Store.Thread(id)
	if err != nil {
		return thread, err
	}
	if thread.RoomID != "" {
		return thread, errors.New("channel context cannot be compacted as a private thread")
	}
	if h.closingScopes[id] {
		return thread, ErrBusy
	}
	for _, t := range h.running {
		if t.thread.ID == id {
			return thread, ErrBusy
		}
	}
	for _, q := range h.queues {
		for _, t := range q {
			if t.thread.ID == id {
				return thread, ErrBusy
			}
		}
	}
	thread, err = h.Store.CompactThread(id)
	if err == nil {
		h.metadataLocked(Update{BotID: thread.BotID, ThreadID: id, Thread: &thread})
	}
	return thread, err
}

// NotifyMetadata tells connected clients to refresh their conversation lists.
func (h *Harness) NotifyMetadata(botID, threadID, roomID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.metadataLocked(Update{BotID: botID, ThreadID: threadID, RoomID: roomID})
}
func (h *Harness) metadataLocked(u Update) {
	h.sequence++
	u.Sequence = h.sequence
	u.Metadata = true
	h.broadcastLocked(u)
}
