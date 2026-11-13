package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"bqbot/internal/store"
)

func TestWorkspaceStreamScopeAndRevocation(t *testing.T) {
	var renewed, closed atomic.Int32
	var negotiation atomic.Bool
	const remoteID = "11223344556677889900aabbccddeeff"
	workspace := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer workspace-secret" {
			t.Error("missing workspace credential")
			w.WriteHeader(401)
			return
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/streams":
			var offer struct{ Control bool }
			if err := json.NewDecoder(r.Body).Decode(&offer); err != nil {
				t.Error(err)
			}
			negotiation.Store(offer.Control)
			json.NewEncoder(w).Encode(map[string]any{"id": remoteID, "sdp": "answer", "control": offer.Control})
		case r.Method == "PUT" && r.URL.Path == "/streams/"+remoteID:
			renewed.Add(1)
			w.WriteHeader(204)
		case r.Method == "DELETE" && r.URL.Path == "/streams/"+remoteID:
			closed.Add(1)
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer workspace.Close()
	t.Setenv("WORKSPACE_TEST_KEY", "workspace-secret")
	_, api, _ := newTestService(t, nil)
	first, other := mustSave(t, api, "first"), mustSave(t, api, "other")
	cfg := store.ComputerConfig{BotID: first.ID, URL: workspace.URL, Provider: "workspace", Enabled: true, TokenEnv: "WORKSPACE_TEST_KEY"}
	if _, err := api.SaveComputer(cfg); err != nil {
		t.Fatal(err)
	}
	view, err := api.OpenWorkspaceStream(first.ID, "offer", false)
	if err != nil || view.SDP != "answer" || view.ID == remoteID || view.Control || negotiation.Load() {
		t.Fatal(view, err)
	}
	statusIs(t, api.UpdateWorkspaceStream(other.ID, view.ID, false), 404)
	if err = api.UpdateWorkspaceStream(first.ID, view.ID, false); err != nil || renewed.Load() != 1 {
		t.Fatal(err)
	}
	control, err := api.OpenWorkspaceStream(first.ID, "offer", true)
	if err != nil || !control.Control || !negotiation.Load() {
		t.Fatal("missing confirmed input negotiation", err)
	}
	if err = api.UpdateWorkspaceStream(first.ID, control.ID, true); err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = false
	if _, err = api.SaveComputer(cfg); err != nil || closed.Load() != 2 {
		t.Fatal(closed.Load(), err)
	}
	statusIs(t, api.UpdateWorkspaceStream(first.ID, view.ID, false), 404)
	if _, err = api.OpenWorkspaceStream(first.ID, "offer", false); err == nil {
		t.Fatal("disabled workspace opened")
	}
}
