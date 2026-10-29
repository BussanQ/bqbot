// Package store keeps bots and their transcripts on disk, under the app's
// home directory (~/.bqbot, or $BQBOT_HOME).
package store

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"bqbot/internal/driver"
)

// Bot is an agent the person chats with.
type Bot struct {
	Effort       string `json:"effort,omitempty"`
	ApprovalMode string `json:"approval_mode,omitempty"`
	ID           string `json:"id"`
	Name         string `json:"name"`
	Engine       string `json:"engine"`            // driver name: claude, codex, pi, echo
	Model        string `json:"model,omitempty"`   // "" is the engine's default
	Persona      string `json:"persona,omitempty"` // system prompt
	Cursor       string `json:"cursor,omitempty"`  // provider-native session to resume
	// Handoffs are the sessions in which the bot answers other bots, apart
	// from its conversation with the person: the asking bot's ID to the
	// session to resume.
	Handoffs map[string]string `json:"handoffs,omitempty"`
	Created  time.Time         `json:"created"`
}

// Message is one entry of a bot's transcript.
type Message struct {
	Attachments []driver.Attachment `json:"attachments,omitempty"`
	ID          string              `json:"id"`
	Role        string              `json:"role"` // user, assistant, error
	Text        string              `json:"text"`
	From        string              `json:"from,omitempty"` // in a handoff transcript, the user message's asking bot
	Tools       []string            `json:"tools,omitempty"`
	At          time.Time           `json:"at"`
}

// Store is the app's data directory. Its methods are safe for concurrent use.
type Store struct {
	dir string
	mu  sync.Mutex
}

// DefaultDir is $BQBOT_HOME, or ~/.bqbot.
func DefaultDir() (string, error) {
	if d := os.Getenv("BQBOT_HOME"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".bqbot"), nil
}

// Open opens the store at dir, creating it.
func Open(dir string) (*Store, error) {
	for _, d := range []string{dir, filepath.Join(dir, "threads"), filepath.Join(dir, "work"), filepath.Join(dir, "handoffs")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	return &Store{dir: dir}, nil
}

func (s *Store) botsPath() string { return filepath.Join(s.dir, "bots.json") }

// Bots returns the bots in the order they were created.
func (s *Store) Bots() ([]Bot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readBots()
}

func (s *Store) readBots() ([]Bot, error) {
	b, err := os.ReadFile(s.botsPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var bots []Bot
	if err := json.Unmarshal(b, &bots); err != nil {
		return nil, fmt.Errorf("%s: %w", s.botsPath(), err)
	}
	return bots, nil
}

func (s *Store) writeBots(bots []Bot) error {
	return writeJSON(s.botsPath(), bots)
}

// SaveBot adds the bot, or replaces the one with its ID.
func (s *Store) SaveBot(bot Bot) error {
	if !validID(bot.ID) {
		return fmt.Errorf("invalid bot id %q", bot.ID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	bots, err := s.readBots()
	if err != nil {
		return err
	}
	for i := range bots {
		if bots[i].ID == bot.ID {
			if !sameSession(bots[i], bot) {
				bot.Cursor = ""
				bot.Handoffs = nil
			}
			bots[i] = bot
			return s.writeBots(bots)
		}
	}
	return s.writeBots(append(bots, bot))
}

// DeleteBot removes the bot, its transcript, its handoff transcripts (both
// ways) and its working directory.
func (s *Store) DeleteBot(id string) error {
	if !validID(id) {
		return fmt.Errorf("invalid bot id %q", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	bots, err := s.readBots()
	if err != nil {
		return err
	}
	kept := bots[:0]
	for _, b := range bots {
		if b.ID != id {
			delete(b.Handoffs, id) // its session with the deleted bot
			kept = append(kept, b)
		}
	}
	if err := s.writeBots(kept); err != nil {
		return err
	}
	if err := os.Remove(s.threadPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.RemoveAll(filepath.Join(s.dir, "handoffs", id)); err != nil {
		return err
	}
	for _, b := range kept {
		if err := os.Remove(s.handoffPath(b.ID, id)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := s.deleteEvents(id, kept); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(s.dir, "work", id))
}

// WorkDir returns the bot's own working directory, creating it.
func (s *Store) WorkDir(id string) (string, error) {
	if !validID(id) {
		return "", fmt.Errorf("invalid bot id %q", id)
	}
	d := filepath.Join(s.dir, "work", id)
	return d, os.MkdirAll(d, 0o700)
}

func (s *Store) threadPath(id string) string {
	return filepath.Join(s.dir, "threads", id+".ndjson")
}

func (s *Store) handoffPath(botID, askerID string) string {
	return filepath.Join(s.dir, "handoffs", botID, askerID+".ndjson")
}

// Append adds a message to the bot's transcript.
func (s *Store) Append(botID string, m Message) error {
	if !validID(botID) {
		return fmt.Errorf("invalid bot id %q", botID)
	}
	return s.appendTo(s.threadPath(botID), m)
}

// AppendHandoff adds a message to the transcript of the bot's session with
// the asking bot.
func (s *Store) AppendHandoff(botID, askerID string, m Message) error {
	if !validID(botID) || !validID(askerID) {
		return fmt.Errorf("invalid bot id %q or %q", botID, askerID)
	}
	return s.appendTo(s.handoffPath(botID, askerID), m)
}

func (s *Store) appendTo(path string, m any) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// Transcript returns the bot's messages, oldest first. A line that does not
// parse, as one cut short by a crash, is skipped.
func (s *Store) Transcript(botID string) ([]Message, error) {
	if !validID(botID) {
		return nil, fmt.Errorf("invalid bot id %q", botID)
	}
	return s.read(s.threadPath(botID))
}

// HandoffTranscript returns the messages of the bot's session with the
// asking bot, oldest first.
func (s *Store) HandoffTranscript(botID, askerID string) ([]Message, error) {
	if !validID(botID) || !validID(askerID) {
		return nil, fmt.Errorf("invalid bot id %q or %q", botID, askerID)
	}
	return s.read(s.handoffPath(botID, askerID))
}

func (s *Store) read(path string) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var msgs []Message
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 32<<20)
	for sc.Scan() {
		var m Message
		if json.Unmarshal(sc.Bytes(), &m) == nil {
			msgs = append(msgs, m)
		}
	}
	return msgs, sc.Err()
}

// NewID returns a random ID.
func NewID() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// validID keeps IDs to what NewID makes, so they are safe in file paths.
func validID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r == '-') {
			return false
		}
	}
	return true
}
