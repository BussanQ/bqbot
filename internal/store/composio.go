package store

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// ComposioSettings contains connection metadata only. The API key lives in mygo secrets.
type ComposioSettings struct {
	Enabled      bool     `json:"enabled"`
	UserID       string   `json:"user_id"`
	AuthConfigID string   `json:"auth_config_id"`
	MCPURL       string   `json:"mcp_url"`
	BotIDs       []string `json:"bot_ids,omitempty"`
}

func (s *Store) ComposioSettings() (ComposioSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var v ComposioSettings
	b, err := os.ReadFile(filepath.Join(s.dir, "composio.json"))
	if errors.Is(err, os.ErrNotExist) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	err = json.Unmarshal(b, &v)
	return v, err
}
func (s *Store) SaveComposioSettings(v ComposioSettings) error {
	if v.Enabled && (strings.TrimSpace(v.UserID) == "" || v.MCPURL == "") {
		return errors.New("Composio user ID and MCP URL are required")
	}
	if v.MCPURL != "" {
		u, err := url.Parse(v.MCPURL)
		if err != nil || u.Scheme != "https" || u.Hostname() != "mcp.composio.dev" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("Composio MCP URL must be an HTTPS URL on mcp.composio.dev without credentials or query parameters")
		}
	}
	for _, id := range v.BotIDs {
		if !validID(id) {
			return errors.New("invalid Composio bot ID")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSON(filepath.Join(s.dir, "composio.json"), v)
}
