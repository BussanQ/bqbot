package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"bqbot/internal/mcp"
	"bqbot/internal/store"
)

type assignmentReceipt struct {
	Status    string `json:"status"`
	ID        string `json:"assignment_id,omitempty"`
	ThreadID  string `json:"thread_id,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	Target    string `json:"target"`
	Error     string `json:"error,omitempty"`
}
type plannedAssignment struct {
	request  mcp.AssignmentRequest
	bot      store.Bot
	previous *store.Assignment
}

func (h *Harness) coordinate(from *turn, req mcp.CoordinateRequest) ([]assignmentReceipt, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if from.ctx.Err() != nil || h.running[from.bot.ID] != from {
		return nil, context.Canceled
	}
	if from.depth >= 4 {
		return nil, errors.New("coordination depth limit is four")
	}
	if len(req.Assignments) < 1 || len(req.Assignments) > 4 {
		return nil, errors.New("coordinate_bots requires 1 to 4 assignments")
	}
	if from.chain == nil {
		from.chain = &coordinationChain{}
	}
	if from.chain.count+len(req.Assignments) > 32 {
		return nil, errors.New("coordination chain limit of 32 assignments reached")
	}
	if from.dispatched+len(req.Assignments) > 8 {
		return nil, errors.New("at most eight assignments per turn")
	}
	existing, err := h.Store.Assignments()
	if err != nil {
		return nil, err
	}
	plans := make([]plannedAssignment, 0, len(req.Assignments))
	seen := map[string]bool{}
	for _, request := range req.Assignments {
		request.Task = strings.TrimSpace(request.Task)
		if request.Task == "" || len(request.Task) > 1<<20 || len(request.DoneWhen) > 8192 {
			return nil, errors.New("empty or oversized task/acceptance criterion")
		}
		if request.Mode == "" {
			request.Mode = "delegate"
		}
		if request.Reply == "" {
			request.Reply = "report"
		}
		if request.Mode != "delegate" && request.Mode != "consult" && request.Mode != "rework" {
			return nil, errors.New("mode must be delegate, consult or rework")
		}
		if request.Reply != "report" && request.Reply != "none" {
			return nil, errors.New("reply must be report or none")
		}
		if request.Thread != "" && request.Thread != "new" {
			return nil, errors.New("thread must be new or omitted")
		}
		if request.Thread != "" && from.roomID != "" {
			return nil, errors.New("channel handoffs stay in the current channel")
		}
		bot, err := h.botNamed(request.Bot)
		if err != nil {
			return nil, err
		}
		if bot.ID == from.bot.ID || slices.Contains(from.ancestors, bot.ID) {
			return nil, errors.New("cannot delegate to yourself or an ancestor")
		}
		if seen[bot.ID] {
			return nil, errors.New("each target must be unique within a batch")
		}
		seen[bot.ID] = true
		if h.deleting[bot.ID] || h.Drivers[bot.Engine] == nil {
			return nil, errors.New("target is unavailable")
		}
		if from.roomID != "" {
			room, err := h.Store.Room(from.roomID)
			if err != nil {
				return nil, err
			}
			if !member(room, bot.ID) {
				return nil, errors.New("target is not a member of this channel")
			}
		}
		plan := plannedAssignment{request: request, bot: bot}
		if request.Mode == "rework" {
			if request.Thread != "" {
				return nil, errors.New("rework continues the original assignment thread")
			}
			for _, old := range existing {
				if old.ID == request.AssignmentID && old.SourceBotID == from.bot.ID && old.SourceThreadID == from.thread.ID && old.RoomID == from.roomID && old.TargetBotID == bot.ID {
					copy := old
					plan.previous = &copy
					break
				}
			}
			if plan.previous == nil {
				return nil, errors.New("rework requires a visible assignment for this target")
			}
			if plan.previous.Status != "completed" && plan.previous.Status != "failed" {
				return nil, errors.New("rework requires a finished assignment")
			}
			if meta, err := h.Store.Thread(plan.previous.TargetThreadID); err != nil || meta.Closed {
				return nil, errors.New("rework thread is closed or missing")
			}
		} else if request.AssignmentID != "" {
			return nil, errors.New("assignment_id is only valid for rework")
		}
		plans = append(plans, plan)
	}
	receipts := make([]assignmentReceipt, 0, len(plans))
	for _, plan := range plans {
		receipt, err := h.dispatchAssignmentLocked(from, plan)
		if err != nil {
			receipt = assignmentReceipt{Status: "failed", Target: plan.bot.Name, Error: err.Error()}
		}
		receipts = append(receipts, receipt)
	}
	return receipts, nil
}
func (h *Harness) dispatchAssignmentLocked(from *turn, plan plannedAssignment) (assignmentReceipt, error) {
	req, target := plan.request, plan.bot
	var thread store.Thread
	var err error
	switch {
	case plan.previous != nil:
		thread, err = h.Store.Thread(plan.previous.TargetThreadID)
	case from.roomID != "":
		thread, err = h.Store.RoomThread(from.roomID, target.ID)
	case req.Thread == "new":
		thread = store.Thread{ID: store.NewID(), BotID: target.ID, AskerID: from.bot.ID, Title: "From " + from.bot.Name}
		if err = h.Store.SaveThread(thread); err == nil {
			thread, err = h.Store.Thread(thread.ID)
		}
	default:
		thread, err = h.Store.HandoffThread(target.ID, from.bot.ID)
	}
	if err != nil {
		return assignmentReceipt{}, err
	}
	text := req.Task
	if plan.previous != nil {
		text = "Rework assignment " + plan.previous.ID + ". Original task:\n" + plan.previous.Task + "\nPrevious result:\n" + plan.previous.Result + "\nCorrection requested:\n" + text
	}
	if req.DoneWhen != "" {
		text += "\n\nAcceptance criterion (explain whether it is met):\n" + req.DoneWhen
	}
	t := newTurn(context.Background(), target, store.Message{ID: store.NewID(), Role: "user", Text: text, At: time.Now()}, from)
	t.thread = thread
	t.roomID = from.roomID
	t.consult = from.consult || req.Mode == "consult" || plan.previous != nil && plan.previous.Consult
	if t.consult {
		t.user.Text = "CONSULT ONLY: analyze and answer; do not change files, execute commands or take external actions.\n\n" + t.user.Text
	}
	if t.roomID != "" {
		t.user.BotID = from.bot.ID
	}
	a := store.Assignment{ID: store.NewID(), SourceBotID: from.bot.ID, SourceThreadID: from.thread.ID, TargetBotID: target.ID, TargetThreadID: thread.ID, RoomID: from.roomID, Task: req.Task, Mode: req.Mode, Reply: req.Reply, DoneWhen: req.DoneWhen, Depth: t.depth, Status: "queued", CreatedAt: time.Now(), RootBotID: from.bot.ID, RootThreadID: from.thread.ID}
	if t.consult {
		a.Consult = true
	}
	if from.job != nil {
		a.ParentID = from.job.assignment.ID
		a.RootBotID = from.job.assignment.RootBotID
		a.RootThreadID = from.job.assignment.RootThreadID
	}
	if plan.previous != nil {
		a.ReworkOf = plan.previous.ID
	}
	j := &asyncJob{botID: from.bot.ID, threadID: from.thread.ID, roomID: from.roomID, target: t, source: from, parent: from.job, assignment: a}
	t.job = j
	if err = h.Store.SaveAssignment(a); err != nil {
		t.cancel()
		return assignmentReceipt{}, err
	}
	if err = h.enqueueLocked(t); err != nil {
		a.Status = "failed"
		a.Error = err.Error()
		return assignmentReceipt{}, errors.Join(err, h.Store.SaveAssignment(a))
	}
	h.jobs[j] = true
	if j.parent != nil && req.Reply != "none" {
		j.parent.pending++
	}
	from.chain.count++
	from.dispatched++
	from.reply.Tools = append(from.reply.Tools, "已转交给 "+target.Name)
	from.output = true
	if err = h.publishLocked(from, Update{BotID: from.bot.ID, Reply: clone(from.reply), Busy: true}); err != nil {
		from.err = errors.Join(from.err, err)
	}
	h.metadataLocked(Update{BotID: target.ID, ThreadID: thread.ID, RoomID: from.roomID, Thread: &thread})
	return assignmentReceipt{Status: "accepted", ID: a.ID, ThreadID: thread.ID, MessageID: t.user.ID, Target: target.Name}, nil
}
func (h *Harness) assignmentStatus(from *turn, id string) (string, error) {
	values, err := h.Store.Assignments()
	if err != nil {
		return "", err
	}
	visible := []store.Assignment{}
	for _, v := range values {
		allowed := v.RoomID == from.roomID && ((v.SourceBotID == from.bot.ID && v.SourceThreadID == from.thread.ID) || (v.TargetBotID == from.bot.ID && v.TargetThreadID == from.thread.ID) || (v.RootBotID == from.bot.ID && v.RootThreadID == from.thread.ID))
		if allowed && (id == "" || id == v.ID) {
			visible = append(visible, v)
		}
	}
	if id != "" && len(visible) == 0 {
		return "", errors.New("assignment is not visible in this conversation")
	}
	data, err := json.Marshal(visible)
	return string(data), err
}
func (hd *handoff) serveCoordinate(w http.ResponseWriter, r *http.Request) {
	from := hd.caller(r, false)
	if from == nil {
		reply(w, 401, "", errors.New("unauthorized"))
		return
	}
	var req mcp.CoordinateRequest
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(&req); err != nil {
		reply(w, 400, "", errors.New("invalid coordination request"))
		return
	}
	receipts, err := hd.h.coordinate(from, req)
	data, _ := json.Marshal(map[string]any{"assignments": receipts})
	reply(w, 200, string(data), err)
}
func (hd *handoff) serveAssignmentStatus(w http.ResponseWriter, r *http.Request) {
	from := hd.caller(r, false)
	if from == nil {
		reply(w, 401, "", errors.New("unauthorized"))
		return
	}
	var req struct {
		ID string `json:"assignment_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		reply(w, 400, "", fmt.Errorf("invalid status request"))
		return
	}
	text, err := hd.h.assignmentStatus(from, req.ID)
	reply(w, 200, text, err)
}
