package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// MCPConfig contains launch metadata. Env and Headers map names to environment
// variable names, so credentials are resolved in Go and never returned to the page.
type MCPConfig struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Enabled   bool              `json:"enabled"`
	Transport string            `json:"transport"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	BotIDs    []string          `json:"bot_ids,omitempty"`
}

func (v MCPConfig) Validate() error {
	if !validID(v.ID) || strings.TrimSpace(v.Name) == "" || len(v.Name) > 120 {
		return errors.New("MCP ID and name are required")
	}
	for _, id := range v.BotIDs {
		if !validID(id) {
			return errors.New("invalid MCP bot ID")
		}
	}
	switch v.Transport {
	case "stdio":
		if strings.TrimSpace(v.Command) == "" || strings.ContainsAny(v.Command, "\x00\r\n") {
			return errors.New("MCP command is required")
		}
		if v.URL != "" || len(v.Headers) > 0 {
			return errors.New("stdio MCP cannot have a URL or headers")
		}
	case "http", "sse":
		u, err := url.Parse(v.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("MCP URL must be HTTP(S) without credentials, query or fragment")
		}
		if v.Command != "" || len(v.Args) > 0 || len(v.Env) > 0 {
			return errors.New("remote MCP cannot have a command or environment")
		}
	default:
		return errors.New("MCP transport must be stdio, http or sse")
	}
	for k, e := range v.Env {
		if !envIdentifier(k) || !envIdentifier(e) {
			return errors.New("MCP environment values must name environment variables")
		}
	}
	for k, e := range v.Headers {
		if k == "" || strings.ContainsAny(k, " \t\r\n:") || !envIdentifier(e) {
			return errors.New("MCP headers must map header names to environment variable names")
		}
	}
	return nil
}
func envIdentifier(v string) bool {
	if v == "" {
		return false
	}
	for i, r := range v {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
func (s *Store) MCPServers() ([]MCPConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readMCPLocked()
}
func (s *Store) readMCPLocked() ([]MCPConfig, error) {
	var v []MCPConfig
	b, err := os.ReadFile(filepath.Join(s.dir, "mcp.json"))
	if errors.Is(err, os.ErrNotExist) {
		return []MCPConfig{}, nil
	}
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(b, &v)
	return v, err
}
func (s *Store) SaveMCPServer(v MCPConfig) error {
	if err := v.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.readMCPLocked()
	if err != nil {
		return err
	}
	found := false
	for i, old := range list {
		if old.ID == v.ID {
			list[i] = v
			found = true
			break
		}
	}
	if !found {
		list = append(list, v)
	}
	return writeJSON(filepath.Join(s.dir, "mcp.json"), list)
}
func (s *Store) DeleteMCPServer(id string) error {
	if !validID(id) {
		return fmt.Errorf("invalid MCP ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.readMCPLocked()
	if err != nil {
		return err
	}
	for i, v := range list {
		if v.ID == id {
			return writeJSON(filepath.Join(s.dir, "mcp.json"), append(list[:i], list[i+1:]...))
		}
	}
	return errors.New("MCP server not found")
}
