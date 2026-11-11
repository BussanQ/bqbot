package service

import (
	"net/http"
	"net/url"

	"bqbot/internal/driver"
	"bqbot/internal/store"
)

type cachedModelCatalog struct {
	settings driver.EngineSettings
	list     driver.ModelInventory
}

type modelCatalog struct {
	driver.ModelInventory
	Notice string `json:"notice,omitempty"`
}

func (s *Server) modelRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/engines/{engine}/models", func(w http.ResponseWriter, r *http.Request) {
		d := s.h.Drivers[r.PathValue("engine")]
		if d == nil {
			fail(w, invalid("unknown engine"))
			return
		}
		settings, err := s.h.Store.Settings()
		if err != nil {
			fail(w, err)
			return
		}
		list, err := driver.DiscoverModels(r.Context(), d, settings[d.Name()])
		catalog := modelCatalog{ModelInventory: list}
		if err == nil && list.Source == "agent" {
			s.modelCapabilities.Store(d.Name(), cachedModelCatalog{settings[d.Name()], list})
		}
		if err != nil {
			catalog.Notice = "未能读取 Agent 目录，当前显示配置或内置模型。"
		}
		result(w, catalog, nil)
	})
	mux.HandleFunc("PUT /v1/bots/{bot}/model-options", func(w http.ResponseWriter, r *http.Request) {
		var options store.Bot
		if !decode(w, r, &options) {
			return
		}
		err := s.h.UpdateModelOptions(r.PathValue("bot"), options, func(b store.Bot) error {
			settings, err := s.h.Store.Settings()
			if err != nil {
				return err
			}
			return s.validateBot(b, settings)
		})
		result(w, struct{}{}, err)
	})
}
func (c *Client) ModelCatalog(engine string) (v modelCatalog, err error) {
	err = c.do("GET", "/v1/engines/"+url.PathEscape(engine)+"/models", nil, &v)
	return
}
func (c *Client) SetModelOptions(id string, options store.Bot) error {
	return c.do("PUT", botPath(id)+"/model-options", options, nil)
}
