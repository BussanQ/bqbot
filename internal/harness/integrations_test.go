package harness

import (
	"bqbot/internal/driver"
	"bqbot/internal/mcp"
	"bqbot/internal/store"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type externalEngine struct{}

func (*externalEngine) Name() string   { return "external-test" }
func (*externalEngine) Binary() string { return "" }
func (*externalEngine) Run(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
	data, err := os.ReadFile(in.MCP.Args[1])
	if err != nil {
		return err
	}
	var grant mcp.Grant
	if err = json.Unmarshal(data, &grant); err != nil {
		return err
	}
	backend := &mcp.HTTPBackend{URL: grant.URL, Token: grant.Token}
	tools, err := backend.ExternalTools(ctx)
	if err != nil {
		return err
	}
	if len(tools) == 0 {
		emit(driver.Event{Kind: driver.EvText, Text: "no external tools"})
		return nil
	}
	result, err := backend.CallExternal(ctx, tools[0]["name"].(string), json.RawMessage(`{"query":"hello"}`))
	if err != nil {
		return err
	}
	encoded, _ := json.Marshal(result)
	emit(driver.Event{Kind: driver.EvText, Text: string(encoded)})
	return nil
}
func TestExternalMCPApprovalScopeAndRevocation(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private" {
			t.Error("missing environment credential")
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if len(req.ID) == 0 {
			w.WriteHeader(202)
			return
		}
		var result any = map[string]any{}
		switch req.Method {
		case "initialize":
			result = map[string]string{"protocolVersion": "2025-06-18"}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "lookup", "inputSchema": map[string]string{"type": "object"}}}}
		case "tools/call":
			calls.Add(1)
			result = map[string]any{"content": []any{map[string]string{"type": "text", "text": "external answer"}}}
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer upstream.Close()
	t.Setenv("BQBOT_TEST_MCP_TOKEN", "Bearer private")
	rig := newRig(t, &externalEngine{})
	bot := rig.bot("Tools", "external-test", "")
	other := rig.bot("Isolated", "external-test", "")
	if err := rig.st.SaveMCPServer(store.MCPConfig{ID: "fixture", Name: "Fixture", Enabled: true, Transport: "http", URL: upstream.URL, Headers: map[string]string{"Authorization": "BQBOT_TEST_MCP_TOKEN"}, BotIDs: []string{bot.ID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.h.Send(bot, "use tool"); err != nil {
		t.Fatal(err)
	}
	var approval *driver.ApprovalRequest
	deadline := time.After(3 * time.Second)
	for approval == nil {
		select {
		case u := <-rig.ups:
			approval = u.Approval
		case <-deadline:
			t.Fatal("missing external tool approval")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("upstream executed before approval")
	}
	if err := rig.h.Approve(bot.ID, approval.ID, driver.ApprovalDecision{Outcome: "allow"}); err != nil {
		t.Fatal(err)
	}
	done := rig.wait(bot.ID)
	if done.Err != nil || !strings.Contains(done.Reply.Text, "external answer") || calls.Load() != 1 {
		t.Fatal(done, calls.Load())
	}
	if _, err := rig.h.Send(other, "no access"); err != nil {
		t.Fatal(err)
	}
	done = rig.wait(other.ID)
	if done.Reply.Text != "no external tools" || calls.Load() != 1 {
		t.Fatal(done)
	}
	if _, err := rig.h.Send(bot, "deny tool"); err != nil {
		t.Fatal(err)
	}
	approval = nil
	deadline = time.After(3 * time.Second)
	for approval == nil {
		select {
		case u := <-rig.ups:
			approval = u.Approval
		case <-deadline:
			t.Fatal("missing second approval")
		}
	}
	if err := rig.h.Approve(bot.ID, approval.ID, driver.ApprovalDecision{Outcome: "deny"}); err != nil {
		t.Fatal(err)
	}
	done = rig.wait(bot.ID)
	if !strings.Contains(done.Reply.Text, "permission denied") || calls.Load() != 1 {
		t.Fatal(done)
	}
}
