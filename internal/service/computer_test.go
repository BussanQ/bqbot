package service

import (
	"errors"
	"net/http"
	"testing"

	"bqbot/internal/computer"
	"bqbot/internal/driver"
	"bqbot/internal/harness"
	"bqbot/internal/store"
)

func TestComputerServiceRequiresExplicitHostConsentAndInputConfirmation(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	bot := store.Bot{ID: "computerbot", Name: "Computer", Engine: "echo"}
	if e = st.SaveBot(bot); e != nil {
		t.Fatal(e)
	}
	h := harness.New(st, &driver.Echo{})
	defer h.Close()
	server, e := Listen(h, "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer server.Close()
	api := NewClient(server.URL, server.Token)
	config := store.ComputerConfig{BotID: bot.ID, Provider: "host", URL: "http://127.0.0.1:1/mcp", Enabled: true}
	if _, e = api.SaveComputer(config); e == nil {
		t.Fatal("host consent not required")
	}
	config.HostConsent = true
	if _, e = api.SaveComputer(config); e != nil {
		t.Fatal(e)
	}
	_, e = api.ComputerAction(bot.ID, ComputerRequest{Action: computer.Action{Kind: "click", X: 1, Y: 1}})
	var httpErr *HTTPError
	if !errors.As(e, &httpErr) || httpErr.StatusCode != http.StatusForbidden {
		t.Fatal(e)
	}
	list, e := api.Computers()
	if e != nil || len(list) != 1 || list[0].BotID != bot.ID {
		t.Fatal(list, e)
	}
	config.Enabled = false
	if _, e = api.SaveComputer(config); e != nil {
		t.Fatal(e)
	}
	if _, e = api.ComputerAction(bot.ID, ComputerRequest{Action: computer.Action{Kind: "screenshot"}}); e == nil {
		t.Fatal("disabled screenshot allowed")
	}
	config.URL = ""
	config.Enabled = true
	if _, e = api.SaveComputer(config); e != nil {
		t.Fatal(e)
	}
	status, e := api.HostStatus()
	if e != nil || status.Available || status.Running || status.Error == "" {
		t.Fatal("headless host status", status, e)
	}
	if _, e = api.StartHost(); e == nil {
		t.Fatal("headless service launched host")
	}
	if _, e = api.StopHost(); e != nil {
		t.Fatal(e)
	}
	if _, e = api.ComputerAction(bot.ID, ComputerRequest{Action: computer.Action{Kind: "screenshot"}}); e == nil {
		t.Fatal("headless native action allowed")
	}
}
