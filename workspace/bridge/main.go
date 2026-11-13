// bqbot-workspace serves one isolated desktop. Only its authenticated bridge
// is exposed, Xvnc and Chromium's debugging endpoint listen on loopback.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const toolName = "workspace_action"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--bridge-revision" {
		fmt.Println("input-v1")
		return
	}
	secret, err := hex.DecodeString(os.Getenv("BQBOT_WORKSPACE_HASH"))
	if err != nil || len(secret) != sha256.Size {
		log.Fatal("invalid workspace authentication digest")
	}
	os.Unsetenv("BQBOT_WORKSPACE_HASH")
	raw, err := os.ReadFile("/opt/bqbot/workspace-action.json")
	if err != nil {
		log.Fatal(err)
	}
	port, err := strconv.Atoi(os.Getenv("BQBOT_MEDIA_PORT"))
	if err != nil || port < 1 || port > 65535 {
		log.Fatal("invalid BQBOT_MEDIA_PORT")
	}
	video, err := newVideoServer(port, os.Getenv("BQBOT_ICE_HOST"))
	if err != nil {
		log.Fatal(err)
	}
	defer video.close()
	mux := http.NewServeMux()
	mux.Handle("POST /mcp", &bridge{schema: raw, gate: &video.gate, slot: make(chan struct{}, 1)})
	mux.HandleFunc("POST /streams", video.offer)
	mux.HandleFunc("PUT /streams/{id}", video.renew)
	mux.HandleFunc("DELETE /streams/{id}", video.stop)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(got[:], secret[:]) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}
		if r.Header.Get("Origin") != "" {
			http.Error(w, "direct browser access is disabled", 403)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
	server := http.Server{Addr: ":18443", Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 45 * time.Second, MaxHeaderBytes: 32 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	go func() { <-ctx.Done(); server.Close() }()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

type bridge struct {
	gate   *inputGate
	schema json.RawMessage
	slot   chan struct{}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func (b *bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req rpcRequest
	if !readJSON(w, r, &req) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if req.JSONRPC != "2.0" {
		http.Error(w, "invalid JSON-RPC request", 400)
		return
	}
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	fail := func(code int, msg string) { resp["error"] = map[string]any{"code": code, "message": msg} }
	switch req.Method {
	case "initialize":
		resp["result"] = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "bqbot-workspace", "version": "1"}}
	case "ping":
		resp["result"] = map[string]any{}
	case "tools/list":
		resp["result"] = map[string]any{"tools": []any{map[string]any{"name": toolName, "description": "Inspect or operate this workspace's browser and Linux desktop. Read the current page or accessibility tree before targeting an element.", "inputSchema": b.schema}}}
	case "tools/call":
		var p struct {
			Name      string
			Arguments json.RawMessage
		}
		if json.Unmarshal(req.Params, &p) != nil || p.Name != toolName || len(p.Arguments) == 0 {
			fail(-32602, "unknown tool or missing arguments")
			break
		}
		result, err := b.execute(r.Context(), p.Arguments)
		if err != nil {
			resp["result"] = map[string]any{"isError": true, "content": []any{map[string]string{"type": "text", "text": err.Error()}}}
		} else {
			resp["result"] = result
		}
	default:
		fail(-32601, "unsupported method")
	}
	json.NewEncoder(w).Encode(resp)
}

func readJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	if dec.Decode(out) != nil || dec.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid request", 400)
		return false
	}
	return true
}

func (b *bridge) execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	select {
	case b.slot <- struct{}{}:
		defer func() { <-b.slot }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var op struct{ Kind string }
	if err := json.Unmarshal(args, &op); err != nil {
		return nil, err
	}
	switch op.Kind {
	case "screenshot", "gui_tree", "browser_read", "browser_tabs":
	default:
		if !b.gate.TryRLock() {
			return nil, errors.New("桌面已由用户接管，请在释放接管后重试")
		}
		defer b.gate.RUnlock()
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/python3", "/opt/bqbot/desktop.py")
	// A cancelled typing operation must also stop the helper's xdotool child.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.Stdin = bytes.NewReader(args)
	cmd.WaitDelay = time.Second
	data, err := cmd.Output()
	if err != nil || len(data) > 12<<20 || !json.Valid(data) {
		return nil, errors.New("workspace action failed or timed out")
	}
	return json.RawMessage(strings.TrimSpace(string(data))), nil
}
