package computer

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"bqbot/internal/mcpgateway"
	"bqbot/internal/store"
)

// WorkspaceSchema is shared by the MCP bridge and the turn-scoped computer tool.
//
//go:embed workspace-action.json
var WorkspaceSchema json.RawMessage

func workspaceAction(ctx context.Context, c store.ComputerConfig, token string, a Action) (Result, error) {
	client, err := mcpgateway.Open(ctx, mcpgateway.Config{Transport: "http", URL: strings.TrimRight(c.URL, "/") + "/mcp", Headers: map[string]string{"Authorization": "Bearer " + token}})
	if err != nil {
		return Result{}, errors.New("无法连接 Workspace Bridge，请检查地址、令牌和工作区服务")
	}
	defer client.Close()
	args, _ := json.Marshal(a)
	raw, err := client.Call(ctx, "workspace_action", args)
	if err != nil {
		return Result{}, err
	}
	var result struct {
		IsError bool
		Content []struct{ Type, Text, Data string }
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		return Result{}, err
	}
	var lines []string
	for _, v := range result.Content {
		if v.Type == "text" {
			lines = append(lines, v.Text)
		}
		if !result.IsError && a.Kind == "screenshot" && v.Type == "image" {
			if len(v.Data) > base64.StdEncoding.EncodedLen(maxImage) {
				return Result{}, errors.New("workspace screenshot is too large")
			}
			data, err := base64.StdEncoding.DecodeString(v.Data)
			if err != nil {
				return Result{}, err
			}
			return screenshot(data)
		}
	}
	text := strings.Join(lines, "\n")
	if result.IsError {
		if text == "" {
			text = "workspace action failed"
		}
		return Result{}, errors.New(text)
	}
	if a.Kind == "screenshot" {
		return Result{}, errors.New("workspace did not return a screenshot")
	}
	return Result{Text: text}, nil
}

type StreamAnswer struct {
	Control bool   `json:"control,omitempty"`
	ID      string `json:"id"`
	SDP     string `json:"sdp"`
}

func OpenStream(ctx context.Context, c store.ComputerConfig, offer string, st *store.Store, control bool) (StreamAnswer, error) {
	c, token, err := workspaceConnection(c, st)
	if err != nil {
		return StreamAnswer{}, err
	}
	if offer == "" || len(offer) > 96<<10 {
		return StreamAnswer{}, errors.New("invalid WebRTC offer")
	}
	b, err := request(ctx, c, token, "POST", "/streams", map[string]any{"sdp": offer, "control": control}, 128<<10)
	var a StreamAnswer
	if err != nil {
		return a, err
	}
	if json.Unmarshal(b, &a) != nil || a.SDP == "" || !validStreamID(a.ID) {
		return StreamAnswer{}, errors.New("invalid Workspace Bridge answer")
	}
	return a, nil
}

func validStreamID(id string) bool {
	_, err := hex.DecodeString(id)
	return len(id) == 32 && err == nil
}

// UpdateStream renews or ends a previously negotiated video lease.
func UpdateStream(ctx context.Context, c store.ComputerConfig, id string, closeStream bool, st *store.Store) error {
	if !validStreamID(id) {
		return errors.New("invalid workspace stream ID")
	}
	c, token, err := workspaceConnection(c, st)
	if err != nil {
		return err
	}
	method := "PUT"
	if closeStream {
		method = "DELETE"
	}
	_, err = request(ctx, c, token, method, "/streams/"+id, nil, 1024)
	return err
}
