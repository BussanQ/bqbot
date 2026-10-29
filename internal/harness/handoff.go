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
	"time"

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
	turn map[[32]byte]grantTurn // by sha256 of the token
}

type grantTurn struct {
	turn         *turn
	approvalOnly bool
}

// Listen starts the handoff endpoint on 127.0.0.1, so the bots' turns get
// the bqbot MCP tools. exe is the bqbot executable, which the engines start
// as `exe mcp <grant file>`.
func (h *Harness) Listen(exe string) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	hd := &handoff{h: h, exe: exe, url: "http://" + ln.Addr().String(), turn: map[[32]byte]grantTurn{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /list_bots", hd.serveList)
	mux.HandleFunc("POST /ask_bot", hd.serveAsk)
	mux.HandleFunc("POST /approve", hd.serveApprove)
	hd.srv = &http.Server{Handler: mux}
	h.mu.Lock()
	if h.closed || h.hand != nil {
		h.mu.Unlock()
		ln.Close()
		return errors.New("harness is closed or already listening")
	}
	h.hand = hd
	h.mu.Unlock()
	go hd.srv.Serve(ln)
	return nil
}

// Close stops the handoff endpoint.
func (h *Harness) Close() error {
	h.mu.Lock()
	h.closed = true
	hd := h.hand
	h.hand = nil
	var waits []<-chan struct{}
	for id, t := range h.running {
		h.stopLocked(id)
		waits = append(waits, t.done)
	}
	h.mu.Unlock()
	for _, done := range waits {
		<-done
	}
	var err error
	if hd != nil {
		err = hd.srv.Close()
	}
	h.mu.Lock()
	for s := range h.subscribers {
		s.close()
		delete(h.subscribers, s)
	}
	if h.legacy != nil {
		h.legacy.close()
	}
	h.mu.Unlock()
	return err
}

// grant gives t a token, in a grant file only this user can read, and
// returns the MCP server command for the turn and a func that revokes both.
func (hd *handoff) grant(t *turn, approvalOnly bool) (*driver.MCPServer, func(), error) {
	var b [32]byte
	rand.Read(b[:])
	token := hex.EncodeToString(b[:])
	f, err := os.CreateTemp("", "bqbot-grant-*.json") // mode 0600
	if err != nil {
		return nil, nil, err
	}
	err = json.NewEncoder(f).Encode(struct {
		URL          string `json:"url"`
		Token        string `json:"token"`
		ApprovalOnly bool   `json:"approval_only"`
	}{hd.url, token, approvalOnly})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return nil, nil, err
	}
	key := sha256.Sum256([]byte(token))
	hd.mu.Lock()
	hd.turn[key] = grantTurn{t, approvalOnly}
	hd.mu.Unlock()
	var once sync.Once
	revoke := func() {
		once.Do(func() {
			hd.mu.Lock()
			delete(hd.turn, key)
			hd.mu.Unlock()
			os.Remove(f.Name())
		})
	}
	return &driver.MCPServer{Command: hd.exe, Args: []string{"mcp", f.Name()}}, revoke, nil
}

// caller is the turn whose token the request bears, nil for none.
func (hd *handoff) caller(r *http.Request, approval bool) *turn {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		return nil
	}
	hd.mu.Lock()
	defer hd.mu.Unlock()
	g := hd.turn[sha256.Sum256([]byte(token))]
	if g.turn == nil || g.turn.ctx.Err() != nil || g.approvalOnly != approval {
		return nil
	}
	return g.turn
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
	from := hd.caller(r, false)
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
	from := hd.caller(r, false)
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
	if strings.TrimSpace(question) == "" {
		return "", errors.New("empty question")
	}
	target, err := h.botNamed(name)
	if err != nil {
		return "", err
	}
	if target.ID == from.bot.ID {
		return "", errors.New("you cannot ask yourself")
	}
	h.mu.Lock()
	if ctx.Err() != nil || from.ctx.Err() != nil || h.running[from.bot.ID] != from {
		h.mu.Unlock()
		return "", context.Canceled
	}
	if h.waitCycleLocked(h.running[target.ID], from, map[*turn]bool{}) {
		h.mu.Unlock()
		return "", errors.New("handoff would create a cyclic wait")
	}
	t := newTurn(from.ctx, target, store.Message{ID: store.NewID(), Role: "user", Text: strings.TrimSpace(question), At: time.Now()}, from)
	t.requestCtx = ctx
	stopCancellation := context.AfterFunc(ctx, t.cancel)
	defer stopCancellation()
	if err = h.enqueueLocked(t); err != nil {
		h.mu.Unlock()
		return "", err
	}
	from.waiting[t] = true
	h.mu.Unlock()
	defer func() { h.mu.Lock(); delete(from.waiting, t); h.mu.Unlock() }()
	h.note(from, "已转交给 "+target.Name)
	select {
	case <-t.done:
	case <-ctx.Done():
		h.cancelWork(t)
		<-t.done
	case <-from.ctx.Done():
		h.cancelWork(t)
		<-t.done
	}
	switch {
	case errors.Is(t.err, context.Canceled):
		return "", fmt.Errorf("%s's turn was stopped before it answered", target.Name)
	case t.err != nil:
		return "", fmt.Errorf("%s could not answer: %v", target.Name, t.err)
	case t.result.Role == "error":
		return "", fmt.Errorf("%s could not answer: %s", target.Name, t.result.Text)
	case strings.TrimSpace(t.result.Text) == "":
		return "", fmt.Errorf("%s gave no answer", target.Name)
	}
	return t.result.Text, nil
}
func (h *Harness) cancelWork(t *turn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	t.cancel()
	if h.running[t.bot.ID] == t {
		h.cancelApprovalsLocked(t)
	} else {
		h.cancelQueuedLocked(t)
	}
}

// A depth-one job cannot delegate, but two root turns can each wait for a
// queued job behind the other. Reject the edge that would close that cycle.
func (h *Harness) waitCycleLocked(t, goal *turn, seen map[*turn]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	if t == goal {
		return true
	}
	seen[t] = true
	for child := range t.waiting {
		if h.waitCycleLocked(h.running[child.bot.ID], goal, seen) {
			return true
		}
	}
	return false
}

func (hd *handoff) serveApprove(w http.ResponseWriter, r *http.Request) {
	t := hd.caller(r, true)
	if t == nil {
		reply(w, http.StatusUnauthorized, "", errors.New("this turn may not request approvals"))
		return
	}
	var req driver.ApprovalRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		reply(w, http.StatusBadRequest, "", err)
		return
	}
	decision, err := hd.h.approval(r.Context(), t, req)
	if err != nil {
		reply(w, http.StatusConflict, "", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(decision)
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
