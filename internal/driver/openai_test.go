package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIStreamingHistoryImages(t *testing.T) {
	t.Setenv("BQBOT_TEST_API_KEY", "test-secret")
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Errorf("request path/header %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": heartbeat\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	image := Attachment{Name: "a.png", MIME: "image/png", Data: "aGk="}
	events, err := collect(t, &OpenAI{}, TurnInput{
		System: "Be brief", Text: "now", Cursor: "ignored", Effort: "low", Attachments: []Attachment{image},
		History:  []HistoryMessage{{Role: "user", Text: "before", Attachments: []Attachment{image}}, {Role: "assistant", Text: "then"}},
		Settings: EngineSettings{BaseURL: server.URL + "/v1", APIKeyEnv: "BQBOT_TEST_API_KEY", Models: []string{"custom"}},
	})
	if err != nil || text(events) != "Hello" {
		t.Fatalf("events=%v err=%v", events, err)
	}
	if received["model"] != "custom" || received["reasoning_effort"] != "low" || received["stream"] != true {
		t.Fatalf("request %v", received)
	}
	messages := received["messages"].([]any)
	if len(messages) != 4 {
		t.Fatalf("messages %v", messages)
	}
	encoded, _ := json.Marshal(messages)
	if strings.Count(string(encoded), "data:image/png;base64,aGk=") != 2 {
		t.Fatalf("images absent: %s", encoded)
	}
}

func TestOpenAIFailureDoesNotExpireSession(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{"http", 401, "secret body"},
		{"truncated", 200, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"},
		{"provider", 200, "data: {\"error\":{\"message\":\"failed\"}}\n\n"},
		{"tool", 200, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"shell\"}}]}}]}\n\n"},
		{"length", 200, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}]}\n\n"},
		{"malformed", 200, "data: {bad json}\n\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(test.status); fmt.Fprint(w, test.body) }))
			defer server.Close()
			_, err := collect(t, &API{}, TurnInput{Settings: EngineSettings{BaseURL: server.URL}})
			if !errors.Is(err, ErrTurnFailed) || errors.Is(err, ErrSessionExpired) || strings.Contains(err.Error(), "secret body") {
				t.Fatalf("error %v", err)
			}
		})
	}
}

func TestOpenAIStreamFraming(t *testing.T) {
	for _, stream := range []string{
		"data: {\"choices\":[\r\ndata: {\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\r\n\r\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]",
		"data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}",
	} {
		t.Run(stream, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, stream) }))
			defer server.Close()
			events, err := collect(t, &OpenAI{}, TurnInput{Settings: EngineSettings{BaseURL: server.URL}})
			if err != nil || text(events) != "ok" {
				t.Fatalf("events=%v err=%v", events, err)
			}
		})
	}
}

func TestOpenAICancelFinalChunk(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"last\"},\"finish_reason\":\"stop\"}]}\n\n")
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := (&OpenAI{}).Run(ctx, TurnInput{Settings: EngineSettings{BaseURL: server.URL}}, func(Event) { cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error %v", err)
	}
}

func TestOpenAICancel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := (&OpenAI{}).Run(ctx, TurnInput{Settings: EngineSettings{BaseURL: server.URL}}, func(Event) { cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error %v", err)
	}
}

func TestOpenAIValidation(t *testing.T) {
	for _, a := range []Attachment{{MIME: "text/plain", Data: "aGk="}, {MIME: "image/png", Data: "bad"}, {MIME: "image/png"}} {
		if _, err := imageDataURL(a); err == nil {
			t.Fatalf("accepted %+v", a)
		}
	}
	t.Setenv("BQBOT_MISSING_API_KEY", "")
	_, err := collect(t, &OpenAI{}, TurnInput{Settings: EngineSettings{APIKeyEnv: "BQBOT_MISSING_API_KEY"}})
	if err == nil {
		t.Fatal("accepted missing configured API key")
	}
}

func TestCatalogConfiguredModels(t *testing.T) {
	models := Catalog("grok", EngineSettings{Models: []string{" custom ", "", "custom", "other"}})
	if len(models) != 2 || models[0].ID != "custom" || models[1].Engine != "grok" {
		t.Fatal(models)
	}
	models = Catalog("codex", EngineSettings{})
	models[0].Efforts[0] = "changed"
	if Catalog("codex", EngineSettings{})[0].Efforts[0] == "changed" {
		t.Fatal("catalog returned shared slices")
	}
}
