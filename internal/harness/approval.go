package harness

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"bqbot/internal/driver"
	"bqbot/internal/store"
)

type pendingApproval struct {
	request driver.ApprovalRequest
	answer  chan driver.ApprovalDecision
}

// autoAllowed deliberately recognizes exact local edit tools only. Shell,
// arbitrary MCP names, and lookalike tool names always require Ask approval.
func autoAllowed(mode string, req driver.ApprovalRequest) bool {
	if req.Kind != "tool" {
		return false
	}
	if mode == "full-auto" {
		return true
	}
	if mode != "auto-edit" {
		return false
	}
	switch strings.ToLower(req.Tool) {
	case "edit", "write", "multiedit", "apply_patch":
		return true
	}
	return false
}
func (h *Harness) approval(ctx context.Context, t *turn, req driver.ApprovalRequest) (driver.ApprovalDecision, error) {
	h.mu.Lock()
	if t.ctx.Err() != nil || ctx.Err() != nil || h.running[t.bot.ID] != t {
		h.mu.Unlock()
		return driver.ApprovalDecision{}, context.Canceled
	}
	if req.Kind != "tool" && req.Kind != "question" {
		h.mu.Unlock()
		return driver.ApprovalDecision{}, fmt.Errorf("invalid approval kind %q", req.Kind)
	}
	if req.ID == "" {
		req.ID = store.NewID()
	}
	if t.approvalIDs[req.ID] {
		h.mu.Unlock()
		return driver.ApprovalDecision{}, errors.New("duplicate approval id")
	}
	t.approvalIDs[req.ID] = true
	req.Input = append([]byte(nil), req.Input...)
	req.Options = append([]string(nil), req.Options...)
	t.output = true // never replay a turn after it requested permission
	if err := h.publishLocked(t, Update{BotID: t.bot.ID, Busy: true, Approval: &req}); err != nil {
		h.mu.Unlock()
		return driver.ApprovalDecision{}, err
	}
	if autoAllowed(t.bot.ApprovalMode, req) {
		err := h.publishLocked(t, Update{BotID: t.bot.ID, Busy: true, ResolvedApproval: req.ID, Decision: &driver.ApprovalDecision{Outcome: "allow"}})
		h.mu.Unlock()
		return driver.ApprovalDecision{Outcome: "allow"}, err
	}
	p := &pendingApproval{request: req, answer: make(chan driver.ApprovalDecision, 1)}
	t.pending[req.ID] = p
	h.mu.Unlock()
	select {
	case decision := <-p.answer:
		if err := t.contextErr(); err != nil {
			return driver.ApprovalDecision{}, err
		}
		if err := ctx.Err(); err != nil {
			return driver.ApprovalDecision{}, err
		}
		return decision, nil
	case <-ctx.Done():
	case <-t.ctx.Done():
	}
	h.mu.Lock()
	if t.pending[req.ID] == p {
		delete(t.pending, req.ID)
		h.publishLocked(t, Update{BotID: t.bot.ID, Busy: true, ResolvedApproval: req.ID})
	}
	h.mu.Unlock()
	if ctx.Err() != nil {
		return driver.ApprovalDecision{}, ctx.Err()
	}
	return driver.ApprovalDecision{}, context.Canceled
}

// Approve resolves a currently pending, single-use request for this bot.
func (h *Harness) Approve(botID, id string, decision driver.ApprovalDecision) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	t := h.running[botID]
	if t == nil || t.ctx.Err() != nil {
		return errors.New("turn is no longer active")
	}
	p := t.pending[id]
	if p == nil {
		return errors.New("approval is no longer pending")
	}
	switch p.request.Kind {
	case "tool":
		if decision.Outcome != "allow" && decision.Outcome != "deny" {
			return errors.New("tool approval requires allow or deny")
		}
	case "question":
		if decision.Outcome != "answer" && decision.Outcome != "deny" {
			return errors.New("question requires answer or deny")
		}
	}
	if err := h.publishLocked(t, Update{BotID: botID, Busy: true, ResolvedApproval: id, Decision: &decision}); err != nil {
		return err
	}
	delete(t.pending, id)
	p.answer <- decision
	return nil
}
func (h *Harness) cancelApprovalsLocked(t *turn) {
	for id := range t.pending {
		delete(t.pending, id)
		h.publishLocked(t, Update{BotID: t.bot.ID, Busy: true, ResolvedApproval: id})
	}
}
