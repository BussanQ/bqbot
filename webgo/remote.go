package webgo

import (
	"encoding/json"
	"errors"
)

type RemoteAction struct {
	Kind   string   `json:"kind"`
	Origin string   `json:"origin"`
	BotIDs []string `json:"bot_ids"`
}

func (d *Desktop) Remote(r RemoteAction) (json.RawMessage, error) {
	d.mu.Lock()
	api := d.app.api
	closed := d.closed
	d.mu.Unlock()
	if closed {
		return nil, errors.New("desktop is closed")
	}
	var v any
	var err error
	switch r.Kind {
	case "state":
		v, err = api.RemoteState()
	case "enable":
		v, err = api.EnableRemote(r.Origin)
	case "disable":
		err = api.DisableRemote()
	case "pair":
		var code string
		code, err = api.PairRemote(r.BotIDs)
		v = map[string]string{"code": code}
	case "revoke":
		err = api.RevokeRemote()
	default:
		return nil, errors.New("unknown remote action")
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}
