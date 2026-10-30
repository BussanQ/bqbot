package control

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"bqbot/internal/driver"
	"bqbot/internal/harness"
	"bqbot/internal/service"
	"bqbot/internal/store"
)

func TestControlVocabularyAndCredentialRedaction(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := harness.New(st, &driver.Echo{})
	defer h.Close()
	s, err := service.Listen(h, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	st.SaveBot(store.Bot{ID: "echo", Name: "Echo", Engine: "echo", Cursor: "PRIVATE-CURSOR", Persona: "PRIVATE-PERSONA"})
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_bots","arguments":{}}}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"approve","arguments":{}}}
`
	var out bytes.Buffer
	if err := Serve(context.Background(), strings.NewReader(input), &out, service.NewClient(s.URL, s.Token)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "PRIVATE") || strings.Contains(out.String(), s.Token) {
		t.Fatal("control leaked local metadata")
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatal(out.String())
	}
	var list struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Result.Tools) != 4 {
		t.Fatalf("wrong vocabulary %s", lines[0])
	}
	if !strings.Contains(lines[2], `"isError":true`) {
		t.Fatal("approval tool was not rejected")
	}
}
