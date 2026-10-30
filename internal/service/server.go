// Package service exposes the harness over authenticated loopback HTTP and SSE.
package service

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"bqbot/internal/composio"
	"bqbot/internal/driver"
	"bqbot/internal/harness"
	"bqbot/internal/remote"
	"bqbot/internal/store"
)

// DefaultAddr is the embedded and standalone service's default listen address.
const DefaultAddr = "127.0.0.1:8799"

// Server owns an HTTP listener, but does not own or stop the harness.
// Token is a credential: callers must never log it or put it in argv.
type Server struct {
	remoteMu     sync.Mutex
	remote       *remote.Gateway
	URL          string
	Token        string
	h            *harness.Harness
	http         *http.Server
	key          [32]byte
	mu           sync.Mutex
	closed       bool
	discovery    map[string]discoveryFile
	composio     *composio.Client
	automation   *harness.Automation
	webhook      *http.Server
	webhookURL   string
	webhookError string
}

// Engine describes an installed driver and its configured model catalog.
type Engine struct {
	Name      string         `json:"name"`
	Available bool           `json:"available"`
	Version   string         `json:"version"`
	Models    []driver.Model `json:"models"`
}

// Listen starts a service on a literal loopback address (or localhost).
// An empty addr uses DefaultAddr; port 0 requests an ephemeral port.
func Listen(h *harness.Harness, addr string) (*Server, error) {
	if h == nil || h.Store == nil {
		return nil, errors.New("harness and store are required")
	}
	if addr == "" {
		addr = DefaultAddr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, errors.New("invalid listen address")
	}
	if host == "localhost" {
		host = "127.0.0.1"
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("service must listen on loopback")
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, err
	}
	s := &Server{URL: "http://" + ln.Addr().String(), Token: hex.EncodeToString(secret[:]), h: h, discovery: make(map[string]discoveryFile), composio: composio.New()}
	s.key = sha256.Sum256([]byte(s.Token))
	s.automation, err = harness.NewAutomation(h)
	if err != nil {
		ln.Close()
		return nil, err
	}
	webhookConfig, err := h.Store.WebhookConfig()
	if err != nil {
		s.automation.Close()
		ln.Close()
		return nil, err
	}
	if webhookConfig.Enabled {
		if err := s.enableWebhook("127.0.0.1:8800"); err != nil {
			s.webhookError = err.Error()
		}
	}
	s.http = &http.Server{Handler: s.handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	go s.http.Serve(ln)
	return s, nil
}

// Close disconnects clients and removes discovery files still owned by this server.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	err := s.http.Close()
	s.remoteMu.Lock()
	if s.remote != nil {
		err = errors.Join(err, s.remote.Close())
		s.remote = nil
	}
	s.remoteMu.Unlock()
	if s.webhook != nil {
		err = errors.Join(err, s.webhook.Close())
		s.webhook = nil
	}
	if s.automation != nil {
		s.automation.Close()
	}
	for path, file := range s.discovery {
		if e := s.removeDiscovery(path, file); e != nil {
			err = errors.Join(err, e)
		}
	}
	return err
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	s.phase3Routes(mux)
	s.composioRoutes(mux)
	s.integrationRoutes(mux)
	s.automationRoutes(mux)
	s.remoteRoutes(mux)
	s.teamRoutes(mux)
	s.voiceRoutes(mux)
	s.computerRoutes(mux)
	mux.HandleFunc("GET /v1/bots", func(w http.ResponseWriter, r *http.Request) { v, e := s.h.Store.Bots(); result(w, v, e) })
	mux.HandleFunc("PUT /v1/bots", s.saveBot)
	mux.HandleFunc("DELETE /v1/bots/{bot}", s.withBot(func(w http.ResponseWriter, r *http.Request, b store.Bot) { result(w, struct{}{}, s.h.DeleteBot(b.ID)) }))
	mux.HandleFunc("GET /v1/bots/{bot}/transcript", s.withBot(func(w http.ResponseWriter, r *http.Request, b store.Bot) {
		v, e := s.h.Store.Transcript(b.ID)
		result(w, v, e)
	}))
	mux.HandleFunc("GET /v1/bots/{bot}/handoffs/{asker}", s.withBot(func(w http.ResponseWriter, r *http.Request, b store.Bot) {
		if _, err := s.bot(r.PathValue("asker")); err != nil {
			fail(w, err)
			return
		}
		v, e := s.h.Store.HandoffTranscript(b.ID, r.PathValue("asker"))
		result(w, v, e)
	}))
	mux.HandleFunc("GET /v1/bots/{bot}/workdir", s.withBot(func(w http.ResponseWriter, r *http.Request, b store.Bot) {
		v, e := s.h.Store.WorkDir(b.ID)
		result(w, v, e)
	}))
	mux.HandleFunc("POST /v1/bots/{bot}/send", s.withBot(s.send))
	mux.HandleFunc("POST /v1/bots/{bot}/stop", s.withBot(func(w http.ResponseWriter, r *http.Request, b store.Bot) {
		var req struct{}
		if !decode(w, r, &req) {
			return
		}
		s.h.Stop(b.ID)
		result(w, struct{}{}, nil)
	}))
	mux.HandleFunc("POST /v1/bots/{bot}/approvals/{approval}", s.withBot(s.approve))
	mux.HandleFunc("GET /v1/settings", func(w http.ResponseWriter, r *http.Request) { v, e := s.h.Store.Settings(); result(w, v, e) })
	mux.HandleFunc("PUT /v1/settings", s.saveSettings)
	mux.HandleFunc("GET /v1/engines", s.engines)
	mux.HandleFunc("GET /v1/snapshot", func(w http.ResponseWriter, r *http.Request) { v, e := s.h.FullSnapshot(); result(w, v, e) })
	mux.HandleFunc("GET /v1/events", s.events)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fail(w, problem{http.StatusNotFound, "endpoint not found"})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		key := sha256.Sum256([]byte(token))
		if subtle.ConstantTimeCompare(key[:], s.key[:]) != 1 || !ok {
			fail(w, problem{http.StatusUnauthorized, "unauthorized"})
			return
		}
		// Native clients need no CORS. Reject browser origins and DNS-rebinding hosts.
		if r.Header.Get("Origin") != "" || "http://"+r.Host != s.URL {
			fail(w, problem{http.StatusForbidden, "origin or host is not allowed"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

type problem struct {
	status  int
	message string
}

func (e problem) Error() string    { return e.message }
func invalid(message string) error { return problem{http.StatusBadRequest, message} }
func fail(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	var p problem
	if errors.As(err, &p) {
		status = p.status
	}
	if errors.Is(err, harness.ErrBusy) {
		status = http.StatusConflict
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
func result(w http.ResponseWriter, v any, err error) {
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

const maxRequestBytes = 18 << 20

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if ct := strings.Split(r.Header.Get("Content-Type"), ";")[0]; ct != "application/json" {
		fail(w, problem{http.StatusUnsupportedMediaType, "application/json required"})
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	err := d.Decode(v)
	if err == nil {
		var extra any
		if e := d.Decode(&extra); e != io.EOF {
			if e != nil {
				err = e
			} else {
				err = errors.New("expected a single JSON value")
			}
		}
	}
	if err != nil {
		var limit *http.MaxBytesError
		status := http.StatusBadRequest
		if errors.As(err, &limit) {
			status = http.StatusRequestEntityTooLarge
		}
		fail(w, problem{status, "invalid or oversized JSON request"})
		return false
	}
	return true
}

func (s *Server) bot(id string) (store.Bot, error) {
	if !validID(id) {
		return store.Bot{}, invalid("invalid bot ID")
	}
	bots, err := s.h.Store.Bots()
	if err != nil {
		return store.Bot{}, err
	}
	for _, b := range bots {
		if b.ID == id {
			return b, nil
		}
	}
	return store.Bot{}, problem{http.StatusNotFound, "bot not found"}
}
func (s *Server) withBot(fn func(http.ResponseWriter, *http.Request, store.Bot)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, e := s.bot(r.PathValue("bot"))
		if e != nil {
			fail(w, e)
			return
		}
		fn(w, r, b)
	}
}

func (s *Server) saveBot(w http.ResponseWriter, r *http.Request) {
	var b store.Bot
	if !decode(w, r, &b) {
		return
	}
	settings, err := s.h.Store.Settings()
	if err == nil {
		err = s.validateBot(b, settings)
	}
	if err == nil {
		err = s.h.SaveProfile(b)
	}
	result(w, struct{}{}, err)
}

type sendRequest struct {
	Text        string              `json:"text"`
	Attachments []driver.Attachment `json:"attachments,omitempty"`
	Mode        string              `json:"mode,omitempty"`
}

func (s *Server) send(w http.ResponseWriter, r *http.Request, b store.Bot) {
	var req sendRequest
	if !decode(w, r, &req) {
		return
	}
	if err := validateSend(req); err != nil {
		fail(w, err)
		return
	}
	settings, err := s.h.Store.Settings()
	if err == nil {
		err = s.validateBot(b, settings)
	}
	if err != nil {
		fail(w, err)
		return
	}
	v, err := s.h.Submit(b, req.Text, req.Attachments, req.Mode)
	result(w, v, err)
}
func (s *Server) approve(w http.ResponseWriter, r *http.Request, b store.Bot) {
	id := r.PathValue("approval")
	if !safeText(id, 256) || id == "" {
		fail(w, invalid("invalid approval ID"))
		return
	}
	var d driver.ApprovalDecision
	if !decode(w, r, &d) {
		return
	}
	if d.Outcome != "allow" && d.Outcome != "deny" && d.Outcome != "answer" {
		fail(w, invalid("invalid approval outcome"))
		return
	}
	if len(d.Answer) > 1<<20 || (d.Outcome != "answer" && d.Answer != "") {
		fail(w, invalid("invalid approval answer"))
		return
	}
	if err := s.h.Approve(b.ID, id, d); err != nil {
		fail(w, problem{http.StatusConflict, err.Error()})
		return
	}
	result(w, struct{}{}, nil)
}
func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	var settings map[string]driver.EngineSettings
	if !decode(w, r, &settings) {
		return
	}
	if err := s.validateSettings(settings); err != nil {
		fail(w, err)
		return
	}
	result(w, struct{}{}, s.h.Store.SaveSettings(settings))
}
func (s *Server) engines(w http.ResponseWriter, r *http.Request) {
	settings, err := s.h.Store.Settings()
	if err != nil {
		fail(w, err)
		return
	}
	names := sortedEngines(s.h.Drivers)
	engines := make([]Engine, len(names))
	var probes sync.WaitGroup
	for i, name := range names {
		probes.Add(1)
		go func() {
			defer probes.Done()
			d := s.h.Drivers[name]
			cfg := settings[name]
			bin := cfg.Path
			if bin == "" {
				bin = d.Binary()
			}
			e := Engine{Name: name, Available: true, Models: driver.Catalog(name, cfg)}
			if bin != "" {
				version, err := driver.Probe(r.Context(), bin)
				e.Available = err == nil
				e.Version = version
			}
			engines[i] = e
		}()
	}
	probes.Wait()
	result(w, engines, nil)
}

// events sends a complete initial snapshot followed by ordered updates.
// Slow consumers reconnect and recover their transcript and pending approvals.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	ch, unsubscribe := s.h.Subscribe()
	defer unsubscribe()
	snapshot, err := s.h.FullSnapshot()
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	emit := func(kind string, v any) bool {
		data, err := json.Marshal(v)
		if err != nil {
			return false
		}
		_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !emit("snapshot", snapshot) {
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case u, ok := <-ch:
			if !ok {
				return
			}
			if u.Sequence > 0 && len(snapshot) > 0 && u.Sequence <= snapshot[0].Sequence {
				continue
			}
			if !emit("update", u) {
				return
			}
		case <-ticker.C:
			_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			if rc.Flush() != nil {
				return
			}
		}
	}
}
