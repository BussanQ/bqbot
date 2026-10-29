package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ApprovalServer is separate from the depth-limited handoff server.
const ApprovalServer = "bqbot_approval"
const ToolApprove = "approve_tool"
const ApprovalToolName = "mcp__" + ApprovalServer + "__" + ToolApprove

// ApprovalRequest and ApprovalDecision mirror the driver's wire format without
// importing driver (which depends on this package).
type ApprovalRequest struct {
	ID       string          `json:"id"`
	Kind     string          `json:"kind"`
	Tool     string          `json:"tool,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
	Question string          `json:"question,omitempty"`
	Options  []string        `json:"options,omitempty"`
}
type ApprovalDecision struct {
	Outcome string `json:"outcome"`
	Answer  string `json:"answer,omitempty"`
}

// ApprovalBackend answers permission prompts independently of bot handoffs.
type ApprovalBackend interface {
	Approve(context.Context, ApprovalRequest) (ApprovalDecision, error)
}

// ApprovalRestricted marks an HTTP backend that must expose only approval.
func (c *HTTPBackend) ApprovalRestricted() bool { return c.ApprovalOnly }
func approvalRestricted(b Backend) bool {
	scoped, ok := b.(interface{ ApprovalRestricted() bool })
	return ok && scoped.ApprovalRestricted()
}
func backendName(b Backend) string {
	if approvalRestricted(b) {
		return ApprovalServer
	}
	return Server
}
func backendTools(b Backend) map[string]any {
	if !approvalRestricted(b) {
		if _, ok := b.(ThreadBackend); !ok {
			return map[string]any{"tools": toolList["tools"].([]any)[:2]}
		}
		return toolList
	}
	return map[string]any{"tools": []any{map[string]any{
		"name":        ToolApprove,
		"description": "Ask the person to approve a Claude tool call or answer AskUserQuestion. Permission is denied when approval is unavailable.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"tool_name":   map[string]any{"type": "string"},
			"input":       map[string]any{"type": "object", "additionalProperties": true},
			"tool_use_id": map[string]any{"type": "string"},
		}, "required": []string{"tool_name", "input"}},
	}}}
}

func permissionResult(allow bool, input any, reason string) map[string]any {
	result := map[string]any{"behavior": "deny", "message": reason}
	if allow {
		result = map[string]any{"behavior": "allow", "updatedInput": input}
	}
	encoded, _ := json.Marshal(result)
	return toolResult(string(encoded), nil)
}

func callApproval(ctx context.Context, b Backend, params json.RawMessage) map[string]any {
	deny := func(reason string) map[string]any { return permissionResult(false, nil, reason) }
	if ctx.Err() != nil {
		return deny("Permission request cancelled")
	}
	var p struct {
		Name      string `json:"name"`
		Arguments struct {
			Tool  string          `json:"tool_name"`
			Input json.RawMessage `json:"input"`
			ID    string          `json:"tool_use_id"`
		} `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return deny("Invalid permission request")
	}
	if p.Name != ToolApprove {
		return toolResult("", fmt.Errorf("unknown tool %q", p.Name))
	}
	var input map[string]any
	if p.Arguments.Tool == "" || json.Unmarshal(p.Arguments.Input, &input) != nil || input == nil {
		return deny("A tool name and object input are required")
	}
	backend, ok := b.(ApprovalBackend)
	if !ok {
		return deny("Approval is unavailable")
	}
	id := p.Arguments.ID
	if id == "" {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return deny("Cannot create approval request")
		}
		id = hex.EncodeToString(nonce[:])
	}
	req := ApprovalRequest{ID: id, Kind: "tool", Tool: p.Arguments.Tool, Input: p.Arguments.Input}
	if p.Arguments.Tool == "AskUserQuestion" {
		var questions struct {
			Questions []struct {
				Question string `json:"question"`
				Options  []struct {
					Label string `json:"label"`
				} `json:"options"`
			} `json:"questions"`
		}
		if json.Unmarshal(p.Arguments.Input, &questions) != nil || len(questions.Questions) == 0 {
			return deny("Invalid question")
		}
		answers := map[string]string{}
		for i, q := range questions.Questions {
			if strings.TrimSpace(q.Question) == "" {
				return deny("Empty question")
			}
			req.ID, req.Kind, req.Question = fmt.Sprintf("%s:%d", id, i), "question", q.Question
			req.Options = nil
			for _, o := range q.Options {
				req.Options = append(req.Options, o.Label)
			}
			decision, err := backend.Approve(ctx, req)
			if err != nil || ctx.Err() != nil || decision.Outcome != "answer" {
				return deny("Question was not answered")
			}
			answers[q.Question] = decision.Answer
		}
		input["answers"] = answers
		return permissionResult(true, input, "")
	}
	decision, err := backend.Approve(ctx, req)
	if err != nil {
		return deny("Approval is unavailable: " + err.Error())
	}
	if ctx.Err() != nil || decision.Outcome != "allow" {
		return deny("Permission denied")
	}
	return permissionResult(true, input, "")
}

// Approve posts the wire request to the grant's /approve route. Unlike handoff
// endpoints, the response is a direct decision JSON object, not a Reply wrapper.
func (c *HTTPBackend) Approve(ctx context.Context, request ApprovalRequest) (ApprovalDecision, error) {
	var decision ApprovalDecision
	body, err := json.Marshal(request)
	if err != nil {
		return decision, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.URL, "/")+"/approve", bytes.NewReader(body))
	if err != nil {
		return decision, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	client := c.Client
	if client == nil {
		client = &http.Client{Transport: &http.Transport{Proxy: nil}}
	}
	resp, err := client.Do(req)
	if err != nil {
		return decision, fmt.Errorf("approval endpoint: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return decision, fmt.Errorf("approval endpoint answered %s", resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&decision); err != nil {
		return ApprovalDecision{}, err
	}
	switch decision.Outcome {
	case "allow", "deny", "answer":
		return decision, nil
	default:
		return ApprovalDecision{}, errors.New("invalid approval decision")
	}
}
