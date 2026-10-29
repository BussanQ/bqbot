package mcp

import "context"

// ThreadBackend is optional for legacy embedded backends. HTTP grants implement
// discovery and bounded mutation within the caller's conversation scope.
type ThreadBackend interface {
	ListThreads(context.Context) (string, error)
	CloseThread(context.Context, string) (string, error)
	ListRoomTargets(context.Context) (string, error)
}

func (c *HTTPBackend) ListThreads(ctx context.Context) (string, error) {
	return c.post(ctx, "/list_threads", struct{}{})
}
func (c *HTTPBackend) CloseThread(ctx context.Context, id string) (string, error) {
	return c.post(ctx, "/close_thread", map[string]string{"thread_id": id})
}
func (c *HTTPBackend) ListRoomTargets(ctx context.Context) (string, error) {
	return c.post(ctx, "/list_room_targets", struct{}{})
}
func init() {
	list := toolList["tools"].([]any)
	for _, tool := range []struct {
		name, description string
		write             bool
	}{
		{ToolThreads, "List threads visible to this turn's grant, including your delegated threads.", false},
		{ToolCloseThread, "Close an inactive thread visible to this grant. Active threads must be stopped by the person.", true},
		{ToolRoomTargets, "List other members of the current channel eligible for ask_bot. Empty outside a channel.", false},
	} {
		schema := map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
		if tool.write {
			schema["properties"] = map[string]any{"thread_id": map[string]any{"type": "string"}}
			schema["required"] = []string{"thread_id"}
		}
		list = append(list, map[string]any{"name": tool.name, "description": tool.description, "inputSchema": schema, "annotations": map[string]any{"readOnlyHint": !tool.write}})
	}
	toolList["tools"] = list
}
