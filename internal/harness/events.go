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
		u := Update{BotID: id, Busy: true, Reply: clone(t.reply), Queued: len(h.queues[id]), Answering: t.askerName}
		if t.asker != "" {
			u.Reply = nil
		}
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

// publishLocked persists the full thread event first, then publishes a main
// conversation projection. Persistence failures cancel the turn and are sent
// as explicit Error updates (the failing storage cannot persist that notice).
func (h *Harness) publishLocked(t *turn, u Update) error {
	h.sequence++
	u.Sequence = h.sequence
	u.Queued = len(h.queues[t.bot.ID])
	u.Answering = t.askerName
	if u.Err != nil {
		u.Error = u.Err.Error()
	}
	err := h.Store.AppendEvent(t.bot.ID, t.asker, u)
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
	if t.asker != "" {
		u.Reply = nil
		u.User = nil
		u.QueuedUser = nil
		u.Event = nil
	}
	if active := h.running[t.bot.ID]; active != nil && active != t {
		u.Answering = active.askerName
	}
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
	return err
}
func copyUpdate(u Update) Update {
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
