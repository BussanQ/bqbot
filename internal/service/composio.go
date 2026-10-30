package service

import (
	"bqbot/internal/composio"
	"bqbot/internal/store"
	"net/http"
)

type ComposioState struct {
	Settings   store.ComposioSettings `json:"settings"`
	Configured bool                   `json:"configured"`
}

func (s *Server) composioRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/composio", func(w http.ResponseWriter, r *http.Request) {
		settings, err := s.h.Store.ComposioSettings()
		if err != nil {
			fail(w, err)
			return
		}
		configured, err := s.composio.Configured()
		result(w, ComposioState{settings, configured}, err)
	})
	mux.HandleFunc("PUT /v1/composio", func(w http.ResponseWriter, r *http.Request) {
		var v store.ComposioSettings
		if !decode(w, r, &v) {
			return
		}
		result(w, struct{}{}, s.h.Store.SaveComposioSettings(v))
	})
	mux.HandleFunc("PUT /v1/composio/key", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Key string `json:"key"`
		}
		if !decode(w, r, &v) {
			return
		}
		result(w, struct{}{}, s.composio.SaveKey(v.Key))
	})
	mux.HandleFunc("DELETE /v1/composio/key", func(w http.ResponseWriter, r *http.Request) { result(w, struct{}{}, s.composio.DeleteKey()) })
	mux.HandleFunc("POST /v1/composio/connect", func(w http.ResponseWriter, r *http.Request) {
		var v struct{}
		if !decode(w, r, &v) {
			return
		}
		settings, err := s.h.Store.ComposioSettings()
		if err != nil {
			fail(w, err)
			return
		}
		link, err := s.composio.Connect(r.Context(), settings.UserID, settings.AuthConfigID)
		result(w, link, err)
	})
	mux.HandleFunc("GET /v1/composio/accounts", func(w http.ResponseWriter, r *http.Request) {
		settings, err := s.h.Store.ComposioSettings()
		if err != nil {
			fail(w, err)
			return
		}
		v, err := s.composio.Accounts(r.Context(), settings.UserID)
		result(w, v, err)
	})
}
func (c *Client) Composio() (v ComposioState, err error) {
	err = c.do("GET", "/v1/composio", nil, &v)
	return
}
func (c *Client) SaveComposio(v store.ComposioSettings) error {
	return c.do("PUT", "/v1/composio", v, nil)
}
func (c *Client) SaveComposioKey(key string) error {
	return c.do("PUT", "/v1/composio/key", map[string]string{"key": key}, nil)
}
func (c *Client) DeleteComposioKey() error { return c.do("DELETE", "/v1/composio/key", nil, nil) }
func (c *Client) ConnectComposio() (v composio.Link, err error) {
	err = c.do("POST", "/v1/composio/connect", struct{}{}, &v)
	return
}
func (c *Client) ComposioAccounts() (v []composio.Account, err error) {
	err = c.do("GET", "/v1/composio/accounts", nil, &v)
	return
}
