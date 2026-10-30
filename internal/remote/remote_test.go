package remote

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bqbot/internal/driver"
	"bqbot/internal/store"
)

type fakeBackend struct{ sent string }

func (f *fakeBackend) Bots() ([]store.Bot, error) {
	return []store.Bot{{ID: "one", Name: "One", Persona: "private"}, {ID: "two", Name: "Two"}}, nil
}
func (f *fakeBackend) Transcript(id string) ([]store.Message, error) {
	return []store.Message{{Role: "assistant", Text: "hello"}}, nil
}
func (f *fakeBackend) Send(id, text string, _ []driver.Attachment, _ string) (store.Message, error) {
	f.sent = id
	return store.Message{ID: "message"}, nil
}
func request(g *Gateway, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://chat.example"+path, strings.NewReader(body))
	r.Header.Set("Origin", "https://chat.example")
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	return w
}
func TestPairScopeAndRevocation(t *testing.T) {
	backend := &fakeBackend{}
	g, err := Listen(backend, "127.0.0.1:0", "https://chat.example")
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	code, err := g.PairCode([]string{"one"})
	if err != nil {
		t.Fatal(err)
	}
	paired := request(g, "POST", "/pair", `{"code":"`+code+`"}`, nil)
	if paired.Code != 200 {
		t.Fatal(paired.Code)
	}
	cookie := paired.Result().Cookies()[0]
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("insecure cookie")
	}
	if got := request(g, "POST", "/pair", `{"code":"`+code+`"}`, nil).Code; got != 401 {
		t.Fatal("code reused", got)
	}
	bots := request(g, "GET", "/bots", "", cookie)
	if bots.Code != 200 || strings.Contains(bots.Body.String(), "private") || strings.Contains(bots.Body.String(), "Two") {
		t.Fatal(bots.Body.String())
	}
	if got := request(g, "POST", "/send", `{"bot":"two","text":"hello"}`, cookie).Code; got != 403 {
		t.Fatal("scope escaped", got)
	}
	if got := request(g, "POST", "/send", `{"bot":"one","text":"hello"}`, cookie).Code; got != 202 || backend.sent != "one" {
		t.Fatal(got)
	}
	for _, path := range []string{"/v1/settings", "/v1/bots/one/approvals/id", "/v1/remote"} {
		if got := request(g, "GET", path, "", cookie).Code; got != 404 {
			t.Fatal("privileged endpoint", path, got)
		}
	}
	r := httptest.NewRequest("POST", "https://chat.example/send", strings.NewReader(`{"bot":"one","text":"hello"}`))
	r.AddCookie(cookie)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("missing origin accepted")
	}
	g.Revoke()
	if got := request(g, "GET", "/bots", "", cookie).Code; got != 401 {
		t.Fatal("revoked session", got)
	}
}
func TestPairLimitsAndExpiry(t *testing.T) {
	g, err := Listen(&fakeBackend{}, "127.0.0.1:0", "https://chat.example")
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if _, err := g.PairCode([]string{"missing"}); err == nil {
		t.Fatal("unknown bot")
	}
	code, _ := g.PairCode([]string{"one"})
	for range 5 {
		request(g, "POST", "/pair", `{"code":"wrong"}`, nil)
	}
	if got := request(g, "POST", "/pair", `{"code":"`+code+`"}`, nil).Code; got != 401 {
		t.Fatal("attempt limit", got)
	}
	code, _ = g.PairCode([]string{"one"})
	g.expires = time.Now().Add(-time.Second)
	if got := request(g, "POST", "/pair", `{"code":"`+code+`"}`, nil).Code; got != 401 {
		t.Fatal("expired code", got)
	}
	for _, origin := range []string{"http://example.com", "https://user:secret@example.com", "https://example.com/path"} {
		if bad, err := Listen(&fakeBackend{}, "127.0.0.1:0", origin); err == nil {
			bad.Close()
			t.Fatal(origin)
		}
	}
	data, _ := json.Marshal(g.State())
	if strings.Contains(string(data), "code") {
		t.Fatal("code in state")
	}
}
