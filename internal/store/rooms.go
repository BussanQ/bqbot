package store

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Room is a shared conversation with explicit membership and reply policy.
type Room struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Instructions string    `json:"instructions,omitempty"`
	WorkDir      string    `json:"work_dir,omitempty"`
	Members      []string  `json:"members"`
	LeadID       string    `json:"lead_id"`
	ReplyMode    string    `json:"reply_mode"`
	Created      time.Time `json:"created"`
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func (s *Store) roomsPath() string { return filepath.Join(s.dir, "rooms.json") }
func (s *Store) roomsLocked() ([]Room, error) {
	rooms := []Room{}
	err := readJSONFile(s.roomsPath(), &rooms)
	return rooms, err
}
func (s *Store) Rooms() ([]Room, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.roomsLocked() }
func (s *Store) Room(id string) (Room, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.roomLocked(id)
}
func (s *Store) roomLocked(id string) (Room, error) {
	if !validID(id) {
		return Room{}, errors.New("invalid room id")
	}
	rooms, err := s.roomsLocked()
	if err != nil {
		return Room{}, err
	}
	for _, r := range rooms {
		if r.ID == id {
			return r, nil
		}
	}
	return Room{}, errors.New("room not found")
}
func (s *Store) SaveRoom(r Room) error {
	if !validID(r.ID) || strings.TrimSpace(r.Name) == "" || len(r.Name) > 120 || len(r.Instructions) > 128<<10 {
		return errors.New("invalid room")
	}
	if r.ReplyMode == "" {
		r.ReplyMode = "lead"
	}
	if r.ReplyMode != "lead" && r.ReplyMode != "auto" {
		return errors.New("invalid room reply mode")
	}
	if len(r.Members) == 0 || len(r.Members) > 32 || !contains(r.Members, r.LeadID) {
		return errors.New("room requires members and a member as lead")
	}
	if r.WorkDir != "" {
		if !filepath.IsAbs(r.WorkDir) {
			return errors.New("room working directory must be absolute")
		}
		info, err := os.Stat(r.WorkDir)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return errors.New("room working directory is not a directory")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	bots, err := s.readBots()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, id := range r.Members {
		if seen[id] {
			return errors.New("duplicate room member")
		}
		seen[id] = true
		found := false
		for _, b := range bots {
			if b.ID == id {
				found = true
			}
		}
		if !found {
			return errors.New("room member not found")
		}
	}
	rooms, err := s.roomsLocked()
	if err != nil {
		return err
	}
	r.Name = strings.TrimSpace(r.Name)
	for i, old := range rooms {
		if old.ID == r.ID {
			r.Created = old.Created
			rooms[i] = r
			return writeJSON(s.roomsPath(), rooms)
		}
	}
	r.Created = time.Now()
	return writeJSON(s.roomsPath(), append(rooms, r))
}
func (s *Store) roomTranscriptPath(id string) string {
	return filepath.Join(s.dir, "rooms", id+".ndjson")
}
func (s *Store) RoomTranscript(id string) ([]Message, error) {
	if _, err := s.Room(id); err != nil {
		return nil, err
	}
	return s.read(s.roomTranscriptPath(id))
}
func (s *Store) AppendRoom(id string, m Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.roomLocked(id); err != nil {
		return err
	}
	return s.appendLocked(s.roomTranscriptPath(id), m)
}
func (s *Store) RoomWorkDir(id string) (string, error) {
	r, err := s.Room(id)
	if err != nil {
		return "", err
	}
	if r.WorkDir != "" {
		return r.WorkDir, nil
	}
	path := filepath.Join(s.dir, "work", "rooms", id)
	return path, os.MkdirAll(path, 0700)
}
func (s *Store) DeleteRoom(id string) error {
	if !validID(id) {
		return errors.New("invalid room id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rooms, err := s.roomsLocked()
	if err != nil {
		return err
	}
	kept := rooms[:0]
	found := false
	for _, r := range rooms {
		if r.ID == id {
			found = true
		} else {
			kept = append(kept, r)
		}
	}
	if !found {
		return errors.New("room not found")
	}
	if err := writeJSON(s.roomsPath(), kept); err != nil {
		return err
	}
	if err := s.removeThreadsLocked(func(t Thread) bool { return t.RoomID == id }); err != nil {
		return err
	}
	for _, path := range []string{s.roomTranscriptPath(id), s.memoryPath(id), filepath.Join(s.dir, "work", "rooms", id), filepath.Join(s.dir, "events", "rooms", id+".ndjson")} {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	// A custom working directory is user-owned and is deliberately retained.
	return nil
}
func (s *Store) removeThreadsLocked(remove func(Thread) bool) error {
	var ts []Thread
	if err := readJSONFile(s.threadIndexPath(), &ts); err != nil {
		return err
	}
	kept := ts[:0]
	for _, t := range ts {
		if !remove(t) {
			kept = append(kept, t)
			continue
		}
		for _, path := range []string{s.conversationPath(t), s.memoryPath(t.ID), filepath.Join(s.dir, "events", t.ID+".ndjson")} {
			if err := os.RemoveAll(path); err != nil {
				return err
			}
		}
	}
	return writeJSON(s.threadIndexPath(), kept)
}
func (s *Store) deleteChatDataLocked(id string) error {
	if err := s.removeThreadsLocked(func(t Thread) bool { return t.BotID == id || t.AskerID == id }); err != nil {
		return err
	}
	rooms, err := s.roomsLocked()
	if err != nil {
		return err
	}
	for i := range rooms {
		r := &rooms[i]
		members := []string{}
		for _, m := range r.Members {
			if m != id {
				members = append(members, m)
			}
		}
		r.Members = members
		if r.LeadID == id {
			r.LeadID = ""
			if len(members) > 0 {
				r.LeadID = members[0]
			}
		}
	}
	if err := writeJSON(s.roomsPath(), rooms); err != nil {
		return err
	}
	return os.RemoveAll(s.memoryPath(id))
}

// DecisionSettings stores provider configuration, never the secret itself.
type DecisionSettings struct {
	BaseURL   string `json:"base_url"`
	APIKeyEnv string `json:"api_key_env"`
	Model     string `json:"model"`
}

func (s *Store) DecisionSettings() (DecisionSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := DecisionSettings{BaseURL: "https://openrouter.ai/api/v1", APIKeyEnv: "OPENROUTER_API_KEY"}
	err := readJSONFile(filepath.Join(s.dir, "decision.json"), &v)
	return v, err
}
func (s *Store) SaveDecisionSettings(v DecisionSettings) error {
	if v.BaseURL == "" {
		v.BaseURL = "https://openrouter.ai/api/v1"
	}
	if v.APIKeyEnv == "" {
		v.APIKeyEnv = "OPENROUTER_API_KEY"
	}
	u, err := url.Parse(v.BaseURL)
	if err != nil || u == nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid decision model base URL")
	}
	if len(v.Model) > 200 || strings.ContainsAny(v.Model, "\r\n\x00") {
		return errors.New("invalid decision model")
	}
	for i, r := range v.APIKeyEnv {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9') {
			return errors.New("invalid API key environment variable")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSON(filepath.Join(s.dir, "decision.json"), v)
}
