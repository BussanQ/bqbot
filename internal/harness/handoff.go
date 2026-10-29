package harness

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"

	"bqbot/internal/driver"
	"bqbot/internal/mcp"
	"bqbot/internal/store"
)

// handoff is the loopback endpoint the bqbot MCP server calls back. Each
// turn that may ask other bots gets its own token, valid until the turn ends.
type handoff struct {
	h    *Harness
	exe  string
	url  string
	srv  *http.Server
	mu   sync.Mutex
	turn map[[32]byte]*turn // by sha256 of the token
}

// Listen starts the handoff endpoint on 127.0.0.1, so the bots' turns get
// the bqbot MCP tools. exe is the bqbot executable, which the engines start
// as `exe mcp <grant file>`.
func (h *Harness) Listen(exe string) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	hd := &handoff{h: h, exe: exe, url: "http://" + ln.Addr().String(), turn: map[[32]byte]*turn{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /list_bots", hd.serveList)
	mux.HandleFunc("POST /ask_bot", hd.serveAsk)
	hd.srv = &http.Server{Handler: mux}
	h.mu.Lock()
	h.hand = hd
	h.mu.Unlock()
	go hd.srv.Serve(ln)
	return nil
}

// Close stops the handoff endpoint.
func (h *Harness) Close() error {
	h.mu.Lock()
	hd := h.hand
	h.hand = nil
	h.mu.Unlock()
	if hd == nil {
		return nil
	}
	return hd.srv.Close()
}

// grant gives t a token, in a grant file only this user can read, and
// returns the MCP server command for the turn and a func that revokes both.
func (hd *handoff) grant(t *turn) (*driver.MCPServer, func(), error) {
	var b [32]byte
	rand.Read(b[:])
	token := hex.EncodeToString(b[:])
	f, err := os.CreateTemp("", "bqbot-grant-*.json") // mode 0600
	if err != nil {
		return nil, nil, err
	}
	err = json.NewEncoder(f).Encode(mcp.Grant{URL: hd.url, Token: token})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return nil, nil, err
	}
	key := sha256.Sum256([]byte(token))
	hd.mu.Lock()
	hd.turn[key] = t
	hd.mu.Unlock()
	revoke := func() {
		hd.mu.Lock()
		delete(hd.turn, key)
		hd.mu.Unlock()
		os.Remove(f.Name())
	}
	return &driver.MCPServer{Command: hd.exe, Args: []string{"mcp", f.Name()}}, revoke, nil
}

// caller is the turn whose token the request bears, nil for none.
func (hd *handoff) caller(r *http.Request) *turn {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		return nil
	}
	hd.mu.Lock()
	defer hd.mu.Unlock()
	return hd.turn[sha256.Sum256([]byte(token))]
}

func reply(w http.ResponseWriter, code int, text string, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	r := mcp.Reply{Text: text}
	if err != nil {
		r.Error = err.Error()
	}
	json.NewEncoder(w).Encode(r)
}

func (hd *handoff) serveList(w http.ResponseWriter, r *http.Request) {
	from := hd.caller(r)
	if from == nil {
		reply(w, http.StatusUnauthorized, "", errors.New("this turn may not use the bqbot tools"))
		return
	}
	bots, err := hd.h.Store.Bots()
	if err != nil {
		reply(w, http.StatusInternalServerError, "", err)
		return
	}
	var b strings.Builder
	for _, o := range bots {
		if o.ID == from.bot.ID {
			continue
		}
		b.WriteString("- " + o.Name)
		if hd.h.Busy(o.ID) {
			b.WriteString(" (busy)")
		}
		if p := strings.TrimSpace(o.Persona); p != "" {
			b.WriteString(": " + clip(p, 200))
		}
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		reply(w, http.StatusOK, "The person has no other bots.", nil)
		return
	}
	reply(w, http.StatusOK, b.String(), nil)
}

func (hd *handoff) serveAsk(w http.ResponseWriter, r *http.Request) {
	from := hd.caller(r)
	if from == nil {
		reply(w, http.StatusUnauthorized, "", errors.New("this turn may not use the bqbot tools"))
		return
	}
	var req mcp.AskRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		reply(w, http.StatusBadRequest, "", fmt.Errorf("bad request: %v", err))
		return
	}
	text, err := hd.h.ask(r.Context(), from, req.Name, req.Question)
	if err != nil {
		reply(w, http.StatusOK, "", err)
		return
	}
	reply(w, http.StatusOK, text, nil)
}

// ask runs a turn of the bot named name for the question of from, and
// returns its answer. The turn stops if ctx ends or from's turn ends first.
func (h *Harness) ask(ctx context.Context, from *turn, name, question string) (string, error) {
	if from.depth > 0 {
		return "", errors.New("a bot answering another bot cannot ask bots in turn")
	}
	target, err := h.botNamed(name)
	if err != nil {
		return "", err
	}
	if target.ID == from.bot.ID {
		return "", errors.New("you cannot ask yourself")
	}
	_, t, err := h.start(target, question, from)
	if errors.Is(err, ErrBusy) {
		h.note(from, "转交给 "+target.Name+" 失败：它正忙")
		return "", fmt.Errorf("%s is busy with another conversation; try again later or answer without it", target.Name)
	}
	if err != nil {
		h.note(from, "转交给 "+target.Name+" 失败")
		return "", err
	}
	h.note(from, "已转交给 "+target.Name)
	select {
	case <-t.done:
	case <-ctx.Done():
		t.cancel()
		<-t.done
	case <-from.done:
		t.cancel()
		<-t.done
	}
	switch {
	case errors.Is(t.err, context.Canceled):
		return "", fmt.Errorf("%s's turn was stopped before it answered", target.Name)
	case t.result.Role == "error":
		return "", fmt.Errorf("%s could not answer: %s", target.Name, t.result.Text)
	case t.err != nil:
		return "", fmt.Errorf("%s could not answer: %v", target.Name, t.err)
	case strings.TrimSpace(t.result.Text) == "":
		return "", fmt.Errorf("%s gave no answer", target.Name)
	}
	return t.result.Text, nil
}

// botNamed finds a bot by its name, ignoring case and outer spaces.
func (h *Harness) botNamed(name string) (store.Bot, error) {
	bots, err := h.Store.Bots()
	if err != nil {
		return store.Bot{}, err
	}
	name = strings.TrimSpace(name)
	var found []store.Bot
	for _, b := range bots {
		if strings.EqualFold(strings.TrimSpace(b.Name), name) {
			found = append(found, b)
		}
	}
	switch len(found) {
	case 0:
		return store.Bot{}, fmt.Errorf("no bot is named %q; list_bots shows their names", name)
	case 1:
		return found[0], nil
	}
	return store.Bot{}, fmt.Errorf("%d bots are named %q; ask the person to rename one", len(found), name)
}
