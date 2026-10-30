package main

import (
	"bqbot/internal/store"
	"encoding/json"
	"errors"
)

// Phase4Action is a closed vocabulary for tools and automation settings.
// Config contains only the value for the selected operation, never a URL to proxy.
type Phase4Action struct {
	Kind   string          `json:"kind"`
	ID     string          `json:"id"`
	Key    string          `json:"key"`
	Config json.RawMessage `json:"config"`
}

func (d *Desktop) Phase4(r Phase4Action) (json.RawMessage, error) {
	d.mu.Lock()
	api := d.app.api
	closed := d.closed
	d.mu.Unlock()
	if closed {
		return nil, errors.New("desktop is closed")
	}
	var value any
	var err error
	switch r.Kind {
	case "routines":
		value, err = api.Routines()
	case "save-routine":
		var v store.Routine
		if err = json.Unmarshal(r.Config, &v); err == nil {
			value, err = api.SaveRoutine(v)
		}
	case "delete-routine":
		err = api.DeleteRoutine(r.ID)
	case "run-routine":
		value, err = api.RunRoutine(r.ID)
	case "routine-history":
		value, err = api.RoutineHistory(r.ID)
	case "webhook-state":
		value, err = api.WebhookState()
	case "enable-webhook":
		err = api.EnableWebhook()
	case "disable-webhook":
		err = api.DisableWebhook()
	case "rotate-webhook-key":
		value, err = api.RotateWebhookKey()
	case "mcp-servers":
		value, err = api.MCPServers()
	case "save-mcp-server":
		var v store.MCPConfig
		if err = json.Unmarshal(r.Config, &v); err == nil {
			value, err = api.SaveMCPServer(v)
		}
	case "delete-mcp-server":
		err = api.DeleteMCPServer(r.ID)
	case "composio":
		value, err = api.Composio()
	case "save-composio":
		var v store.ComposioSettings
		if err = json.Unmarshal(r.Config, &v); err == nil {
			err = api.SaveComposio(v)
		}
	case "save-composio-key":
		err = api.SaveComposioKey(r.Key)
	case "delete-composio-key":
		err = api.DeleteComposioKey()
	case "connect-composio":
		value, err = api.ConnectComposio()
	case "composio-accounts":
		value, err = api.ComposioAccounts()
	default:
		return nil, errors.New("unknown tools or automation action")
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
