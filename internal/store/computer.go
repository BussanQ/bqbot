package store

import (
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// ComputerConfig selects an existing desktop. Credentials are environment references.
// Host access requires separate explicit consent, even when the connection is enabled.
type ComputerConfig struct {
	BotID       string `json:"bot_id"`
	Provider    string `json:"provider"`
	Enabled     bool   `json:"enabled"`
	HostConsent bool   `json:"host_consent"`
	URL         string `json:"url"`
	TokenEnv    string `json:"token_env,omitempty"`
	SandboxID   string `json:"sandbox_id,omitempty"`
}

func (c ComputerConfig) Validate() error {
	if !validID(c.BotID) {
		return errors.New("invalid computer bot ID")
	}
	if c.Provider != "boat" && c.Provider != "vm" && c.Provider != "host" {
		return errors.New("computer provider must be boat, vm or host")
	}
	u, e := url.Parse(c.URL)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("computer URL must not contain credentials, query or fragment")
	}
	local := net.ParseIP(u.Hostname()) != nil && net.ParseIP(u.Hostname()).IsLoopback()
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return errors.New("computer URL requires HTTPS or literal loopback HTTP")
	}
	if c.Provider == "host" && !local {
		return errors.New("host Cua must use a literal loopback address")
	}
	if c.Provider == "host" && c.Enabled && !c.HostConsent {
		return errors.New("host control requires explicit consent")
	}
	if c.TokenEnv != "" && !envIdentifier(c.TokenEnv) {
		return errors.New("computer token must name an environment variable")
	}
	if c.Provider == "boat" && (!validID(c.SandboxID) || c.TokenEnv == "") {
		return errors.New("Boat requires sandbox ID and token environment variable")
	}
	if strings.ContainsAny(c.URL, "\r\n\x00") {
		return errors.New("invalid computer URL")
	}
	return nil
}

func (s *Store) Computers() ([]ComputerConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readComputersLocked()
}
func (s *Store) readComputersLocked() ([]ComputerConfig, error) {
	b, e := os.ReadFile(filepath.Join(s.dir, "computers.json"))
	if errors.Is(e, os.ErrNotExist) {
		return []ComputerConfig{}, nil
	}
	if e != nil {
		return nil, e
	}
	var v []ComputerConfig
	e = json.Unmarshal(b, &v)
	return v, e
}
func (s *Store) SaveComputer(c ComputerConfig) error {
	if e := c.Validate(); e != nil {
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list, e := s.readComputersLocked()
	if e != nil {
		return e
	}
	for i, v := range list {
		if v.BotID == c.BotID {
			list[i] = c
			return writeJSON(filepath.Join(s.dir, "computers.json"), list)
		}
	}
	return writeJSON(filepath.Join(s.dir, "computers.json"), append(list, c))
}
func (s *Store) Computer(botID string) (ComputerConfig, error) {
	list, e := s.Computers()
	if e != nil {
		return ComputerConfig{}, e
	}
	for _, c := range list {
		if c.BotID == botID {
			return c, nil
		}
	}
	return ComputerConfig{}, errors.New("computer is not configured")
}
