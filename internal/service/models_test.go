package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"bqbot/internal/driver"
	"bqbot/internal/store"
)

func TestModelOptionsStayOnCapturedAgent(t *testing.T) {
	_, api, _ := newTestService(t, &driver.Builtin{})
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "" {
			t.Error("unexpected model request")
		}
		w.Write([]byte(`{"data":[{"id":"lab/deep","supported_reasoning_efforts":["high","extended"]},{"id":"lab/plain"}]}`))
	}))
	defer models.Close()
	t.Setenv("OPENAI_API_KEY", "not-for-this-endpoint")
	if err := api.SaveSettings(map[string]driver.EngineSettings{"bqbot": {BaseURL: models.URL + "/v1"}}); err != nil {
		t.Fatal(err)
	}
	list, err := api.ModelCatalog("bqbot")
	if err != nil || list.Source != "agent" || len(list.Models) != 2 {
		t.Fatal(list, err)
	}
	opts := store.Bot{Engine: "bqbot", Model: "lab/deep", Effort: "extended", ApprovalMode: "ask"}
	if err = api.SetModelOptions("bqbot", opts); err != nil {
		t.Fatal(err)
	}
	opts.Engine = "claude"
	if api.SetModelOptions("bqbot", opts) == nil {
		t.Fatal("stale engine changed the bot")
	}
	opts.Engine = "bqbot"
	opts.Model = "lab/plain"
	if api.SetModelOptions("bqbot", opts) == nil {
		t.Fatal("unsupported effort was saved")
	}
	bots, err := api.Bots()
	if err != nil || len(bots) != 1 || bots[0].Name != "bqbot" || bots[0].Model != "lab/deep" || bots[0].Effort != "extended" || bots[0].Persona == "" {
		t.Fatal(bots, err)
	}
	if err = api.DeleteBot("bqbot"); err != nil {
		t.Fatal(err)
	}
	if api.SetModelOptions("bqbot", opts) == nil {
		t.Fatal("recreated deleted bot")
	}
}
