package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"bqbot/internal/driver"
	"bqbot/internal/mcp"
	"bqbot/internal/store"
)

type computerTestEngine struct{}

func (*computerTestEngine) Name() string   { return "computer-test" }
func (*computerTestEngine) Binary() string { return "" }
func (*computerTestEngine) Run(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
	b, e := os.ReadFile(in.MCP.Args[1])
	if e != nil {
		return e
	}
	var grant mcp.Grant
	if e = json.Unmarshal(b, &grant); e != nil {
		return e
	}
	backend := &mcp.HTTPBackend{URL: grant.URL, Token: grant.Token}
	v, e := backend.CallExternal(ctx, "external_computer", json.RawMessage(`{"kind":"screenshot"}`))
	if e != nil {
		return e
	}
	b, _ = json.Marshal(v)
	emit(driver.Event{Kind: driver.EvText, Text: string(b)})
	return nil
}
func TestComputerPermissionAndConfigurationRevocation(t *testing.T) {
	rig := newRig(t, &computerTestEngine{})
	bot := rig.bot("Computer", "computer-test", "")
	config := store.ComputerConfig{BotID: bot.ID, Provider: "vm", URL: "http://127.0.0.1:1/mcp", Enabled: true}
	if e := rig.st.SaveComputer(config); e != nil {
		t.Fatal(e)
	}
	if _, e := rig.h.Send(bot, "take a screenshot"); e != nil {
		t.Fatal(e)
	}
	var approval *driver.ApprovalRequest
	timer := time.After(3 * time.Second)
	for approval == nil {
		select {
		case u := <-rig.ups:
			approval = u.Approval
		case <-timer:
			t.Fatal("computer did not request approval before connecting")
		}
	}
	config.Enabled = false
	if e := rig.st.SaveComputer(config); e != nil {
		t.Fatal(e)
	}
	if e := rig.h.Approve(bot.ID, approval.ID, driver.ApprovalDecision{Outcome: "allow"}); e != nil {
		t.Fatal(e)
	}
	done := rig.wait(bot.ID)
	if done.Err == nil || !strings.Contains(done.Err.Error(), "configuration changed") {
		t.Fatal(done)
	}
}
func TestComputerScreenshotPersistsInTranscript(t *testing.T) {
	var frame bytes.Buffer
	if e := png.Encode(&frame, image.NewRGBA(image.Rect(0, 0, 2, 2))); e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/commands") {
			w.Write([]byte(`{"success":true,"exitCode":0}`))
			return
		}
		w.Write(frame.Bytes())
	}))
	defer server.Close()
	t.Setenv("BQBOT_COMPUTER_TEST_TOKEN", "test")
	rig := newRig(t, &computerTestEngine{})
	bot := rig.bot("Computer", "computer-test", "")
	bot.ApprovalMode = "full-auto"
	if e := rig.st.SaveBot(bot); e != nil {
		t.Fatal(e)
	}
	if e := rig.st.SaveComputer(store.ComputerConfig{BotID: bot.ID, Provider: "boat", URL: server.URL, Enabled: true, SandboxID: "test", TokenEnv: "BQBOT_COMPUTER_TEST_TOKEN"}); e != nil {
		t.Fatal(e)
	}
	if _, e := rig.h.Send(bot, "screenshot"); e != nil {
		t.Fatal(e)
	}
	done := rig.wait(bot.ID)
	if done.Err != nil || done.Reply == nil || len(done.Reply.Attachments) != 1 {
		t.Fatal(done)
	}
	messages, e := rig.st.Transcript(bot.ID)
	if e != nil || len(messages) != 2 || len(messages[1].Attachments) != 1 || !strings.HasPrefix(messages[1].Attachments[0].Name, "computer-") {
		t.Fatal(messages, e)
	}
}

func TestComputerConsultHasNoTools(t *testing.T) {
	rig := newRig(t, &computerTestEngine{})
	bot := rig.bot("Computer", "computer-test", "")
	if e := rig.st.SaveComputer(store.ComputerConfig{BotID: bot.ID, Provider: "vm", URL: "http://127.0.0.1:1/mcp", Enabled: true}); e != nil {
		t.Fatal(e)
	}
	turn := &turn{consult: true, bot: bot}
	in := &driver.TurnInput{MCP: &driver.MCPServer{}}
	cleanup, e := rig.h.prepareExternal(turn, in)
	if cleanup != nil {
		cleanup()
	}
	if e != nil || len(in.MCP.Tools) > 0 || turn.external != nil {
		t.Fatal(e, in.MCP.Tools)
	}
}
