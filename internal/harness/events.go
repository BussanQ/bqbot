package harness

import (
	"errors"
	"sort"
	"sync"
)

// Each subscriber owns a bounded mailbox. A slow consumer disconnects and
// recovers through FullSnapshot instead of retaining an unbounded event backlog.
type subscription struct {
	mu    sync.Mutex
	queue []Update
	wake  chan struct{}
	stop  chan struct{}
	out   chan Update
	once  sync.Once
}

func newSubscription() *subscription {
	s := &subscription{wake: make(chan struct{}, 1), stop: make(chan struct{}), out: make(chan Update)}
	go func() {
		defer close(s.out)
		for {
			s.mu.Lock()
			var u Update
			ok := len(s.queue) > 0
			if ok {
				u = s.queue[0]
				s.queue[0] = Update{}
				s.queue = s.queue[1:]
			}
			s.mu.Unlock()
			if ok {
				select {
				case s.out <- u:
				case <-s.stop:
					return
				}
				continue
			}
			select {
			case <-s.wake:
			case <-s.stop:
				return
			}
		}
	}()
	return s
}
func (s *subscription) push(u Update) {
	s.mu.Lock()
	select {
	case <-s.stop:
		s.mu.Unlock()
		return
	default:
	}
	if len(s.queue) >= 256 {
		s.queue = nil
		s.close()
		s.mu.Unlock()
		return
	}
	s.queue = append(s.queue, copyUpdate(u))
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *subscription) close() { s.once.Do(func() { close(s.stop) }) }

// Subscribe broadcasts every subsequent update independently to the caller.
// The idempotent cancellation function closes the returned channel.
func (h *Harness) Subscribe() (<-chan Update, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := newSubscription()
	if h.closed {
		s.close()
	} else {
		h.subscribers[s] = true
	}
	return s.out, func() { h.mu.Lock(); delete(h.subscribers, s); s.close(); h.mu.Unlock() }
}

// Snapshot returns active turns and their unresolved approval cards.
func (h *Harness) Snapshot() []Update {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []Update
	ids := make([]string, 0, len(h.running))
	for id := range h.running {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		t := h.running[id]
		u := Update{BotID: id, ThreadID: t.thread.ID, RoomID: t.roomID, Busy: true, Reply: clone(t.reply), Queued: len(h.queues[id]), Answering: t.askerName}
		out = append(out, u)
		keys := make([]string, 0, len(t.pending))
		for key := range t.pending {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			a := u
			a.Reply = nil
			req := t.pending[key].request
			a.Approval = &req
			out = append(out, copyUpdate(a))
		}
	}
	return out
}

// publishLocked persists the full thread event before broadcasting its scoped
// conversation update. Persistence failures cancel the turn and are sent
// as explicit Error updates (the failing storage cannot persist that notice).
func (h *Harness) publishLocked(t *turn, u Update) error {
	h.sequence++
	u.Sequence = h.sequence
	u.ThreadID = t.thread.ID
	u.RoomID = t.roomID
	u.Queued = 0
	match := func(other *turn) bool {
		if t.roomID != "" {
			return other.roomID == t.roomID
		}
		return other.thread.ID == t.thread.ID
	}
	for _, q := range h.queues {
		for _, queued := range q {
			if match(queued) {
				u.Queued++
			}
		}
	}
	u.Busy = u.Queued > 0
	for _, active := range h.running {
		if match(active) {
			u.Busy = true
		}
	}
	u.Answering = t.askerName
	if u.Err != nil {
		u.Error = u.Err.Error()
	}
	eventID, asker := t.thread.ID, ""
	if eventID == "" {
		eventID, asker = t.bot.ID, t.asker
	}
	err := h.Store.AppendEvent(eventID, asker, u)
	if err != nil {
		t.err = errors.Join(t.err, err)
		t.cancel()
		u.Err = err
		u.Error = err.Error()
		u.Reply = nil
		u.User = nil
		u.Approval = nil
		u.QueuedUser = nil
		u.Event = nil
	}
	h.broadcastLocked(u)
	return err
}

func (h *Harness) broadcastLocked(u Update) {
	for s := range h.subscribers {
		s.push(u)
	}
	if h.OnUpdate != nil {
		if h.legacy == nil {
			h.legacy = newSubscription()
			s := h.legacy
			callback := h.OnUpdate
			go func() {
				for event := range s.out {
					callback(event)
				}
			}()
		}
		h.legacy.push(u)
	}
}
func copyUpdate(u Update) Update {
	if u.Thread != nil {
		v := *u.Thread
		u.Thread = &v
	}
	if u.Room != nil {
		v := *u.Room
		v.Members = append([]string(nil), v.Members...)
		u.Room = &v
	}
	if u.Bot != nil {
		v := *u.Bot
		if v.Handoffs != nil {
			v.Handoffs = map[string]string{}
			for k, c := range u.Bot.Handoffs {
				v.Handoffs[k] = c
			}
		}
		u.Bot = &v
	}

	if u.QueuedUser != nil {
		u.QueuedUser = clone(*u.QueuedUser)
	}
	if u.Event != nil {
		e := *u.Event
		u.Event = &e
	}
	if u.Decision != nil {
		d := *u.Decision
		u.Decision = &d
	}
	if u.Reply != nil {
		u.Reply = clone(*u.Reply)
	}
	if u.User != nil {
		u.User = clone(*u.User)
	}
	if u.Approval != nil {
		a := *u.Approval
		a.Input = append([]byte(nil), a.Input...)
		a.Options = append([]string(nil), a.Options...)
		u.Approval = &a
	}
	return u
}
