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
		out, e := computer.Execute(r.Context(), cfg, v.Action)
		result(w, out, e)
	}))
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
