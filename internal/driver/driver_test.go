package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// fakeCLI writes an executable script that saves its argv and stdin and
// prints out, as the engine's CLI would.
func fakeCLI(t *testing.T, out string, exit int) (bin, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script CLI")
	}
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "out.jsonl"), []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$(dirname \"$0\")/args\"\ncat > \"$(dirname \"$0\")/stdin\"\ncat \"$(dirname \"$0\")/out.jsonl\"\nexit " +
		string(rune('0'+exit)) + "\n"
	bin = filepath.Join(dir, "cli")
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

func collect(t *testing.T, d Driver, in TurnInput) ([]Event, error) {
	t.Helper()
	var evs []Event
	err := d.Run(context.Background(), in, func(e Event) { evs = append(evs, e) })
	return evs, err
}

func text(evs []Event) string {
	var b strings.Builder
	for _, e := range evs {
		if e.Kind == EvText {
			b.WriteString(e.Text)
		}
	}
	return b.String()
}

func read(t *testing.T, path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const claudeStream = `{"type":"system","subtype":"hook_started","session_id":"s1"}
{"type":"system","subtype":"init","session_id":"s1","model":"claude"}
not json
{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}}
{"type":"assistant","message":{"content":[{"type":"text","text":"Hello"},{"type":"tool_use","name":"Bash","input":{"command":"ls -la"}}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"Done."}]}}
{"type":"result","subtype":"success","is_error":false,"result":"Done.","session_id":"s1"}
`

func TestClaude(t *testing.T) {
	bin, dir := fakeCLI(t, claudeStream, 0)
	d := &Claude{Bin: bin}
	evs, err := collect(t, d, TurnInput{Text: "hi there", System: "Be brief.", Model: "sonnet", Cursor: "prev", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	// Deltas stream; the assistant message that repeats them is not doubled;
	// the second message, without deltas, comes whole.
	if got := text(evs); got != "HelloDone." {
		t.Errorf("text %q", got)
	}
	if !slices.Contains(evs, Event{Kind: EvTool, Text: "Bash: ls -la"}) {
		t.Errorf("no tool event in %v", evs)
	}
	if !slices.Contains(evs, Event{Kind: EvSession, Cursor: "s1"}) {
		t.Errorf("no session in %v", evs)
	}
	args := strings.Fields(read(t, filepath.Join(dir, "args")))
	for _, want := range [][]string{{"--model", "sonnet"}, {"--resume", "prev"}, {"--permission-mode", "dontAsk"}, {"--output-format", "stream-json"}} {
		i := slices.Index(args, want[0])
		if i < 0 || i+1 >= len(args) || args[i+1] != want[1] {
			t.Errorf("args %q lack %q", args, want)
		}
	}
	if got := read(t, filepath.Join(dir, "stdin")); got != "hi there" {
		t.Errorf("stdin %q", got)
	}
}

func TestClaudeError(t *testing.T) {
	bin, dir := fakeCLI(t, `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"Not logged in"}`+"\n", 1)
	_, err := collect(t, &Claude{Bin: bin}, TurnInput{Text: "x", Dir: dir})
	if !errors.Is(err, ErrTurnFailed) || !strings.Contains(err.Error(), "Not logged in") {
		t.Errorf("err %v", err)
	}
}

func TestClaudeNoResult(t *testing.T) {
	bin, dir := fakeCLI(t, `{"type":"system","subtype":"init","session_id":"s1"}`+"\n", 0)
	if _, err := collect(t, &Claude{Bin: bin}, TurnInput{Text: "x", Dir: dir}); !errors.Is(err, ErrTurnFailed) {
		t.Errorf("err %v", err)
	}
}

func TestStop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script CLI")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "cli")
	os.WriteFile(bin, []byte("#!/bin/sh\necho '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"s\"}'\nexec sleep 30\n"), 0o700)
	ctx, cancel := context.WithCancel(context.Background())
	err := (&Claude{Bin: bin}).Run(ctx, TurnInput{Text: "x", Dir: dir}, func(e Event) {
		if e.Kind == EvSession {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err %v", err)
	}
}

const piStream = `{"type":"session","version":3,"id":"p1","timestamp":"2026-10-28T09:08:39.380Z","cwd":"/tmp"}
{"type":"agent_start"}
{"type":"message_start","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}
{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}
{"type":"message_start","message":{"role":"assistant","content":[],"stopReason":"pending"}}
{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"Look"}}
{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"ing."}}
{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Looking."}],"stopReason":"toolUse"}}
{"type":"tool_execution_start","toolCallId":"c1","toolName":"ls","args":{"path":"."}}
{"type":"tool_execution_end","toolCallId":"c1","toolName":"ls","isError":false}
{"type":"message_start","message":{"role":"assistant","content":[],"stopReason":"pending"}}
{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":"Done."}],"stopReason":"stop"}}
{"type":"agent_end","messages":[]}
{"type":"agent_settled","aborted":false}
`

func collectPiParser(t *testing.T, stream string) ([]Event, error) {
	t.Helper()
	var evs []Event
	p := &piParser{emit: func(e Event) { evs = append(evs, e) }}
	for _, line := range strings.Split(stream, "\n") {
		if err := p.line([]byte(line)); err != nil {
			return evs, err
		}
	}
	if p.lastErr != "" {
		return evs, fmt.Errorf("%w: %s", ErrTurnFailed, p.lastErr)
	}
	return evs, nil
}

func TestPiParser(t *testing.T) {
	evs, err := collectPiParser(t, piStream)
	if err != nil || text(evs) != "Looking.Done." {
		t.Fatalf("events %v error %v", evs, err)
	}
	if !slices.Contains(evs, Event{Kind: EvTool, Text: "ls: ."}) || !slices.Contains(evs, Event{Kind: EvSession, Cursor: "p1"}) {
		t.Errorf("events %v", evs)
	}
	args := (&Pi{}).Args(TurnInput{Model: "openai/gpt-5", Cursor: "p0", Effort: "high"})
	for _, want := range [][]string{{"--mode", "rpc"}, {"--model", "openai/gpt-5"}, {"--session", "p0"}, {"--thinking", "high"}} {
		if after(args, want[0]) != want[1] {
			t.Errorf("args %q lack %q", args, want)
		}
	}
	if !slices.Contains(args, "--no-extensions") || !slices.Contains(args, "--no-mcp") {
		t.Errorf("args %q", args)
	}
}

// Pi exits 0 when the provider fails; the error is in the stream, as the
// real CLI printed it.
func TestPiProviderError(t *testing.T) {
	evs, err := collectPiParser(t, `{"type":"session","version":3,"id":"p1"}
{"type":"message_start","message":{"role":"assistant","content":[]}}
{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"Connection error."}}
{"type":"auto_retry_start","attempt":1,"maxAttempts":3,"delayMs":2000,"errorMessage":"Connection error."}
{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"Connection error."}}
{"type":"auto_retry_end","success":false,"attempt":3,"finalError":"Connection error."}
{"type":"agent_settled","aborted":false}
`)
	if !errors.Is(err, ErrTurnFailed) || !strings.Contains(err.Error(), "Connection error.") {
		t.Errorf("err %v", err)
	}
	// The session still counts, so the next turn resumes it.
	if !slices.Contains(evs, Event{Kind: EvSession, Cursor: "p1"}) {
		t.Errorf("events %v", evs)
	}
}

// A retry that succeeds clears the error of the attempt before it.
func TestPiRetrySucceeds(t *testing.T) {
	evs, err := collectPiParser(t, `{"type":"session","version":3,"id":"p1"}
{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"529 overloaded"}}
{"type":"auto_retry_start","attempt":1}
{"type":"message_start","message":{"role":"assistant","content":[]}}
{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"stopReason":"stop"}}
{"type":"auto_retry_end","success":true,"attempt":2}
{"type":"agent_settled","aborted":false}
`)
	if err != nil || text(evs) != "ok" {
		t.Errorf("text %q, err %v", text(evs), err)
	}
}

var testMCP = &MCPServer{Command: "/Apps/bq bot", Args: []string{"mcp", "/tmp/grant.json"}}

// argLines are the args the fake CLI saved, one per line, as given.
func argLines(t *testing.T, dir string) []string {
	return strings.Split(strings.TrimSuffix(read(t, filepath.Join(dir, "args")), "\n"), "\n")
}

func after(args []string, flag string) string {
	if i := slices.Index(args, flag); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

func TestClaudeMCP(t *testing.T) {
	bin, dir := fakeCLI(t, claudeStream, 0)
	if _, err := collect(t, &Claude{Bin: bin}, TurnInput{Text: "x", Dir: dir, MCP: testMCP}); err != nil {
		t.Fatal(err)
	}
	args := argLines(t, dir)
	var cfg struct {
		MCPServers map[string]struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(after(args, "--mcp-config")), &cfg); err != nil {
		t.Fatalf("--mcp-config in %q: %v", args, err)
	}
	s := cfg.MCPServers["bqbot"]
	if len(cfg.MCPServers) != 1 || s.Type != "stdio" || s.Command != testMCP.Command || !slices.Equal(s.Args, testMCP.Args) {
		t.Errorf("config %+v", cfg)
	}
	if !slices.Contains(args, "--strict-mcp-config") || after(args, "--allowedTools") != "mcp__bqbot__ask_bot,mcp__bqbot__list_bots" ||
		after(args, "--permission-mode") != "dontAsk" {
		t.Errorf("args %q", args)
	}

	collect(t, &Claude{Bin: bin}, TurnInput{Text: "x", Dir: dir})
	if args := argLines(t, dir); !slices.Contains(args, "--strict-mcp-config") || slices.Contains(args, "--allowedTools") {
		t.Errorf("args without MCP %q", args)
	}
}

func TestCodexMCP(t *testing.T) {
	args := (&Codex{}).Args(TurnInput{MCP: testMCP})
	for _, want := range []string{
		`mcp_servers.bqbot.command="/Apps/bq bot"`,
		`mcp_servers.bqbot.args=["mcp","/tmp/grant.json"]`,
		`mcp_servers.bqbot.enabled_tools=["ask_bot","list_bots"]`,
		`mcp_servers.bqbot.default_tools_approval_mode="approve"`,
		`mcp_servers.bqbot.tool_timeout_sec=900`,
		`sandbox_mode="read-only"`,
	} {
		if i := slices.Index(args, want); i < 1 || args[i-1] != "-c" {
			t.Errorf("args %q lack -c %s", args, want)
		}
	}
	if args[0] != "app-server" {
		t.Errorf("args %q", args)
	}
}

func TestPiMCP(t *testing.T) {
	dir := t.TempDir()
	if err := writeMCPConfig(dir, testMCP); err != nil {
		t.Fatal(err)
	}
	args := (&Pi{}).Args(TurnInput{MCP: testMCP})
	if slices.Contains(args, "--no-mcp") || !slices.Contains(args, "--approve") ||
		after(args, "--tools") != "read,grep,find,ls,edit,write,bash,mcp__bqbot__ask_bot,mcp__bqbot__list_bots" {
		t.Errorf("args %q", args)
	}
	cfgPath := filepath.Join(dir, ".pi", "mcp.json")
	var cfg struct {
		Codemode   bool `json:"autoEnableCodemode"`
		MCPServers map[string]struct {
			Command  string   `json:"command"`
			Args     []string `json:"args"`
			Exposure string   `json:"exposure"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(read(t, cfgPath)), &cfg); err != nil {
		t.Fatal(err)
	}
	s := cfg.MCPServers["bqbot"]
	if len(cfg.MCPServers) != 1 || cfg.Codemode || s.Exposure != "direct" || s.Command != testMCP.Command || !slices.Equal(s.Args, testMCP.Args) {
		t.Errorf("config %+v", cfg)
	}
	if fi, _ := os.Stat(cfgPath); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}

	// A turn without the server removes the config and turns MCP off.
	if err := writeMCPConfig(dir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Errorf("config left: %v", err)
	}
	if args := (&Pi{}).Args(TurnInput{}); !slices.Contains(args, "--no-mcp") || slices.Contains(args, "--approve") {
		t.Errorf("args %q", args)
	}
}
