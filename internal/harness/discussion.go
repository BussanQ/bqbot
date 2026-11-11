package harness

import (
	"context"
	"slices"

	"bqbot/internal/driver"
	"bqbot/internal/store"
)

const roomPass = "[[PASS]]"

// One user message starts one round. Members who already read the message
// may revisit it once if a peer subsequently publishes a reply.
type roomRound struct {
	hasReply     bool
	awaitingPeer []*turn
}

func (t *turn) acceptsRoomTarget(id string) bool {
	return len(t.mentionedMembers) == 0 || slices.Contains(t.mentionedMembers, id)
}

func discussionHistory(t *turn, history []store.Message, in *driver.TurnInput) []store.Message {
	in.RoomReview = t.round.hasReply
	if in.RoomReview {
		in.Text = "Read the shared conversation above, including the replies to this message. Decide whether you have anything to add in the language of the conversation."
		if t.revisit {
			in.Text = "You have already considered this message. Read the new replies. Follow up only if they call for a response from you."
		}
		in.Attachments = nil // the triggering message keeps its attachments in history
	}
	// A newer user submission has its own round. Its members will receive
	// replies that were still being generated when that message arrived.
	trigger := slices.IndexFunc(history, func(m store.Message) bool { return m.ID == t.user.ID })
	if trigger < 0 {
		return history
	}
	tail := slices.DeleteFunc(history[trigger+1:], func(m store.Message) bool {
		return m.Role == "user" && m.From == ""
	})
	return history[:trigger+1+len(tail)]
}

func (h *Harness) removeRoomTurnLocked(t *turn) {
	if t.round == nil {
		return
	}
	q := h.roomTurns[t.roomID]
	if i := slices.Index(q, t); i >= 0 {
		q = slices.Delete(q, i, i+1)
	}
	if len(q) == 0 {
		delete(h.roomTurns, t.roomID)
	} else {
		h.roomTurns[t.roomID] = q
	}
}

func (h *Harness) startRoomHeadsLocked() {
	for _, q := range h.roomTurns {
		h.nextLocked(q[0].bot.ID)
	}
}

func (h *Harness) advanceDiscussionLocked(t *turn) {
	if t.round == nil {
		return
	}
	defer h.removeRoomTurnLocked(t)
	if t.err != nil || h.closed || h.closingScopes[t.roomID] {
		return
	}
	r := t.round
	var readers []*turn
	if t.reply.Text != "" {
		r.hasReply = true
		readers, r.awaitingPeer = r.awaitingPeer, nil
	}
	if !t.revisit {
		r.awaitingPeer = append(r.awaitingPeer, t)
	}
	q := h.roomTurns[t.roomID]
	if len(readers) == 0 || q[len(q)-1].round != r {
		// A newer round will read the updated transcript.
		return
	}
	room, err := h.Store.Room(t.roomID)
	if err != nil {
		h.publishLocked(t, Update{BotID: t.bot.ID, Err: err})
		return
	}
	for _, prior := range readers {
		if !member(room, prior.bot.ID) || h.deleting[prior.bot.ID] {
			continue
		}
		next := newTurn(context.Background(), prior.bot, t.user, nil)
		next.roomID, next.thread, next.round = t.roomID, prior.thread, r
		next.roomUserRecorded, next.revisit = true, true
		next.mentionedMembers = prior.mentionedMembers
		if err := h.enqueueLocked(next); err != nil {
			h.publishLocked(next, Update{BotID: next.bot.ID, Err: err})
		}
	}
}

// A member deciding to pass has no chat bubble, including during reconnect.
func (t *turn) visibleReply() *store.Message {
	if t.round != nil && t.reply.Text == "" && len(t.reply.Tools) == 0 {
		return nil
	}
	return clone(t.reply)
}
