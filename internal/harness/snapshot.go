package harness

import "bqbot/internal/store"

// FullSnapshot reconciles all private, delegated and channel conversations at
// one runtime sequence, including idle persisted replies and pending approvals.
func (h *Harness) FullSnapshot() ([]Update, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	bots, err := h.Store.Bots()
	if err != nil {
		return nil, err
	}
	var updates []Update
	for _, b := range bots {
		threads, e := h.Store.Threads(b.ID)
		if e != nil {
			return nil, e
		}
		for _, thread := range threads {
			if thread.RoomID != "" {
				continue
			}
			messages, e := h.Store.ThreadTranscript(thread.ID)
			if e != nil {
				return nil, e
			}
			botCopy, threadCopy := b, thread
			u := Update{BotID: b.ID, Bot: &botCopy, ThreadID: thread.ID, Thread: &threadCopy, Sync: true, Sequence: h.sequence, Messages: messages}
			h.snapshotWorkLocked(&u, func(t *turn) bool { return t.thread.ID == thread.ID })
			updates = append(updates, u)
		}
	}
	rooms, err := h.Store.Rooms()
	if err != nil {
		return nil, err
	}
	for _, room := range rooms {
		messages, e := h.Store.RoomTranscript(room.ID)
		if e != nil {
			return nil, e
		}
		roomCopy := room
		allMessages := append([]store.Message(nil), messages...)
		var queued []store.Message
		busy := false
		// Every room snapshot projection carries all partial replies, so consumers
		// replacing a room transcript cannot lose another concurrent speaker.
		for _, b := range bots {
			if t := h.running[b.ID]; t != nil && t.roomID == room.ID {
				busy = true
				allMessages = append(allMessages, *clone(t.reply))
				if !t.recorded && !t.roomUserRecorded {
					allMessages = append(allMessages, *clone(t.user))
				}
			}
		}
		for _, b := range bots {
			for _, t := range h.queues[b.ID] {
				if t.roomID == room.ID {
					queued = append(queued, *clone(t.user))
				}
			}
		}
		u := Update{RoomID: room.ID, Room: &roomCopy, Sync: true, Sequence: h.sequence, Messages: allMessages, Busy: busy, Queued: len(queued), QueuedMessages: queued}
		// A channel can have several active speakers. Each active reply gets its own
		// update so one speaker cannot overwrite another during reconciliation.
		updates = append(updates, u)
		for _, b := range bots {
			v := Update{BotID: b.ID, RoomID: room.ID, Room: &roomCopy, Messages: messages, Sync: true, Sequence: h.sequence}
			h.snapshotWorkLocked(&v, func(t *turn) bool { return t.bot.ID == b.ID && t.roomID == room.ID })
			if v.Busy || v.Queued > 0 {
				v.Messages = allMessages
				v.QueuedMessages = queued
				v.Queued = len(queued)
				v.Busy = busy
				updates = append(updates, v)
			}
		}
	}
	return updates, nil
}
func (h *Harness) snapshotWorkLocked(u *Update, match func(*turn) bool) {
	for _, t := range h.running {
		if !match(t) {
			continue
		}
		u.ThreadID = t.thread.ID
		u.Busy = true
		u.Answering = t.askerName
		u.Reply = clone(t.reply)
		if !t.recorded && !t.roomUserRecorded {
			u.Messages = append(u.Messages, *clone(t.user))
		}
		for _, p := range t.pending {
			req := p.request
			req.Input = append([]byte(nil), req.Input...)
			req.Options = append([]string(nil), req.Options...)
			u.Approvals = append(u.Approvals, req)
		}
	}
	for _, q := range h.queues {
		for _, t := range q {
			if match(t) {
				u.Queued++
				u.QueuedMessages = append(u.QueuedMessages, *clone(t.user))
			}
		}
	}
}
