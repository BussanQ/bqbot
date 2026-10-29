package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"bqbot/internal/store"
)

// asyncJob outlives the delegating engine process, but never an explicit stop,
// close or delete. Only its one completion turn is allowed to resume the asker.
type asyncJob struct {
	botID, threadID, roomID string
	target                  *turn
	wake                    *turn
	canceled                bool
}

func (h *Harness) dispatch(from *turn, name, question string) (string, error) {
	return h.dispatchThread(from, name, question, "")
}

func (h *Harness) dispatchThread(from *turn, name, question, mode string) (string, error) {
	if mode != "" && mode != "new" {
		return "", errors.New("thread must be omitted or new")
	}
	if mode == "new" && from.roomID != "" {
		return "", errors.New("channel handoffs stay in the current channel")
	}
	question = strings.TrimSpace(question)
	if question == "" || len(question) > 1<<20 {
		return "", errors.New("empty or oversized question")
	}
	target, err := h.botNamed(name)
	if err != nil {
		return "", err
	}
	if target.ID == from.bot.ID {
		return "", errors.New("you cannot ask yourself")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if from.depth > 0 || from.wake {
		return "", errors.New("completion and delegated turns cannot delegate")
	}
	if from.ctx.Err() != nil || h.running[from.bot.ID] != from {
		return "", context.Canceled
	}
	if from.dispatched >= 8 {
		return "", errors.New("at most eight handoffs per turn")
	}
	if from.roomID != "" {
		room, e := h.Store.Room(from.roomID)
		if e != nil {
			return "", e
		}
		if !member(room, target.ID) {
			return "", errors.New("target is not a member of this channel")
		}
	}
	var thread store.Thread
	if from.roomID != "" {
		thread, err = h.Store.RoomThread(from.roomID, target.ID)
	} else if mode == "new" {
		thread = store.Thread{ID: store.NewID(), BotID: target.ID, AskerID: from.bot.ID, Title: "From " + from.bot.Name}
		if err = h.Store.SaveThread(thread); err == nil {
			thread, err = h.Store.Thread(thread.ID)
		}
	} else {
		thread, err = h.Store.HandoffThread(target.ID, from.bot.ID)
	}
	if err != nil {
		return "", err
	}
	t := newTurn(context.Background(), target, store.Message{ID: store.NewID(), Role: "user", Text: question, At: time.Now()}, from)
	t.thread = thread
	t.roomID = from.roomID
	if t.roomID != "" {
		t.user.BotID = from.bot.ID
	}
	j := &asyncJob{botID: from.bot.ID, threadID: from.thread.ID, roomID: from.roomID, target: t}
	t.job = j
	if err = h.enqueueLocked(t); err != nil {
		return "", err
	}
	h.jobs[j] = true
	from.dispatched++
	from.reply.Tools = append(from.reply.Tools, "已转交给 "+target.Name)
	from.output = true
	h.publishLocked(from, Update{BotID: from.bot.ID, Reply: clone(from.reply), Busy: true})
	h.metadataLocked(Update{BotID: target.ID, ThreadID: thread.ID, RoomID: from.roomID, Thread: &thread})
	receipt, _ := json.Marshal(map[string]string{"status": "accepted", "thread_id": thread.ID, "message_id": t.user.ID, "target": target.Name})
	return string(receipt), nil
}
func (h *Harness) cancelJobsLocked(match func(*asyncJob) bool) []<-chan struct{} {
	var waits []<-chan struct{}
	for j := range h.jobs {
		if !match(j) {
			continue
		}
		j.canceled = true
		for _, t := range []*turn{j.target, j.wake} {
			if t == nil {
				continue
			}
			waits = append(waits, t.done)
			t.cancel()
			if h.running[t.bot.ID] == t {
				h.cancelApprovalsLocked(t)
			} else {
				h.cancelQueuedLocked(t)
			}
		}
		// Keep canceled jobs discoverable until all executing participants have
		// acknowledged cancellation. A later close/delete must still await them.
		finished := true
		for _, t := range []*turn{j.target, j.wake} {
			if t != nil {
				select {
				case <-t.done:
				default:
					finished = false
				}
			}
		}
		if finished {
			delete(h.jobs, j)
		}
	}
	return waits
}

// Called under mu, after the target's result is durable and done is closed.
func (h *Harness) completeJobLocked(t *turn) {
	j := t.job
	if j == nil {
		return
	}
	if t.wake || j.canceled || h.closed {
		delete(h.jobs, j)
		return
	}
	if h.deleting[j.botID] || h.closingScopes[j.threadID] || h.closingScopes[j.roomID] {
		delete(h.jobs, j)
		return
	}
	if errors.Is(t.err, context.Canceled) {
		delete(h.jobs, j)
		return
	}
	bot, err := h.Bot(j.botID)
	if err != nil {
		delete(h.jobs, j)
		return
	}
	thread, err := h.Store.Thread(j.threadID)
	if err != nil || thread.Closed {
		delete(h.jobs, j)
		return
	}
	text := fmt.Sprintf("Handoff completed by %s (thread %s). Continue answering the original request using this result. Do not delegate again.\n\n%s", t.bot.Name, t.thread.ID, t.result.Text)
	if t.err != nil {
		text = fmt.Sprintf("Handoff to %s failed. Explain the failure and continue without delegating again.\n%s", t.bot.Name, t.err)
	}
	wake := newTurn(context.Background(), bot, store.Message{ID: store.NewID(), Role: "user", From: t.bot.Name, Text: text, At: time.Now()}, nil)
	wake.thread = thread
	wake.roomID = j.roomID
	wake.wake = true
	wake.depth = 1
	wake.job = j
	// Inherit the original permission level, rather than upgrading during a job.
	wake.bot.ApprovalMode = t.bot.ApprovalMode
	j.wake = wake
	if err = h.enqueueLocked(wake); err != nil {
		delete(h.jobs, j)
	}
}
