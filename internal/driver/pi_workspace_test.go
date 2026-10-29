package driver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPiRPCSharedWorkspaceKeepsActiveGrant(t *testing.T) {
	bin, dir, log := piRPCFake(t, "cancel")
	firstCtx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- (&Pi{Bin: bin}).Run(firstCtx, TurnInput{Dir: dir, Text: "first", MCP: &MCPServer{Command: "bqbot", Args: []string{"mcp", "first-grant"}}}, func(Event) {})
	}()
	defer func() {
		cancel()
		select {
		case <-firstDone:
		case <-time.After(5 * time.Second):
			t.Error("first Pi process did not stop")
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(log)
		if err == nil && strings.Contains(string(data), `"id":"bqbot-prompt"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first process never started its prompt")
		}
		time.Sleep(time.Millisecond)
	}
	path := filepath.Join(dir, ".pi", "mcp.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	secondCtx, stop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stop()
	err = (&Pi{Bin: bin}).Run(secondCtx, TurnInput{Dir: dir, Text: "second"}, func(Event) {})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued Pi cancellation: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("waiting Pi removed active grant:", err)
	}
	if string(before) != string(after) {
		t.Fatal("waiting Pi overwrote active grant")
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), `"args":`) != 1 {
		t.Fatal("second Pi process started before workspace released")
	}
}

func TestPiWorkspaceReleaseAndDirectoryIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	dir := t.TempDir()
	release, err := lockPiWorkspace(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	other, err := lockPiWorkspace(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	other()
	release()
	next, err := lockPiWorkspace(ctx, dir)
	if err != nil {
		t.Fatal("released workspace unavailable:", err)
	}
	next()
}
