package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"bqbot/internal/mcp"
	"bqbot/internal/store"
)

type coordinationChain struct {
	count    int
	latest   *turn
	canceled bool
}

// asyncJob remains active until its result has been delivered to the source.
// parent links an incoming assignment to work delegated while answering it.
type asyncJob struct {
	botID, threadID, roomID      string
	target, wake, source, latest *turn
	parent                       *asyncJob
	assignment                   store.Assignment
	pending                      int
	finished                     bool
	canceled                     bool
}

func (h *Harness) dispatch(from *turn, name, question string) (string, error) {
	return h.dispatchThread(from, name, question, "")
}
func (h *Harness) dispatchThread(from *turn, name, question, mode string) (string, error) {
	receipts, err := h.coordinate(from, mcp.CoordinateRequest{Assignments: []mcp.AssignmentRequest{{Bot: name, Task: question, Thread: mode}}})
	if err != nil {
		return "", err
	}
	data, _ := json.Marshal(receipts[0])
	return string(data), nil
}
func (h *Harness) cancelJobsLocked(match func(*asyncJob) bool) []<-chan struct{} {
	selected := map[*asyncJob]bool{}
	for j := range h.jobs {
		for ancestor := j; ancestor != nil; ancestor = ancestor.parent {
			if match(ancestor) {
				for p := j; p != nil; p = p.parent {
					selected[p] = true
				}
				break
			}
		}
	}
	for j := range h.jobs {
		for p := j; p != nil; p = p.parent {
			if selected[p] {
				selected[j] = true
				break
			}
		}
	}
	var waits []<-chan struct{}
	for j := range selected {
		j.canceled = true
		if j.source != nil && j.source.chain != nil {
			j.source.chain.canceled = true
		}
		j.assignment.Status = "canceled"
		j.assignment.Error = "assignment canceled"
		if err := h.Store.SaveAssignment(j.assignment); err != nil {
			h.publishLocked(j.target, Update{BotID: j.target.bot.ID, Err: err})
		}
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
	}
	h.cleanCanceledJobsLocked()
	return waits
}
func (h *Harness) cleanCanceledJobsLocked() {
	for j := range h.jobs {
		if !j.canceled {
			continue
		}
		finished := true
		for _, t := range []*turn{j.target, j.wake} {
			if t == nil {
				continue
			}
			select {
			case <-t.done:
			default:
				finished = false
			}
		}
		if finished {
			delete(h.jobs, j)
		}
	}
}

// completeJobLocked runs after this engine turn is durably recorded. A target
// with outstanding children reports only after the last result continuation.
func (h *Harness) completeJobLocked(t *turn) {
	if delivery := t.delivery; delivery != nil {
		delete(h.jobs, delivery)
		if delivery.parent != nil && delivery.parent.pending > 0 {
			delivery.parent.pending--
		}
	}
	j := t.job
	if j == nil {
		h.cleanCanceledJobsLocked()
		return
	}
	if j.canceled || h.closed {
		h.cleanCanceledJobsLocked()
		return
	}
	j.latest = t
	if j.pending > 0 {
		j.assignment.Status = "waiting"
		if err := h.Store.SaveAssignment(j.assignment); err != nil {
			t.err = errors.Join(t.err, err)
		}
		return
	}
	h.finishAssignmentLocked(j, t)
}
func (h *Harness) finishAssignmentLocked(j *asyncJob, t *turn) {
	if j.finished || j.canceled {
		return
	}
	j.finished = true
	j.assignment.Result = t.result.Text
	j.assignment.Status = "completed"
	if t.err != nil {
		j.assignment.Status = "failed"
		j.assignment.Error = t.err.Error()
		if errors.Is(t.err, context.Canceled) {
			j.assignment.Status = "canceled"
		}
	}
	if err := h.Store.SaveAssignment(j.assignment); err != nil {
		t.err = errors.Join(t.err, err)
		j.assignment.Status = "failed"
		j.assignment.Error = err.Error()
	}
	if j.assignment.Reply == "none" || errors.Is(t.err, context.Canceled) || h.closed || h.deleting[j.botID] || h.closingScopes[j.threadID] || h.closingScopes[j.roomID] {
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
	text := fmt.Sprintf("Handoff completed by %s (assignment %s, thread %s). Continue answering the original request using this result. If the acceptance criterion is not met, coordinate_bots with mode rework can request a correction.\n\n%s", t.bot.Name, j.assignment.ID, t.thread.ID, t.result.Text)
	if t.err != nil {
		text = fmt.Sprintf("Handoff to %s failed (assignment %s). Explain the failure and continue.\n%s", t.bot.Name, j.assignment.ID, t.err)
	}
	wake := newTurn(context.Background(), bot, store.Message{ID: store.NewID(), Role: "user", From: t.bot.Name, Text: text, At: time.Now()}, nil)
	wake.thread = thread
	wake.roomID = j.roomID
	wake.wake = true
	wake.depth = j.source.depth
	wake.ancestors = append([]string{}, j.source.ancestors...)
	wake.chain = j.source.chain
	wake.consult = j.source.consult
	wake.job = j.parent
	wake.delivery = j
	wake.asker = j.source.asker
	wake.askerName = j.source.askerName
	wake.bot.ApprovalMode = j.source.bot.ApprovalMode
	j.wake = wake
	if err = h.enqueueLocked(wake); err != nil {
		delete(h.jobs, j)
		if j.parent != nil && j.parent.pending > 0 {
			j.parent.pending--
			j.parent.assignment.Status = "failed"
			j.parent.assignment.Error = "result delivery failed: " + err.Error()
			_ = h.Store.SaveAssignment(j.parent.assignment)
		}
	}
}
