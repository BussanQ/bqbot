package service

import (
	"bqbot/internal/driver"
	"bqbot/internal/harness"
	"bqbot/internal/store"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAutomationHTTPWebhookAuthHistoryAndRotation(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := harness.New(st, &driver.Echo{Delay: time.Millisecond})
	defer h.Close()
	server, err := Listen(h, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	api := NewClient(server.URL, server.Token)
	if err = api.SaveBot(store.Bot{ID: "worker", Name: "Worker", Engine: "echo"}); err != nil {
		t.Fatal(err)
	}
	routine, err := api.SaveRoutine(store.Routine{Name: "Hook", Enabled: true, BotID: "worker", Kind: "webhook", Prompt: "process event"})
	if err != nil {
		t.Fatal(err)
	}
	key, err := api.RotateWebhookKey()
	if err != nil {
		t.Fatal(err)
	}
	if err = server.enableWebhook("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	state, err := api.WebhookState()
	if err != nil || !state.Running || !state.Configured {
		t.Fatal(state, err)
	}
	data, _ := json.Marshal(state)
	if strings.Contains(string(data), key["token"]) {
		t.Fatal("token leaked through state")
	}
	call := func(method, path, token, body string) int {
		req, _ := http.NewRequest(method, state.URL+path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		io.Copy(io.Discard, response.Body)
		return response.StatusCode
	}
	if got := call("GET", "/health", "", ""); got != 401 {
		t.Fatal(got)
	}
	if got := call("GET", "/health", key["token"], ""); got != 200 {
		t.Fatal(got)
	}
	if got := call("GET", "/v1/bots", key["token"], ""); got != 404 {
		t.Fatal("webhook exposed service", got)
	}
	if got := call("POST", "/hooks/"+routine.ID, key["token"], `{"text":"event context"}`); got != 202 {
		t.Fatal(got)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		runs, err := api.RoutineHistory(routine.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) == 1 && runs[0].Status == "completed" {
			if !strings.Contains(runs[0].Result, "event context") {
				t.Fatal(runs)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("webhook did not finish", runs)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = api.RotateWebhookKey(); err != nil {
		t.Fatal(err)
	}
	if got := call("GET", "/health", key["token"], ""); got != 401 {
		t.Fatal("old token still accepted", got)
	}
	if err = api.DisableWebhook(); err != nil {
		t.Fatal(err)
	}
	state, err = api.WebhookState()
	if err != nil || state.Running {
		t.Fatal(state, err)
	}
	if err = api.DeleteRoutine(routine.ID); err != nil {
		t.Fatal(err)
	}
}
