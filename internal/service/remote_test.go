package service

import (
	"net/http"
	"testing"

	"bqbot/internal/remote"
)

func TestRemoteManagementLifecycle(t *testing.T) {
	server, client, _ := newTestService(t, nil)
	bot := mustSave(t, client, "remote-bot")
	state, err := client.RemoteState()
	if err != nil || state.Enabled {
		t.Fatal(state, err)
	}
	_, err = client.PairRemote([]string{bot.ID})
	statusIs(t, err, http.StatusBadRequest)
	_, err = client.EnableRemote("http://insecure.example")
	statusIs(t, err, http.StatusBadRequest)
	gateway, err := remote.Listen(client, "127.0.0.1:0", "https://chat.example")
	if err != nil {
		t.Fatal(err)
	}
	server.remoteMu.Lock()
	server.remote = gateway
	server.remoteMu.Unlock()
	state, err = client.RemoteState()
	if err != nil || !state.Enabled || state.Origin != "https://chat.example" {
		t.Fatal(state, err)
	}
	code, err := client.PairRemote([]string{bot.ID})
	if err != nil || len(code) != 10 {
		t.Fatal("pairing failed", err)
	}
	if err = client.RevokeRemote(); err != nil {
		t.Fatal(err)
	}
	if err = client.DisableRemote(); err != nil {
		t.Fatal(err)
	}
	state, err = client.RemoteState()
	if err != nil || state.Enabled {
		t.Fatal(state, err)
	}
	if err = client.DisableRemote(); err != nil {
		t.Fatal("disable should be idempotent", err)
	}
}
