package mcp

import (
	"context"
	"encoding/json"
	"errors"
)

const ToolCoordinate = "coordinate_bots"
const ToolAssignmentStatus = "assignment_status"

// AssignmentRequest names a target by ID or unique name.
type AssignmentRequest struct {
	Bot          string `json:"bot"`
	Task         string `json:"task"`
	Mode         string `json:"mode,omitempty"`
	Reply        string `json:"reply,omitempty"`
	Thread       string `json:"thread,omitempty"`
	DoneWhen     string `json:"done_when,omitempty"`
	AssignmentID string `json:"assignment_id,omitempty"`
}
type CoordinateRequest struct {
	Assignments []AssignmentRequest `json:"assignments"`
}
type CoordinationBackend interface {
	Coordinate(context.Context, CoordinateRequest) (string, error)
	AssignmentStatus(context.Context, string) (string, error)
}

func (c *HTTPBackend) Coordinate(ctx context.Context, req CoordinateRequest) (string, error) {
	return c.post(ctx, "/coordinate_bots", req)
}
func (c *HTTPBackend) AssignmentStatus(ctx context.Context, id string) (string, error) {
	return c.post(ctx, "/assignment_status", map[string]string{"assignment_id": id})
}
func coordinationCall(ctx context.Context, b Backend, params json.RawMessage) (map[string]any, bool) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(params, &p) != nil {
		return nil, false
	}
	if p.Name != ToolCoordinate && p.Name != ToolAssignmentStatus {
		return nil, false
	}
	backend, ok := b.(CoordinationBackend)
	if !ok {
		return toolResult("", errors.New("coordination unavailable")), true
	}
	if p.Name == ToolCoordinate {
		var req CoordinateRequest
		if err := json.Unmarshal(p.Arguments, &req); err != nil {
			return toolResult("", err), true
		}
		text, err := backend.Coordinate(ctx, req)
		return toolResult(text, err), true
	}
	var req struct {
		ID string `json:"assignment_id"`
	}
	if err := json.Unmarshal(p.Arguments, &req); err != nil {
		return toolResult("", err), true
	}
	text, err := backend.AssignmentStatus(ctx, req.ID)
	return toolResult(text, err), true
}
func init() {
	Tools = append(Tools, ToolCoordinate, ToolAssignmentStatus)
	properties := map[string]any{
		"bot":           map[string]any{"type": "string", "description": "Bot ID or unique name from list_bots"},
		"task":          map[string]any{"type": "string"},
		"mode":          map[string]any{"type": "string", "enum": []string{"delegate", "consult", "rework"}},
		"reply":         map[string]any{"type": "string", "enum": []string{"report", "none"}},
		"thread":        map[string]any{"type": "string", "enum": []string{"new"}},
		"done_when":     map[string]any{"type": "string", "description": "Acceptance criterion the target must address in its result"},
		"assignment_id": map[string]any{"type": "string", "description": "Required for rework; a completed assignment you own"},
	}
	list := toolList["tools"].([]any)
	list = append(list, map[string]any{"name": ToolCoordinate, "description": "Delegate 1-4 tasks asynchronously. Finish this turn after acceptance; results resume you automatically. Maximum depth 4; never delegate to an ancestor. consult forbids actions, reply none sends a one-way task, rework revisits a previous assignment. done_when states the acceptance criterion; it is not an automatic test.", "inputSchema": map[string]any{"type": "object", "required": []string{"assignments"}, "properties": map[string]any{"assignments": map[string]any{"type": "array", "minItems": 1, "maxItems": 4, "items": map[string]any{"type": "object", "required": []string{"bot", "task"}, "properties": properties, "additionalProperties": false}}}}, "annotations": map[string]any{"readOnlyHint": false}})
	list = append(list, map[string]any{"name": ToolAssignmentStatus, "description": "Read progress and results of assignments in this conversation. Omit assignment_id to list visible assignments.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"assignment_id": map[string]any{"type": "string"}}}, "annotations": map[string]any{"readOnlyHint": true}})
	toolList["tools"] = list
}
