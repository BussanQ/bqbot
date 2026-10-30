package service

import (
	"bqbot/internal/store"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type WebhookState struct {
	Running    bool   `json:"running"`
	Configured bool   `json:"configured"`
	URL        string `json:"url"`
	Error      string `json:"error,omitempty"`
}

func (s *Server) automationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/routines", func(w http.ResponseWriter, r *http.Request) { v, e := s.h.Store.Routines(); result(w, v, e) })
	mux.HandleFunc("PUT /v1/routines", func(w http.ResponseWriter, r *http.Request) {
		var v store.Routine
		if !decode(w, r, &v) {
			return
		}
		out, e := s.automation.Save(v)
		if e != nil {
			fail(w, invalid(e.Error()))
			return
		}
		result(w, out, nil)
	})
	mux.HandleFunc("DELETE /v1/routines/{id}", func(w http.ResponseWriter, r *http.Request) {
		result(w, struct{}{}, s.automation.Delete(r.PathValue("id")))
	})
	mux.HandleFunc("POST /v1/routines/{id}/run", func(w http.ResponseWriter, r *http.Request) {
		var req struct{}
		if !decode(w, r, &req) {
			return
		}
		v, e := s.automation.Run(r.PathValue("id"), "manual")
		result(w, v, e)
	})
	mux.HandleFunc("GET /v1/routines/{id}/history", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.h.Store.RoutineRuns(r.PathValue("id"))
		result(w, v, e)
	})
	mux.HandleFunc("GET /v1/webhook", func(w http.ResponseWriter, r *http.Request) { v, e := s.webhookState(); result(w, v, e) })
	mux.HandleFunc("POST /v1/webhook/enable", func(w http.ResponseWriter, r *http.Request) {
		var req struct{}
		if !decode(w, r, &req) {
			return
		}
		result(w, struct{}{}, s.enableWebhook("127.0.0.1:8800"))
	})
	mux.HandleFunc("POST /v1/webhook/disable", func(w http.ResponseWriter, r *http.Request) {
		var req struct{}
		if !decode(w, r, &req) {
			return
		}
		result(w, struct{}{}, s.disableWebhook())
	})
	mux.HandleFunc("POST /v1/webhook/rotate", func(w http.ResponseWriter, r *http.Request) {
		var req struct{}
		if !decode(w, r, &req) {
			return
		}
		token, e := s.rotateWebhook()
		result(w, map[string]string{"token": token}, e)
	})
}
func (s *Server) webhookState() (WebhookState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	config, err := s.h.Store.WebhookConfig()
	address := s.webhookURL
	if address == "" {
		address = "http://127.0.0.1:8800"
	}
	failure := s.webhookError
	if failure == "" && s.automation != nil {
		failure = s.automation.Error()
	}
	return WebhookState{Running: s.webhook != nil, Configured: config.Token != "", URL: address, Error: failure}, err
}
func (s *Server) rotateWebhook() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	config, err := s.h.Store.WebhookConfig()
	if err != nil {
		return "", err
	}
	var bytes [32]byte
	if _, err = rand.Read(bytes[:]); err != nil {
		return "", err
	}
	config.Token = hex.EncodeToString(bytes[:])
	if err = s.h.Store.SaveWebhookConfig(config); err != nil {
		return "", err
	}
	return config.Token, nil
}
func (s *Server) enableWebhook(addr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("service closed")
	}
	if s.webhook != nil {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host != "127.0.0.1" {
		return errors.New("webhook must listen on 127.0.0.1")
	}
	config, err := s.h.Store.WebhookConfig()
	if err != nil {
		return err
	}
	if config.Token == "" {
		return errors.New("generate a webhook key before enabling the receiver")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		s.webhookError = "webhook listen failed: " + err.Error()
		return err
	}
	config.Enabled = true
	if err = s.h.Store.SaveWebhookConfig(config); err != nil {
		ln.Close()
		return err
	}
	address := "http://" + ln.Addr().String()
	server := &http.Server{Handler: s.webhookHandler(address), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	s.webhook, s.webhookURL, s.webhookError = server, address, ""
	go server.Serve(ln)
	return nil
}
func (s *Server) disableWebhook() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	config, err := s.h.Store.WebhookConfig()
	if err != nil {
		return err
	}
	config.Enabled = false
	if err = s.h.Store.SaveWebhookConfig(config); err != nil {
		return err
	}
	if s.webhook != nil {
		err = s.webhook.Close()
		s.webhook = nil
	}
	s.webhookError = ""
	return err
}
func (s *Server) webhookHandler(address string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { result(w, map[string]string{"status": "ok"}, nil) })
	mux.HandleFunc("POST /hooks/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !validID(r.PathValue("id")) {
			fail(w, invalid("invalid hook ID"))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		var req struct {
			Text string `json:"text"`
		}
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if err := d.Decode(&req); err != nil {
			fail(w, invalid("webhook body must be JSON with optional text"))
			return
		}
		var extra any
		if err := d.Decode(&extra); err != io.EOF {
			fail(w, invalid("expected one JSON value"))
			return
		}
		v, e := s.automation.RunWithText(r.PathValue("id"), "webhook", req.Text)
		if e != nil {
			fail(w, invalid(e.Error()))
			return
		}
		writeJSON(w, http.StatusAccepted, v)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		config, err := s.h.Store.WebhookConfig()
		if err != nil {
			fail(w, err)
			return
		}
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		key := sha256.Sum256([]byte(token))
		want := sha256.Sum256([]byte(config.Token))
		if !config.Enabled || config.Token == "" || !ok || subtle.ConstantTimeCompare(key[:], want[:]) != 1 {
			fail(w, problem{http.StatusUnauthorized, "unauthorized"})
			return
		}
		if "http://"+r.Host != address || r.Header.Get("Origin") != "" {
			fail(w, problem{http.StatusForbidden, "origin or host is not allowed"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func (c *Client) Routines() (v []store.Routine, err error) {
	err = c.do("GET", "/v1/routines", nil, &v)
	return
}
func (c *Client) SaveRoutine(v store.Routine) (out store.Routine, err error) {
	err = c.do("PUT", "/v1/routines", v, &out)
	return
}
func (c *Client) DeleteRoutine(id string) error {
	return c.do("DELETE", "/v1/routines/"+url.PathEscape(id), nil, nil)
}
func (c *Client) RunRoutine(id string) (v store.RoutineRun, err error) {
	err = c.do("POST", "/v1/routines/"+url.PathEscape(id)+"/run", struct{}{}, &v)
	return
}
func (c *Client) RoutineHistory(id string) (v []store.RoutineRun, err error) {
	err = c.do("GET", "/v1/routines/"+url.PathEscape(id)+"/history", nil, &v)
	return
}
func (c *Client) WebhookState() (v WebhookState, err error) {
	err = c.do("GET", "/v1/webhook", nil, &v)
	return
}
func (c *Client) EnableWebhook() error  { return c.do("POST", "/v1/webhook/enable", struct{}{}, nil) }
func (c *Client) DisableWebhook() error { return c.do("POST", "/v1/webhook/disable", struct{}{}, nil) }
func (c *Client) RotateWebhookKey() (v map[string]string, err error) {
	err = c.do("POST", "/v1/webhook/rotate", struct{}{}, &v)
	return
}
