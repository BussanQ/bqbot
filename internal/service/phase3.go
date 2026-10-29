package service

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"bqbot/internal/store"
)

// phase3Routes shares the same bearer, origin, body limits and decoder as v1.
func (s *Server) phase3Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/bots/{bot}/threads", s.withBot(func(w http.ResponseWriter, r *http.Request, b store.Bot) {
		v, e := s.h.Store.Threads(b.ID)
		result(w, v, e)
	}))
	mux.HandleFunc("POST /v1/bots/{bot}/threads", s.withBot(func(w http.ResponseWriter, r *http.Request, b store.Bot) {
		var req struct {
			Title string `json:"title"`
		}
		if !decode(w, r, &req) {
			return
		}
		if strings.TrimSpace(req.Title) == "" || !safeText(req.Title, 256) {
			fail(w, invalid("invalid thread title"))
			return
		}
		v, e := s.h.CreateThread(b.ID, req.Title)
		result(w, v, e)
	}))
	mux.HandleFunc("POST /v1/bots/{bot}/clone", s.withBot(func(w http.ResponseWriter, r *http.Request, b store.Bot) {
		var req struct{}
		if !decode(w, r, &req) {
			return
		}
		v, e := s.h.CloneBot(b.ID)
		result(w, v, e)
	}))
	mux.HandleFunc("GET /v1/threads/{thread}/transcript", s.withThread(func(w http.ResponseWriter, r *http.Request, t store.Thread) {
		v, e := s.h.Store.ThreadTranscript(t.ID)
		result(w, v, e)
	}))
	mux.HandleFunc("POST /v1/threads/{thread}/close", s.withThread(func(w http.ResponseWriter, r *http.Request, t store.Thread) {
		var req struct{}
		if !decode(w, r, &req) {
			return
		}
		result(w, struct{}{}, s.h.CloseThread(t.ID))
	}))
	mux.HandleFunc("POST /v1/threads/{thread}/stop", s.withThread(func(w http.ResponseWriter, r *http.Request, t store.Thread) {
		var req struct{}
		if !decode(w, r, &req) {
			return
		}
		s.h.StopThread(t.ID)
		result(w, struct{}{}, nil)
	}))
	mux.HandleFunc("POST /v1/threads/{thread}/compact", s.withThread(func(w http.ResponseWriter, r *http.Request, t store.Thread) {
		var req struct{}
		if !decode(w, r, &req) {
			return
		}
		v, e := s.h.CompactThread(t.ID)
		result(w, v, e)
	}))
	mux.HandleFunc("POST /v1/threads/{thread}/send", s.withThread(func(w http.ResponseWriter, r *http.Request, t store.Thread) {
		var req sendRequest
		if !decode(w, r, &req) {
			return
		}
		if e := validateSend(req); e != nil {
			fail(w, e)
			return
		}
		if t.Closed || t.RoomID != "" {
			fail(w, invalid("thread is closed or belongs to a channel"))
			return
		}
		v, e := s.h.SendThread(t.ID, req.Text, req.Attachments, req.Mode)
		result(w, v, e)
	}))
	mux.HandleFunc("GET /v1/rooms", func(w http.ResponseWriter, r *http.Request) { v, e := s.h.Store.Rooms(); result(w, v, e) })
	mux.HandleFunc("PUT /v1/rooms", func(w http.ResponseWriter, r *http.Request) {
		var room store.Room
		if !decode(w, r, &room) {
			return
		}
		if e := s.validateRoom(room); e != nil {
			fail(w, e)
			return
		}
		result(w, struct{}{}, s.h.SaveRoom(room))
	})
	mux.HandleFunc("DELETE /v1/rooms/{room}", s.withRoom(func(w http.ResponseWriter, r *http.Request, v store.Room) {
		result(w, struct{}{}, s.h.DeleteRoom(v.ID))
	}))
	mux.HandleFunc("GET /v1/rooms/{room}/transcript", s.withRoom(func(w http.ResponseWriter, r *http.Request, v store.Room) {
		m, e := s.h.Store.RoomTranscript(v.ID)
		result(w, m, e)
	}))
	mux.HandleFunc("POST /v1/rooms/{room}/stop", s.withRoom(func(w http.ResponseWriter, r *http.Request, v store.Room) {
		var req struct{}
		if !decode(w, r, &req) {
			return
		}
		s.h.StopThread(v.ID)
		result(w, struct{}{}, nil)
	}))
	mux.HandleFunc("POST /v1/rooms/{room}/send", s.withRoom(func(w http.ResponseWriter, r *http.Request, v store.Room) {
		var req sendRequest
		if !decode(w, r, &req) {
			return
		}
		if e := validateSend(req); e != nil {
			fail(w, e)
			return
		}
		if req.Mode != "" && req.Mode != "queue" {
			fail(w, invalid("channels use queue mode"))
			return
		}
		m, e := s.h.SendRoom(v.ID, req.Text, req.Attachments)
		result(w, m, e)
	}))
	mux.HandleFunc("GET /v1/memories/{scope}", s.withScope(func(w http.ResponseWriter, r *http.Request, id string) {
		v, e := s.h.Store.Memories(id)
		result(w, v, e)
	}))
	mux.HandleFunc("PUT /v1/memories/{scope}", s.withScope(func(w http.ResponseWriter, r *http.Request, id string) {
		var v store.Memory
		if !decode(w, r, &v) {
			return
		}
		if !validID(v.ID) || strings.TrimSpace(v.Text) == "" || len(v.Text) > 8<<10 {
			fail(w, invalid("invalid memory"))
			return
		}
		e := s.h.Store.SaveMemory(id, v)
		if e == nil {
			s.notifyScope(id)
		}
		result(w, struct{}{}, e)
	}))
	mux.HandleFunc("DELETE /v1/memories/{scope}/{memory}", s.withScope(func(w http.ResponseWriter, r *http.Request, id string) {
		mid := r.PathValue("memory")
		if !validID(mid) {
			fail(w, invalid("invalid memory ID"))
			return
		}
		e := s.h.Store.DeleteMemory(id, mid)
		if e == nil {
			s.notifyScope(id)
		}
		result(w, struct{}{}, e)
	}))
	mux.HandleFunc("GET /v1/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		if len(q) > 512 {
			fail(w, invalid("search query too large"))
			return
		}
		v, e := s.h.Store.Search(q)
		result(w, v, e)
	})
	mux.HandleFunc("GET /v1/decision-settings", func(w http.ResponseWriter, r *http.Request) { v, e := s.h.Store.DecisionSettings(); result(w, v, e) })
	mux.HandleFunc("PUT /v1/decision-settings", func(w http.ResponseWriter, r *http.Request) {
		var cfg store.DecisionSettings
		if !decode(w, r, &cfg) {
			return
		}
		if e := validateDecision(cfg); e != nil {
			fail(w, e)
			return
		}
		e := s.h.Store.SaveDecisionSettings(cfg)
		if e == nil {
			s.h.NotifyMetadata("", "", "")
		}
		result(w, struct{}{}, e)
	})
}
func (s *Server) withThread(fn func(http.ResponseWriter, *http.Request, store.Thread)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("thread")
		if !validID(id) {
			fail(w, invalid("invalid thread ID"))
			return
		}
		v, e := s.h.Store.Thread(id)
		if e != nil {
			fail(w, problem{404, "thread not found"})
			return
		}
		fn(w, r, v)
	}
}
func (s *Server) withRoom(fn func(http.ResponseWriter, *http.Request, store.Room)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("room")
		if !validID(id) {
			fail(w, invalid("invalid room ID"))
			return
		}
		v, e := s.h.Store.Room(id)
		if e != nil {
			fail(w, problem{404, "room not found"})
			return
		}
		fn(w, r, v)
	}
}
func (s *Server) withScope(fn func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("scope")
		if !validID(id) {
			fail(w, invalid("invalid memory scope"))
			return
		}
		if _, e := s.h.Store.Thread(id); e != nil {
			if _, e = s.h.Store.Room(id); e != nil {
				fail(w, problem{404, "memory scope not found"})
				return
			}
		}
		fn(w, r, id)
	}
}
func (s *Server) validateRoom(v store.Room) error {
	if !validID(v.ID) || strings.TrimSpace(v.Name) == "" || !safeText(v.Name, 120) || len(v.Instructions) > 128<<10 {
		return invalid("invalid channel profile")
	}
	if v.ReplyMode != "lead" && v.ReplyMode != "auto" {
		return invalid("reply mode must be lead or auto")
	}
	if len(v.Members) == 0 || len(v.Members) > 32 {
		return invalid("channel needs 1 to 32 members")
	}
	seen := map[string]bool{}
	for _, id := range v.Members {
		if seen[id] {
			return invalid("duplicate channel member")
		}
		if _, e := s.bot(id); e != nil {
			return invalid("invalid channel member")
		}
		seen[id] = true
	}
	if !seen[v.LeadID] {
		return invalid("lead must be a channel member")
	}
	if v.WorkDir != "" {
		if !safeText(v.WorkDir, 4096) || !filepath.IsAbs(v.WorkDir) {
			return invalid("workdir must be absolute")
		}
		info, e := os.Stat(v.WorkDir)
		if e != nil || !info.IsDir() {
			return invalid("workdir must be an existing directory")
		}
	}
	return nil
}
func validateDecision(cfg store.DecisionSettings) error {
	if cfg.APIKeyEnv != "" && (len(cfg.APIKeyEnv) > 128 || !envPattern.MatchString(cfg.APIKeyEnv)) {
		return invalid("APIKeyEnv must name an environment variable")
	}
	if !safeText(cfg.Model, 200) || strings.TrimSpace(cfg.Model) != cfg.Model {
		return invalid("invalid decision model")
	}
	if cfg.BaseURL != "" {
		u, e := url.Parse(cfg.BaseURL)
		if e != nil || len(cfg.BaseURL) > 4096 || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return invalid("invalid decision base URL; credentials and queries are not allowed")
		}
	}
	return nil
}

// notifyScope keeps memory metadata on its real thread or room route.
func (s *Server) notifyScope(id string) {
	if thread, err := s.h.Store.Thread(id); err == nil {
		s.h.NotifyMetadata(thread.BotID, id, thread.RoomID)
		return
	}
	s.h.NotifyMetadata("", "", id)
}
