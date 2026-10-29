package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"bqbot/internal/driver"
)

// Settings returns engine configuration, or an empty map on a fresh store.
func (s *Store) Settings() (map[string]driver.EngineSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	settings := map[string]driver.EngineSettings{}
	b, err := os.ReadFile(filepath.Join(s.dir, "settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		return settings, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(b, &settings); err != nil {
		return nil, err
	}
	if settings == nil {
		settings = map[string]driver.EngineSettings{}
	}
	return settings, nil
}

// SaveSettings atomically replaces the engine configuration.
func (s *Store) SaveSettings(settings map[string]driver.EngineSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSON(filepath.Join(s.dir, "settings.json"), settings)
}

func writeJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".bqbot-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// SaveProfile saves editable bot fields while keeping the service-owned
// continuation cursors when the engine/model are unchanged.
func (s *Store) SaveProfile(bot Bot) error {
	if !validID(bot.ID) {
		return fmt.Errorf("invalid bot id %q", bot.ID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	bots, err := s.readBots()
	if err != nil {
		return err
	}
	for i, old := range bots {
		if old.ID != bot.ID {
			continue
		}
		bot.Created = old.Created
		bot.Cursor, bot.Handoffs = "", nil
		if sameSession(old, bot) {
			bot.Cursor, bot.Handoffs = old.Cursor, old.Handoffs
		}
		bots[i] = bot
		return s.writeBots(bots)
	}
	bot.Cursor, bot.Handoffs = "", nil
	if bot.Created.IsZero() {
		bot.Created = time.Now()
	}
	return s.writeBots(append(bots, bot))
}

func sameSession(a, b Bot) bool {
	return a.Engine == b.Engine && a.Model == b.Model
}

// SaveCursor updates only the cursor, provided the session and configuration
// have not been edited since the turn began. It never resurrects a deleted bot.
func (s *Store) SaveCursor(expected Bot, asker, cursor string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	bots, err := s.readBots()
	if err != nil {
		return err
	}
	for i := range bots {
		b := &bots[i]
		if b.ID != expected.ID {
			continue
		}
		if !sameSession(*b, expected) {
			return nil
		}
		if asker == "" {
			if b.Cursor != expected.Cursor {
				return nil
			}
			b.Cursor = cursor
		} else {
			if b.Handoffs[asker] != expected.Handoffs[asker] {
				return nil
			}
			if b.Handoffs == nil {
				b.Handoffs = map[string]string{}
			}
			b.Handoffs[asker] = cursor
		}
		return s.writeBots(bots)
	}
	return nil
}

// RuntimeEvent is a durable runtime update. AskerID selects the separate
// handoff thread; Data holds the harness wire update without a store dependency.
type RuntimeEvent struct {
	ID   string          `json:"id"`
	At   time.Time       `json:"at"`
	Data json.RawMessage `json:"data"`
}

func (s *Store) eventPath(botID, askerID string) (string, error) {
	if !validID(botID) || (askerID != "" && !validID(askerID)) {
		return "", fmt.Errorf("invalid thread id")
	}
	if askerID == "" {
		return filepath.Join(s.dir, "events", botID+".ndjson"), nil
	}
	return filepath.Join(s.dir, "events", "handoffs", botID, askerID+".ndjson"), nil
}

// AppendEvent syncs an event to disk before returning.
func (s *Store) AppendEvent(botID, askerID string, value any) error {
	p, err := s.eventPath(botID, askerID)
	if err != nil {
		return err
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.appendTo(p, RuntimeEvent{ID: NewID(), At: time.Now(), Data: b})
}

// Events returns valid runtime events in persistence order.
func (s *Store) Events(botID, askerID string) ([]RuntimeEvent, error) {
	p, err := s.eventPath(botID, askerID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var events []RuntimeEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 65536), 32<<20)
	for sc.Scan() {
		var e RuntimeEvent
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			events = append(events, e)
		}
	}
	return events, sc.Err()
}
func (s *Store) deleteEvents(id string, kept []Bot) error {
	paths := []string{filepath.Join(s.dir, "events", id+".ndjson"), filepath.Join(s.dir, "events", "handoffs", id)}
	for _, b := range kept {
		paths = append(paths, filepath.Join(s.dir, "events", "handoffs", b.ID, id+".ndjson"))
	}
	for _, p := range paths {
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	return nil
}
