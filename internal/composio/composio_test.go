package composio

import (
	"context"
	"encoding/json"
	"github.com/egoist/mygo"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type memorySecrets map[string][]byte

func (s memorySecrets) Get(k string) ([]byte, error) {
	v, ok := s[k]
	if !ok {
		return nil, mygo.ErrSecretNotFound
	}
	return v, nil
}
func (s memorySecrets) Set(k string, v []byte) error { s[k] = append([]byte{}, v...); return nil }
func (s memorySecrets) Delete(k string) error        { delete(s, k); return nil }
func TestConnectionAndSecretLifecycle(t *testing.T) {
	key := "private-test-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != key {
			t.Error("missing credential")
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/connected_accounts/link":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["user_id"] != "user-1" || body["auth_config_id"] != "ac-test" {
				t.Error("invalid connection request")
			}
			json.NewEncoder(w).Encode(Link{URL: "https://connect.composio.dev/link", AccountID: "account-1"})
		case "/connected_accounts":
			if r.URL.Query().Get("user_ids") != "user-1" {
				t.Error("missing account filter")
			}
			w.Write([]byte(`{"items":[{"id":"account-1","status":"ACTIVE","toolkit":{"slug":"github"}}]}`))
		default:
			w.WriteHeader(500)
			w.Write([]byte(key))
		}
	}))
	defer server.Close()
	c := &Client{Secrets: memorySecrets{}, BaseURL: server.URL, HTTP: server.Client()}
	if configured, err := c.Configured(); err != nil || configured {
		t.Fatal(configured, err)
	}
	if err := c.SaveKey(key); err != nil {
		t.Fatal(err)
	}
	link, err := c.Connect(context.Background(), "user-1", "ac-test")
	if err != nil || link.AccountID != "account-1" {
		t.Fatal(link, err)
	}
	accounts, err := c.Accounts(context.Background(), "user-1")
	if err != nil || len(accounts) != 1 || accounts[0].Toolkit.Slug != "github" {
		t.Fatal(accounts, err)
	}
	var v any
	if err = c.request(context.Background(), "GET", "/error", nil, &v); err == nil || strings.Contains(err.Error(), key) {
		t.Fatal("upstream credential leak", err)
	}
	if err = c.DeleteKey(); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Headers(); err == nil {
		t.Fatal("deleted key still usable")
	}
}
func TestRedirectCannotLeakKey(t *testing.T) {
	called := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer source.Close()
	c := &Client{Secrets: memorySecrets{}, BaseURL: source.URL, HTTP: source.Client()}
	c.SaveKey("test-key")
	_, err := c.Accounts(context.Background(), "u")
	if err == nil || called {
		t.Fatal("credential request followed redirect")
	}
}
