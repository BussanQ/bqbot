package service

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"time"

	"bqbot/internal/computer"
	"bqbot/internal/store"
)

type streamLease struct {
	remoteID string
	config   store.ComputerConfig
	expires  time.Time
}
type workspaceViews struct {
	sync.Mutex
	active map[string]streamLease
	closed bool
}

func (s *Server) workspaceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/computers/{bot}/start", s.withBot(func(w http.ResponseWriter, r *http.Request, b store.Bot) {
		s.computerMu.Lock()
		defer s.computerMu.Unlock()
		cfg, err := s.h.Store.Computer(b.ID)
		if err != nil || !cfg.Enabled || !cfg.Managed {
			fail(w, invalid("本机 Linux 工作区未启用"))
			return
		}
		err = computer.StartLocalWorkspace(r.Context(), s.h.Store, b.ID)
		result(w, struct{}{}, err)
	}))
	mux.HandleFunc("POST /v1/computers/{bot}/streams", s.withBot(func(w http.ResponseWriter, r *http.Request, b store.Bot) {
		var body struct {
			SDP       string `json:"sdp"`
			Confirmed bool   `json:"confirmed"`
		}
		if !decode(w, r, &body) {
			return
		}
		cfg, err := s.h.Store.Computer(b.ID)
		if err != nil {
			fail(w, invalid(err.Error()))
			return
		}
		answer, err := computer.OpenStream(r.Context(), cfg, body.SDP, s.h.Store, body.Confirmed)
		if err != nil {
			fail(w, invalid(err.Error()))
			return
		}
		s.views.Lock()
		current, e := s.h.Store.Computer(b.ID)
		_, botErr := s.h.Bot(b.ID)
		if s.views.closed || botErr != nil || e != nil || cfg != current {
			s.views.Unlock()
			s.stopStream(cfg, answer.ID)
			fail(w, invalid("workspace configuration changed"))
			return
		}
		if s.views.active == nil {
			s.views.active = map[string]streamLease{}
		}
		now := time.Now()
		for id, v := range s.views.active {
			if now.After(v.expires) {
				delete(s.views.active, id)
			}
		}
		localID := store.NewID()
		s.views.active[localID] = streamLease{remoteID: answer.ID, config: cfg, expires: now.Add(25 * time.Second)}
		answer.ID = localID
		s.views.Unlock()
		result(w, answer, nil)
	}))
	update := s.withBot(func(w http.ResponseWriter, r *http.Request, b store.Bot) {
		id := r.PathValue("id")
		closeView := r.Method == "DELETE"
		s.views.Lock()
		v, ok := s.views.active[id]
		if !ok || v.config.BotID != b.ID {
			s.views.Unlock()
			fail(w, problem{404, "unknown workspace stream"})
			return
		}
		current, err := s.h.Store.Computer(b.ID)
		if closeView || current != v.config || err != nil || time.Now().After(v.expires) {
			delete(s.views.active, id)
			s.views.Unlock()
			s.stopStream(v.config, v.remoteID)
			if !closeView {
				fail(w, invalid("workspace stream expired"))
				return
			}
			result(w, struct{}{}, nil)
			return
		}
		v.expires = time.Now().Add(25 * time.Second)
		s.views.active[id] = v
		s.views.Unlock()
		err = computer.UpdateStream(r.Context(), v.config, v.remoteID, false, s.h.Store)
		result(w, struct{}{}, err)
	})
	mux.HandleFunc("PUT /v1/computers/{bot}/streams/{id}", update)
	mux.HandleFunc("DELETE /v1/computers/{bot}/streams/{id}", update)
}

func (s *Server) stopStream(cfg store.ComputerConfig, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = computer.UpdateStream(ctx, cfg, id, true, s.h.Store)
}

// Called under the registry lock so configuration changes and new leases
// have a single ordering. Remote shutdown happens after releasing that lock.
func (v *workspaceViews) detachLocked(bot string, keep *store.ComputerConfig) []streamLease {
	var stale []streamLease
	for id, lease := range v.active {
		if bot != "" && lease.config.BotID != bot || keep != nil && lease.config == *keep {
			continue
		}
		delete(v.active, id)
		stale = append(stale, lease)
	}
	return stale
}

func (s *Server) stopViews(stale []streamLease) {
	for _, lease := range stale {
		s.stopStream(lease.config, lease.remoteID)
	}
}

func (s *Server) saveComputer(ctx context.Context, cfg store.ComputerConfig) error {
	s.computerMu.Lock()
	defer s.computerMu.Unlock()
	if _, err := s.h.Bot(cfg.BotID); err != nil {
		return err
	}
	old, _ := s.h.Store.Computer(cfg.BotID)
	if cfg.Managed && cfg.Enabled {
		if err := computer.StartLocalWorkspace(ctx, s.h.Store, cfg.BotID); err != nil {
			return err
		}
	} else if old.Managed && old.Enabled {
		if err := computer.StopLocalWorkspace(ctx, s.h.Store, cfg.BotID); err != nil {
			return err
		}
	}

	s.views.Lock()
	err := s.h.Store.SaveComputer(cfg)
	var stale []streamLease
	if err == nil {
		stale = s.views.detachLocked(cfg.BotID, &cfg)
	}
	s.views.Unlock()
	s.stopViews(stale)
	return err
}

func (s *Server) revokeWorkspaceViews(bot string) {
	s.views.Lock()
	if bot == "" {
		s.views.closed = true
	}
	stale := s.views.detachLocked(bot, nil)
	s.views.Unlock()
	s.stopViews(stale)
}

func (c *Client) OpenWorkspaceStream(bot, sdp string, confirmed bool) (v computer.StreamAnswer, err error) {
	err = c.do("POST", "/v1/computers/"+url.PathEscape(bot)+"/streams", map[string]any{"sdp": sdp, "confirmed": confirmed}, &v)
	return
}
func (c *Client) UpdateWorkspaceStream(bot, id string, closeView bool) error {
	if id == "" {
		return errors.New("missing stream ID")
	}
	method := "PUT"
	if closeView {
		method = "DELETE"
	}
	return c.do(method, "/v1/computers/"+url.PathEscape(bot)+"/streams/"+url.PathEscape(id), nil, nil)
}
