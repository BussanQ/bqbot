package driver

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// ToolStatus describes the state reported for a single tool call.
type ToolStatus string

const (
	ToolRunning     ToolStatus = "running"
	ToolCompleted   ToolStatus = "completed"
	ToolFailed      ToolStatus = "failed"
	ToolUnconfirmed ToolStatus = "unconfirmed" // the turn ended without a final tool event
)

func resultStatus(failed bool) ToolStatus {
	if failed {
		return ToolFailed
	}
	return ToolCompleted
}

const toolOutputLimit = 8 << 10
const toolOutputNotice = "\n…（输出已截断）"

// LimitToolOutput bounds the text stored with a call, preserving lines and UTF-8.
func LimitToolOutput(text string) string {
	if len(text) <= toolOutputLimit {
		return text
	}
	end := toolOutputLimit - len(toolOutputNotice)
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + toolOutputNotice
}

// AppendToolOutput adds a streamed fragment until the preview is full.
func AppendToolOutput(current, delta string) string {
	if len(current) >= toolOutputLimit-utf8.UTFMax+1 && strings.HasSuffix(current, toolOutputNotice) {
		return current
	}
	return LimitToolOutput(current + delta)
}

// contentText extracts displayable text from CLI and MCP content blocks.
// Images, audio and embedded blobs are not part of the text preview.
func contentText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	switch raw[0] {
	case '"':
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	case '[':
		var blocks []json.RawMessage
		_ = json.Unmarshal(raw, &blocks)
		var out []string
		for _, block := range blocks {
			if s := contentText(block); s != "" {
				out = append(out, s)
			}
		}
		return strings.Join(out, "\n")
	case '{':
		var block struct {
			Type, Text, Path, OldText, NewText string
			Content                            json.RawMessage
			Resource                           struct{ Text string }
		}
		if json.Unmarshal(raw, &block) != nil {
			return ""
		}
		switch block.Type {
		case "text":
			return block.Text
		case "content":
			return contentText(block.Content)
		case "resource":
			return block.Resource.Text
		case "diff":
			return strings.TrimSpace(block.Path + "\n- " + strings.ReplaceAll(block.OldText, "\n", "\n- ") + "\n+ " + strings.ReplaceAll(block.NewText, "\n", "\n+ "))
		}
	}
	return ""
}

func rawOutputText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	if raw[0] == '"' {
		return contentText(raw)
	}
	if raw[0] == '[' {
		var blocks []struct{ Type string }
		if json.Unmarshal(raw, &blocks) == nil {
			for _, b := range blocks {
				if isToolContentBlock(b.Type) {
					return contentText(raw)
				}
			}
		}
	}
	if raw[0] == '{' {
		var v struct {
			Type           string
			Content        json.RawMessage
			Stdout, Stderr string
		}
		if json.Unmarshal(raw, &v) == nil {
			if isToolContentBlock(v.Type) {
				return contentText(raw)
			}
			if len(v.Content) > 0 {
				return contentText(v.Content)
			}
			if v.Stdout != "" && v.Stderr != "" {
				return v.Stdout + "\n" + v.Stderr
			}
			if v.Stdout != "" || v.Stderr != "" {
				return v.Stdout + v.Stderr
			}
		}
	}
	var out bytes.Buffer
	if json.Indent(&out, raw, "", "  ") != nil {
		return ""
	}
	return out.String()
}

func isToolContentBlock(kind string) bool {
	switch kind {
	case "text", "content", "resource", "diff", "image", "audio", "resource_link", "terminal":
		return true
	}
	return false
}

// MCPToolText extracts text and the failure flag from an MCP CallToolResult.
func MCPToolText(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && (raw[0] == '"' || raw[0] == '[') {
		return LimitToolOutput(contentText(raw)), false
	}
	var result struct {
		Content, StructuredContent json.RawMessage
		IsError                    bool
	}
	if json.Unmarshal(raw, &result) != nil {
		return "", false
	}
	output := contentText(result.Content)
	if output == "" {
		output = rawOutputText(result.StructuredContent)
	}
	return LimitToolOutput(output), result.IsError
}
