package service

import (
	"errors"
	"net/http"

	"bqbot/internal/remote"
)

func (s *Server) remoteRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/remote", func(w http.ResponseWriter, r *http.Request) {
		s.remoteMu.Lock()
		defer s.remoteMu.Unlock()
		state := remote.State{}
		if s.remote != nil {
			state = s.remote.State()
		}
		result(w, state, nil)
	})
	mux.HandleFunc("POST /v1/remote/enable", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Origin string `json:"origin"`
		}
		if !decode(w, r, &req) {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closed {
			fail(w, invalid("service is closed"))
			return
		}
		s.remoteMu.Lock()
		defer s.remoteMu.Unlock()
		if s.remote != nil {
			fail(w, invalid("disable the current gateway first"))
			return
		}
		g, err := remote.Listen(NewClient(s.URL, s.Token), "127.0.0.1:8801", req.Origin)
		if err != nil {
			fail(w, invalid(err.Error()))
			return
		}
		s.remote = g
		result(w, g.State(), nil)
	})
	mux.HandleFunc("POST /v1/remote/disable", func(w http.ResponseWriter, r *http.Request) {
		var req struct{}
		if !decode(w, r, &req) {
			return
		}
		s.remoteMu.Lock()
		defer s.remoteMu.Unlock()
		var err error
		if s.remote != nil {
			err = s.remote.Close()
			s.remote = nil
		}
		result(w, struct{}{}, err)
	})
	mux.HandleFunc("POST /v1/remote/pair", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			BotIDs []string `json:"bot_ids"`
		}
		if !decode(w, r, &req) {
			return
		}
		s.remoteMu.Lock()
		defer s.remoteMu.Unlock()
		if s.remote == nil {
			fail(w, invalid("remote access is disabled"))
			return
		}
		code, err := s.remote.PairCode(req.BotIDs)
		result(w, map[string]string{"code": code}, err)
	})
	mux.HandleFunc("POST /v1/remote/revoke", func(w http.ResponseWriter, r *http.Request) {
		var req struct{}
		if !decode(w, r, &req) {
			return
		}
		s.remoteMu.Lock()
		defer s.remoteMu.Unlock()
		if s.remote != nil {
			s.remote.Revoke()
		}
		result(w, struct{}{}, nil)
	})
}
func (c *Client) RemoteState() (v remote.State, err error) {
	err = c.do("GET", "/v1/remote", nil, &v)
	return
}
func (c *Client) EnableRemote(origin string) (v remote.State, err error) {
	err = c.do("POST", "/v1/remote/enable", map[string]string{"origin": origin}, &v)
	return
}
func (c *Client) DisableRemote() error { return c.do("POST", "/v1/remote/disable", struct{}{}, nil) }
func (c *Client) RevokeRemote() error  { return c.do("POST", "/v1/remote/revoke", struct{}{}, nil) }
func (c *Client) PairRemote(ids []string) (string, error) {
	var v map[string]string
	err := c.do("POST", "/v1/remote/pair", map[string]any{"bot_ids": ids}, &v)
	if err == nil && v["code"] == "" {
		err = errors.New("missing pairing code")
	}
	return v["code"], err
}
