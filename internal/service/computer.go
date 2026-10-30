package service

import (
	"net/http"
	"net/url"

	"bqbot/internal/computer"
	"bqbot/internal/store"
)

// ComputerRequest is a user-initiated action. Confirmed is required for input.
// Agents must use the turn-scoped approval gateway, never this management API.
type ComputerRequest struct {
	Action    computer.Action `json:"action"`
	Confirmed bool            `json:"confirmed"`
}

func (s *Server) computerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/computer-host", func(w http.ResponseWriter, r *http.Request) {
		result(w, s.h.Host.Status(), nil)
	})
	mux.HandleFunc("POST /v1/computer-host/start", func(w http.ResponseWriter, r *http.Request) {
		err := s.h.Host.Start(r.Context())
		result(w, s.h.Host.Status(), err)
	})
	mux.HandleFunc("POST /v1/computer-host/stop", func(w http.ResponseWriter, r *http.Request) {
		s.h.Host.Stop()
		result(w, s.h.Host.Status(), nil)
	})
	mux.HandleFunc("GET /v1/computers", func(w http.ResponseWriter, r *http.Request) { v, e := s.h.Store.Computers(); result(w, v, e) })
	mux.HandleFunc("PUT /v1/computers", func(w http.ResponseWriter, r *http.Request) {
		var v store.ComputerConfig
		if !decode(w, r, &v) {
			return
		}
		if _, e := s.bot(v.BotID); e != nil {
			fail(w, invalid("computer bot does not exist"))
			return
		}
		if e := v.Validate(); e != nil {
			fail(w, invalid(e.Error()))
			return
		}
		result(w, v, s.h.Store.SaveComputer(v))
	})
	mux.HandleFunc("POST /v1/computers/{bot}/action", s.withBot(func(w http.ResponseWriter, r *http.Request, b store.Bot) {
		var v ComputerRequest
		if !decode(w, r, &v) {
			return
		}
		if e := v.Action.Validate(); e != nil {
			fail(w, invalid(e.Error()))
			return
		}
		if v.Action.Kind != "screenshot" && !v.Confirmed {
			fail(w, problem{http.StatusForbidden, "computer input requires explicit confirmation"})
			return
		}
		cfg, e := s.h.Store.Computer(b.ID)
		if e != nil {
			fail(w, invalid(e.Error()))
			return
		}
		out, e := computer.ExecuteWithHost(r.Context(), cfg, v.Action, s.h.Host)
		result(w, out, e)
	}))
}
func (c *Client) HostStatus() (v computer.HostStatus, e error) {
	e = c.do("GET", "/v1/computer-host", nil, &v)
	return
}
func (c *Client) StartHost() (v computer.HostStatus, e error) {
	e = c.do("POST", "/v1/computer-host/start", struct{}{}, &v)
	return
}
func (c *Client) StopHost() (v computer.HostStatus, e error) {
	e = c.do("POST", "/v1/computer-host/stop", struct{}{}, &v)
	return
}

func (c *Client) Computers() (v []store.ComputerConfig, e error) {
	e = c.do("GET", "/v1/computers", nil, &v)
	return
}
func (c *Client) SaveComputer(v store.ComputerConfig) (out store.ComputerConfig, e error) {
	e = c.do("PUT", "/v1/computers", v, &out)
	return
}
func (c *Client) ComputerAction(botID string, v ComputerRequest) (out computer.Result, e error) {
	e = c.do("POST", "/v1/computers/"+url.PathEscape(botID)+"/action", v, &out)
	return
}
