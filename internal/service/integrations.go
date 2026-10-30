package service

import (
	"bqbot/internal/store"
	"net/http"
	"net/url"
)

func (s *Server) integrationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/mcp-servers", func(w http.ResponseWriter, r *http.Request) { v, e := s.h.Store.MCPServers(); result(w, v, e) })
	mux.HandleFunc("PUT /v1/mcp-servers", func(w http.ResponseWriter, r *http.Request) {
		var v store.MCPConfig
		if !decode(w, r, &v) {
			return
		}
		if v.ID == "" {
			v.ID = store.NewID()
		}
		if e := v.Validate(); e != nil {
			fail(w, invalid(e.Error()))
			return
		}
		for _, id := range v.BotIDs {
			if _, e := s.bot(id); e != nil {
				fail(w, invalid("MCP bot does not exist"))
				return
			}
		}
		result(w, v, s.h.Store.SaveMCPServer(v))
	})
	mux.HandleFunc("DELETE /v1/mcp-servers/{id}", func(w http.ResponseWriter, r *http.Request) {
		result(w, struct{}{}, s.h.Store.DeleteMCPServer(r.PathValue("id")))
	})
}
func (c *Client) MCPServers() (v []store.MCPConfig, err error) {
	err = c.do("GET", "/v1/mcp-servers", nil, &v)
	return
}
func (c *Client) SaveMCPServer(v store.MCPConfig) (out store.MCPConfig, err error) {
	err = c.do("PUT", "/v1/mcp-servers", v, &out)
	return
}
func (c *Client) DeleteMCPServer(id string) error {
	return c.do("DELETE", "/v1/mcp-servers/"+url.PathEscape(id), nil, nil)
}
