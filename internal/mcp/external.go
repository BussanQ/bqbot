package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// ExternalBackend exposes only tools discovered for the authenticated turn.
type ExternalBackend interface {
	ExternalTools(context.Context) ([]map[string]any, error)
	CallExternal(context.Context, string, json.RawMessage) (map[string]any, error)
}

func (c *HTTPBackend) ExternalTools(ctx context.Context) ([]map[string]any, error) {
	text, err := c.post(ctx, "/external/list", struct{}{})
	if err != nil {
		return nil, err
	}
	var tools []map[string]any
	err = json.Unmarshal([]byte(text), &tools)
	return tools, err
}
func (c *HTTPBackend) CallExternal(ctx context.Context, name string, args json.RawMessage) (map[string]any, error) {
	text, err := c.post(ctx, "/external/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if json.Unmarshal([]byte(text), &result) != nil {
		return nil, errors.New("invalid external MCP result")
	}
	return result, nil
}
func listTools(ctx context.Context, b Backend) (map[string]any, error) {
	base := backendTools(b)
	external, ok := b.(ExternalBackend)
	if !ok || approvalRestricted(b) {
		return base, nil
	}
	tools, err := external.ExternalTools(ctx)
	if err != nil {
		return nil, err
	}
	all := append([]any{}, base["tools"].([]any)...)
	for _, tool := range tools {
		all = append(all, tool)
	}
	return map[string]any{"tools": all}, nil
}
func callExternal(ctx context.Context, b Backend, params json.RawMessage) (map[string]any, bool) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(params, &p) != nil || !strings.HasPrefix(p.Name, "external_") {
		return nil, false
	}
	external, ok := b.(ExternalBackend)
	if !ok {
		return toolResult("", errors.New("external tools unavailable")), true
	}
	result, err := external.CallExternal(ctx, p.Name, p.Arguments)
	if err != nil {
		return toolResult("", err), true
	}
	return result, true
}
