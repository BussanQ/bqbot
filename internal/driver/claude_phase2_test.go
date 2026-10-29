package driver

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"bqbot/internal/mcp"
)

func TestClaudeApprovalBridge(t *testing.T) {
	approval := &MCPServer{Command: "bqbot", Args: []string{"mcp", "approval-grant"}}
	for _, handoff := range []*MCPServer{nil, testMCP} {
		in := TurnInput{MCP: handoff, ApprovalMCP: approval, ApprovalMode: "full-auto", Effort: "high"}
		args := (&Claude{}).Args(in)
		if after(args, "--permission-prompt-tool") != mcp.ApprovalToolName || after(args, "--permission-mode") != "manual" || after(args, "--effort") != "high" {
			t.Fatalf("args %q", args)
		}
		if slices.Contains(args, "--dangerously-skip-permissions") {
			t.Fatal("bypassed bridge")
		}
		var cfg struct {
			Servers map[string]MCPServer `json:"mcpServers"`
		}
		if err := json.Unmarshal([]byte(after(args, "--mcp-config")), &cfg); err != nil {
			t.Fatal(err)
		}
		if cfg.Servers[mcp.ApprovalServer].Command != "bqbot" {
			t.Fatal(cfg)
		}
		_, hasHandoff := cfg.Servers[mcp.Server]
		if hasHandoff != (handoff != nil) {
			t.Fatal("handoff depth boundary lost")
		}
	}
	args := (&Claude{}).Args(TurnInput{ApprovalMode: "full-auto"})
	if after(args, "--permission-mode") != "dontAsk" {
		t.Fatalf("missing callback not fail-closed: %q", args)
	}
}

func TestClaudeInputAndCustomPath(t *testing.T) {
	bin, dir := fakeCLI(t, claudeStream, 0)
	in := TurnInput{Text: "new", Dir: dir, Settings: EngineSettings{Path: bin}, Attachments: []Attachment{{Name: "x", MIME: "image/png", Data: "eA=="}}, History: []HistoryMessage{{Role: "assistant", Text: "previous"}}}
	if _, err := collect(t, &Claude{Bin: "must-not-run"}, in); err != nil {
		t.Fatal(err)
	}
	args := argLines(t, dir)
	if after(args, "--input-format") != "stream-json" {
		t.Fatal(args)
	}
	var wire map[string]any
	if err := json.Unmarshal([]byte(read(t, filepath.Join(dir, "stdin"))), &wire); err != nil {
		t.Fatal(err)
	}
	encoded := read(t, filepath.Join(dir, "stdin"))
	if !strings.Contains(encoded, "previous") || !strings.Contains(encoded, `"media_type":"image/png"`) {
		t.Fatal(encoded)
	}
	in.Cursor = "existing"
	encoded, err := claudeInput(in)
	if err != nil || strings.Contains(encoded, "previous") {
		t.Fatalf("resume replayed history: %s %v", encoded, err)
	}
	in.Attachments[0].MIME = "text/plain"
	if _, err := claudeInput(in); err == nil {
		t.Fatal("nonimage accepted")
	}
}

func TestClaudeSessionExpiration(t *testing.T) {
	for _, tt := range []struct {
		name, message string
		expired       bool
	}{
		{"missing", "No conversation found with session ID: old", true},
		{"generic", "authentication failed", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			payload, _ := json.Marshal(map[string]any{"type": "result", "is_error": true, "subtype": "error_during_execution", "result": tt.message})
			bin, dir := fakeCLI(t, string(payload)+"\n", 1)
			_, err := collect(t, &Claude{Bin: bin}, TurnInput{Dir: dir, Cursor: "old", Text: "x"})
			if errors.Is(err, ErrSessionExpired) != tt.expired {
				t.Fatalf("err %v", err)
			}
		})
	}
	bin, dir := fakeCLI(t, `{"type":"assistant","message":{"content":[{"type":"text","text":"partial"}]}}
{"type":"result","is_error":true,"subtype":"error","result":"session not found"}`+"\n", 1)
	err := (&Claude{Bin: bin}).Run(context.Background(), TurnInput{Dir: dir, Cursor: "old"}, func(Event) {})
	if errors.Is(err, ErrSessionExpired) {
		t.Fatal("partial turn classified as expired")
	}
}
