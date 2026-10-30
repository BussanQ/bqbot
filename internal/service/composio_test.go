package service

import (
	"bqbot/internal/composio"
	"bqbot/internal/driver"
	"bqbot/internal/harness"
	"bqbot/internal/store"
	"encoding/json"
	"github.com/egoist/mygo"
	"strings"
	"testing"
)

type composioTestSecrets map[string][]byte

func (s composioTestSecrets) Get(k string) ([]byte, error) {
	v, ok := s[k]
	if !ok {
		return nil, mygo.ErrSecretNotFound
	}
	return v, nil
}
func (s composioTestSecrets) Set(k string, v []byte) error { s[k] = v; return nil }
func (s composioTestSecrets) Delete(k string) error        { delete(s, k); return nil }
func TestComposioAPIKeepsKeyOutOfState(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := harness.New(st, &driver.Echo{})
	defer h.Close()
	s, err := Listen(h, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// No request is made before replacing the test's keychain adapter.
	s.composio = &composio.Client{Secrets: composioTestSecrets{}}
	c := NewClient(s.URL, s.Token)
	key := "never-return-this-key"
	if err = c.SaveComposioKey(key); err != nil {
		t.Fatal(err)
	}
	settings := store.ComposioSettings{UserID: "test-user", AuthConfigID: "ac-test", MCPURL: "https://mcp.composio.dev/server-test", Enabled: true}
	if err = c.SaveComposio(settings); err != nil {
		t.Fatal(err)
	}
	v, err := c.Composio()
	if err != nil || !v.Configured || v.Settings.UserID != "test-user" {
		t.Fatal(v, err)
	}
	data, _ := json.Marshal(v)
	if strings.Contains(string(data), key) {
		t.Fatal("key leaked to UI")
	}
	if err = c.DeleteComposioKey(); err != nil {
		t.Fatal(err)
	}
	v, err = c.Composio()
	if err != nil || v.Configured {
		t.Fatal(v, err)
	}
	settings.MCPURL = "https://untrusted.example/mcp"
	if err = c.SaveComposio(settings); err == nil {
		t.Fatal("Composio key can be sent to arbitrary host")
	}
}
