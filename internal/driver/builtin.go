// The built-in agent uses a chat completions model and only the MCP server
// granted to this turn. Provider tool IDs are never credentials.
package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"bqbot/internal/mcp"
	"bqbot/internal/mcpgateway"
)

// Builtin runs the first-party model and MCP tool loop without a local agent CLI.
type Builtin struct{ OpenAI }

func (*Builtin) Name() string   { return "bqbot" }
func (*Builtin) Binary() string { return "" }

type apiToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type apiToolRound struct {
	Calls    []apiToolCall
	Complete bool
}

func (r *apiToolRound) add(raw json.RawMessage) error {
	var fragments []struct {
		Index *int `json:"index"`
		apiToolCall
	}
	if json.Unmarshal(raw, &fragments) != nil {
		return errors.New("invalid tool call stream")
	}
	for _, f := range fragments {
		if f.Index == nil || *f.Index < 0 || *f.Index >= 16 {
			return errors.New("invalid tool call index")
		}
		for len(r.Calls) <= *f.Index {
			r.Calls = append(r.Calls, apiToolCall{})
		}
		call := &r.Calls[*f.Index]
		call.ID += f.ID
		call.Type += f.Type
		call.Function.Name += f.Function.Name
		call.Function.Arguments += f.Function.Arguments
		if len(call.Function.Arguments) > 1<<20 || len(call.Function.Name) > 128 || len(call.ID) > 256 {
			return errors.New("tool call is too large")
		}
	}
	return nil
}

func workspaceInstructions(dir string) (string, error) {
	if dir == "" {
		return "", nil
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	info, err := root.Stat("AGENTS.md")
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 32<<10 {
		return "", errors.New("工作目录中的 AGENTS.md 必须是 32 KiB 以内的文本文件")
	}
	f, err := root.Open("AGENTS.md")
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (32<<10)+1))
	if len(b) > 32<<10 {
		return "", errors.New("工作目录中的 AGENTS.md 超出大小限制")
	}
	return string(b), err
}

func (d *Builtin) Run(ctx context.Context, in TurnInput, emit func(Event)) error {
	if in.Settings.BaseURL == "" && in.Settings.APIKeyEnv == "" && os.Getenv("OPENAI_API_KEY") == "" {
		return errors.New("请在「引擎设置 → bqbot」配置模型服务，或设置 OPENAI_API_KEY 后重新启动 bqbot")
	}
	instructions, err := workspaceInstructions(in.Dir)
	if err != nil {
		return err
	}
	if instructions != "" {
		in.System += "\n\nWorkspace AGENTS.md:\n" + instructions
	}
	messages, err := apiHistory(in)
	if err != nil {
		return err
	}
	var client *mcpgateway.Client
	var tools []any
	allowed := map[string]bool{}
	if in.MCP != nil {
		client, err = mcpgateway.Open(ctx, mcpgateway.Config{Transport: "stdio", Command: in.MCP.Command, Args: in.MCP.Args})
		if err != nil {
			return err
		}
		defer client.Close()
		catalog, err := client.Tools(ctx)
		if err != nil {
			return err
		}
		for _, tool := range catalog {
			allowed[tool.Name] = true
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": tool.Name, "description": tool.Description, "parameters": tool.InputSchema}})
		}
	}
	lastImages := -1
	for step := 0; step < 16; step++ {
		var round apiToolRound
		var text strings.Builder
		if step == 15 {
			tools = nil
		} // reserve the final model call for a reply
		err = d.round(ctx, in, messages, tools, &round, func(e Event) {
			if e.Kind == EvText {
				text.WriteString(e.Text)
			}
			emit(e)
		})
		if err != nil || len(round.Calls) == 0 {
			return err
		}
		// Validate the entire batch before making any side effects.
		seen := map[string]bool{}
		for _, c := range round.Calls {
			var args map[string]json.RawMessage
			if !round.Complete || c.ID == "" || seen[c.ID] || c.Type != "function" || !allowed[c.Function.Name] || json.Unmarshal([]byte(c.Function.Arguments), &args) != nil || args == nil {
				return errors.New("模型返回了无效或未授权的工具调用")
			}
			seen[c.ID] = true
		}
		messages = append(messages, map[string]any{"role": "assistant", "content": text.String(), "tool_calls": round.Calls})
		var images []Attachment
		imageBytes := 0
		for _, c := range round.Calls {
			if err := ctx.Err(); err != nil {
				return err
			}
			id, label := fmt.Sprintf("%d/%s", step, c.ID), mcp.ToolPrefix+c.Function.Name
			emit(Event{Kind: EvTool, ToolID: id, Text: label})
			raw, callErr := client.Call(ctx, c.Function.Name, json.RawMessage(c.Function.Arguments))
			output, failed := mcpToolContent(raw)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if callErr != nil {
				output, failed = callErr.Error(), true
			}
			emit(Event{Kind: EvToolResult, ToolID: id, Text: label, Status: resultStatus(failed), Output: LimitToolOutput(output)})
			if len(output) > 128<<10 {
				output = strings.ToValidUTF8(output[:128<<10], "�") + "\n[tool output truncated]"
			}
			var result struct {
				Content []struct{ Type, MIMEType, Data string }
			}
			if !failed && json.Unmarshal(raw, &result) == nil {
				for _, block := range result.Content {
					if block.Type == "image" && len(images) < 4 && len(block.Data) > 0 && imageBytes+len(block.Data) <= 12<<20 {
						img := Attachment{MIME: block.MIMEType, Data: block.Data}
						if _, e := imageDataURL(img); e == nil {
							images = append(images, img)
							imageBytes += len(block.Data)
							output += "\n[image from " + c.Function.Name + "]"
						}
					}
				}
			}
			if failed {
				output = "Tool failed: " + output
			}
			messages = append(messages, map[string]string{"role": "tool", "tool_call_id": c.ID, "content": output})
		}
		if len(images) > 0 {
			if lastImages >= 0 {
				messages[lastImages] = map[string]string{"role": "user", "content": "[Earlier tool images omitted in favor of the latest frames.]"}
			}
			msg, err := apiMessage("user", "Images returned by the preceding tools:", images)
			if err != nil {
				return err
			}
			lastImages = len(messages)
			messages = append(messages, msg)
		}
		emit(Event{Kind: EvBreak})
	}
	return errors.New("本轮已达 16 次模型调用上限")
}
