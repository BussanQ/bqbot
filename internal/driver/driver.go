// Package driver runs one agent engine (a local CLI) for one turn and
// normalizes its native output into Events.
package driver

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// EventKind is the kind of a normalized runtime event.
type EventKind string

const (
	// EvSession carries the provider-native continuation (Cursor) for the next turn.
	EvSession EventKind = "session"
	// EvText is a piece of the reply, appended to what came before.
	EvText EventKind = "text"
	// EvBreak ends a block of the reply: text after it starts a new paragraph.
	EvBreak EventKind = "break"
	// EvTool is a tool the agent started; Text describes it.
	EvTool EventKind = "tool"
)

// Event is one normalized runtime event.
type Event struct {
	Kind   EventKind
	Text   string
	Cursor string
}

// TurnInput is what a driver needs to run a turn.
type TurnInput struct {
	Text         string // the user's message
	System       string // the bot's persona
	Model        string // "" is the engine's default
	Cursor       string // continuation from a previous EvSession, "" for a new session
	Dir          string // working directory
	Effort       string
	ApprovalMode string // ask, auto-edit, full-auto
	Approve      ApproveFunc
	ApprovalMCP  *MCPServer
	Attachments  []Attachment
	History      []HistoryMessage
	Steer        <-chan string
	Settings     EngineSettings
	// MCP is the bqbot MCP server to give the turn, nil for none. The
	// driver opens only that server's tools to the agent.
	MCP *MCPServer
}

// MCPServer is a stdio MCP server command.
type MCPServer struct {
	Command string
	Args    []string
	// Tools adds the turn's discovered external tool names to the built-in list.
	Tools []string
}

// Driver runs turns on one engine.
type Driver interface {
	Name() string
	// Binary is the executable the driver runs, "" for none.
	Binary() string
	// Run runs one turn, calling emit as events come, and returns when the
	// turn ends. Cancelling ctx stops the turn.
	Run(ctx context.Context, in TurnInput, emit func(Event)) error
}

// Probe reports whether bin is installed, with its version.
func Probe(ctx context.Context, bin string) (string, error) {
	path, err := exec.LookPath(bin)
	if err != nil {
		return "", fmt.Errorf("%s not found on PATH", bin)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("%s --version: %w", bin, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// runLines starts bin with args, writes stdin to it, and hands every
// non-empty stdout line to onLine. The first error onLine returns ends the
// turn with that error once the process exits.
func runLines(ctx context.Context, bin string, args []string, dir, stdin string, onLine func([]byte) error) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	stderr := &tail{max: 4096}
	cmd.Stderr = stderr
	cmd.Cancel = func() error {
		if runtime.GOOS == "windows" {
			return cmd.Process.Kill()
		}
		return cmd.Process.Signal(os.Interrupt)
	}
	cmd.WaitDelay = 3 * time.Second
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", bin, err)
	}
	r := bufio.NewReader(out)
	var lineErr error
	for {
		line, err := r.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 && lineErr == nil {
			lineErr = onLine(line)
		}
		if err != nil {
			break
		}
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if lineErr != nil {
		return lineErr
	}
	if waitErr != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%s: %v: %s", bin, waitErr, msg)
		}
		return fmt.Errorf("%s: %w", bin, waitErr)
	}
	return nil
}

// tail keeps the last max bytes written to it.
type tail struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// ErrTurnFailed wraps an error the engine reported for the turn.
var ErrTurnFailed = errors.New("turn failed")

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
