// Package control serves a deliberately restricted MCP control plane.
package control

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"bqbot/internal/service"
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// Serve exposes list/send/status/transcript only. It has no generic HTTP tool,
// approvals, deletions, settings, credentials, or arbitrary local file access.
func Serve(ctx context.Context, in io.Reader, out io.Writer, api *service.Client) error {
	if api == nil {
		return errors.New("service client is required")
	}
	if closer, ok := in.(io.ReadCloser); ok {
		stop := context.AfterFunc(ctx, func() { _ = closer.Close() })
		defer stop()
	}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	encoder := json.NewEncoder(out)
	respond := func(id json.RawMessage, result any, code int, message string) error {
		v := map[string]any{"jsonrpc": "2.0", "id": id}
		if code != 0 {
			v["error"] = map[string]any{"code": code, "message": message}
		} else {
			v["result"] = result
		}
		return encoder.Encode(v)
	}
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var req request
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			if err := respond(nil, nil, -32700, "parse error"); err != nil {
				return err
			}
			continue
		}
		if len(req.ID) == 0 {
			continue
		}
		if req.JSONRPC != "2.0" {
			if err := respond(req.ID, nil, -32600, "invalid request"); err != nil {
				return err
			}
			continue
		}
		var result any
		var code int
		var message string
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "bqbot-control", "version": "1.0"}}
		case "ping":
			result = map[string]any{}
		case "tools/list":
			result = map[string]any{"tools": tools()}
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if json.Unmarshal(req.Params, &p) != nil {
				code, message = -32602, "invalid tool parameters"
				break
			}
			value, err := call(api, p.Name, p.Arguments)
			text := ""
			if err != nil {
				text = err.Error()
			} else {
				data, e := json.Marshal(value)
				if e != nil {
					err = e
					text = e.Error()
				} else {
					text = string(data)
				}
			}
			result = map[string]any{"content": []any{map[string]string{"type": "text", "text": text}}, "isError": err != nil}
		default:
			code, message = -32601, "method not found"
		}
		if err := respond(req.ID, result, code, message); err != nil {
			return err
		}
	}
	return scanner.Err()
}
func tools() []any {
	tool := func(name, description string, properties map[string]any, required []string) any {
		return map[string]any{"name": name, "description": description, "inputSchema": map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}}
	}
	str := map[string]string{"type": "string"}
	return []any{tool("list_bots", "List configured bots", map[string]any{}, []string{}), tool("send_message", "Queue a message using the bot's configured permissions", map[string]any{"bot_id": str, "text": str}, []string{"bot_id", "text"}), tool("status", "Read current runtime state without approval payloads", map[string]any{}, []string{}), tool("transcript", "Read a bot's main conversation", map[string]any{"bot_id": str}, []string{"bot_id"})}
}
func call(api *service.Client, name string, args json.RawMessage) (any, error) {
	var p struct {
		BotID string `json:"bot_id"`
		Text  string `json:"text"`
	}
	if len(args) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(args))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&p); err != nil {
			return nil, errors.New("invalid tool arguments")
		}
	}
	if (name == "list_bots" || name == "status") && (p.BotID != "" || p.Text != "") {
		return nil, errors.New("this tool takes no arguments")
	}
	if name == "transcript" && p.Text != "" {
		return nil, errors.New("transcript takes only bot_id")
	}
	switch name {
	case "list_bots":
		bots, err := api.Bots()
		if err != nil {
			return nil, err
		}
		v := []map[string]string{}
		for _, b := range bots {
			v = append(v, map[string]string{"id": b.ID, "name": b.Name, "engine": b.Engine})
		}
		return v, nil
	case "send_message":
		return api.Send(p.BotID, p.Text, nil, "queue")
	case "transcript":
		return api.Transcript(p.BotID)
	case "status":
		updates, err := api.Snapshot()
		if err != nil {
			return nil, err
		}
		v := []map[string]any{}
		for _, u := range updates {
			v = append(v, map[string]any{"bot_id": u.BotID, "thread_id": u.ThreadID, "room_id": u.RoomID, "busy": u.Busy, "queued": u.Queued, "approval_pending": u.Approval != nil || len(u.Approvals) > 0})
		}
		return v, nil
	default:
		return nil, errors.New("unknown control tool")
	}
}
