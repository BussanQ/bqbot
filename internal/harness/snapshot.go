package harness

// FullSnapshot reconciles idle and active conversations at one runtime sequence.
// It includes persisted replies completed while a client was disconnected and
// replaces pending approvals, so a stale card can never survive reconnection.
func (h *Harness) FullSnapshot() ([]Update, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	bots, err := h.Store.Bots()
	if err != nil {
		return nil, err
	}
	updates := make([]Update, 0, len(bots))
	for _, b := range bots {
		messages, err := h.Store.Transcript(b.ID)
		if err != nil {
			return nil, err
		}
		u := Update{BotID: b.ID, Bot: &b, Sync: true, Sequence: h.sequence, Messages: messages, Queued: len(h.queues[b.ID])}
		if t := h.running[b.ID]; t != nil {
			u.Busy = true
			u.Answering = t.askerName
			if t.asker == "" {
				u.Reply = clone(t.reply)
				if !t.recorded {
					u.Messages = append(u.Messages, *clone(t.user))
				}
			}
			for _, p := range t.pending {
				req := p.request
				req.Input = append([]byte(nil), req.Input...)
				req.Options = append([]string(nil), req.Options...)
				u.Approvals = append(u.Approvals, req)
			}
		}
		for _, queued := range h.queues[b.ID] {
			if queued.asker == "" {
				u.QueuedMessages = append(u.QueuedMessages, *clone(queued.user))
			}
		}
		updates = append(updates, u)
	}
	return updates, nil
}
