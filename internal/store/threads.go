package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Thread identifies a conversation independently of its bot's other sessions.
// Legacy threads continue to use their original transcript paths.
type Thread struct {
	ID             string    `json:"id"`
	BotID          string    `json:"bot_id"`
	Title          string    `json:"title"`
	AskerID        string    `json:"asker_id,omitempty"`
	RoomID         string    `json:"room_id,omitempty"`
	Cursor         string    `json:"cursor,omitempty"`
	Summary        string    `json:"summary,omitempty"`
	SummaryThrough string    `json:"summary_through,omitempty"`
	Closed         bool      `json:"closed,omitempty"`
	Created        time.Time `json:"created"`
	Legacy         bool      `json:"legacy,omitempty"`
	Revision       uint64    `json:"revision,omitempty"`
	Engine         string    `json:"engine,omitempty"`
	Model          string    `json:"model,omitempty"`
}

func (s *Store) threadIndexPath() string { return filepath.Join(s.dir, "threads", "index.json") }
func readJSONFile(path string, out any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// threadIndexLocked discovers the old default and per-asker conversations once.
// It leaves the transcripts in place, so migration never rewrites user messages.
func (s *Store) threadIndexLocked() ([]Thread, error) {
	var threads []Thread
	if err := readJSONFile(s.threadIndexPath(), &threads); err != nil {
		return nil, err
	}
	bots, err := s.readBots()
	if err != nil {
		return nil, err
	}
	changed := false
	for _, b := range bots {
		found := false
		for _, t := range threads {
			if t.ID == b.ID {
				found = true
				break
			}
		}
		if !found {
			threads = append(threads, Thread{ID: b.ID, BotID: b.ID, Title: "Main", Created: b.Created, Legacy: true, Engine: b.Engine, Model: b.Model})
			changed = true
		}
		askers := map[string]bool{}
		for id := range b.Handoffs {
			askers[id] = true
		}
		entries, e := os.ReadDir(filepath.Join(s.dir, "handoffs", b.ID))
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return nil, e
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".ndjson") {
				askers[strings.TrimSuffix(entry.Name(), ".ndjson")] = true
			}
		}
		// Bots order makes migration and search stable across launches.
		for _, asker := range bots {
			if !askers[asker.ID] {
				continue
			}
			found = false
			for _, t := range threads {
				if t.BotID == b.ID && t.AskerID == asker.ID && t.Legacy {
					found = true
					break
				}
			}
			if found {
				continue
			}
			sum := sha256.Sum256([]byte(b.ID + "/" + asker.ID))
			threads = append(threads, Thread{ID: "handoff-" + hex.EncodeToString(sum[:12]), BotID: b.ID, AskerID: asker.ID, Title: "From " + asker.Name, Cursor: b.Handoffs[asker.ID], Created: b.Created, Legacy: true, Engine: b.Engine, Model: b.Model})
			changed = true
		}
	}
	if changed {
		if err := writeJSON(s.threadIndexPath(), threads); err != nil {
			return nil, err
		}
	}
	// Cursor/config overlays keep legacy APIs and thread APIs in agreement.
	for i := range threads {
		for _, b := range bots {
			if b.ID != threads[i].BotID {
				continue
			}
			t := &threads[i]
			if t.Engine != b.Engine || t.Model != b.Model {
				t.Cursor = ""
			}
			if t.ID == b.ID {
				t.Cursor = b.Cursor
			} else if t.Legacy && t.AskerID != "" {
				t.Cursor = b.Handoffs[t.AskerID]
			}
			t.Engine, t.Model = b.Engine, b.Model
			break
		}
	}
	return threads, nil
}

// Threads includes the main conversation and closed conversations for a bot.
// An empty botID lists all conversations.
func (s *Store) Threads(botID string) ([]Thread, error) {
	if botID != "" && !validID(botID) {
		return nil, errors.New("invalid bot id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ts, err := s.threadIndexLocked()
	if err != nil {
		return nil, err
	}
	result := []Thread{}
	for _, t := range ts {
		if botID == "" || t.BotID == botID {
			result = append(result, t)
		}
	}
	return result, nil
}
func (s *Store) Thread(id string) (Thread, error) {
	if !validID(id) {
		return Thread{}, errors.New("invalid thread id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.threadLocked(id)
}
func (s *Store) threadLocked(id string) (Thread, error) {
	ts, err := s.threadIndexLocked()
	if err != nil {
		return Thread{}, err
	}
	for _, t := range ts {
		if t.ID == id {
			return t, nil
		}
	}
	return Thread{}, fmt.Errorf("thread %q not found", id)
}

// SaveThread creates or edits a title; continuation and compaction state belong
// to the runtime and cannot be overwritten by a stale profile from a client.
func (s *Store) SaveThread(t Thread) error {
	if !validID(t.ID) || !validID(t.BotID) || strings.TrimSpace(t.Title) == "" || len(t.Title) > 256 {
		return errors.New("invalid thread")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ts, err := s.threadIndexLocked()
	if err != nil {
		return err
	}
	bots, err := s.readBots()
	if err != nil {
		return err
	}
	var bot *Bot
	for i := range bots {
		if bots[i].ID == t.BotID {
			bot = &bots[i]
		}
	}
	if bot == nil {
		return errors.New("bot not found")
	}
	for i, old := range ts {
		if old.ID == t.ID {
			if old.BotID != t.BotID || old.AskerID != t.AskerID || old.RoomID != t.RoomID {
				return errors.New("cannot move a thread")
			}
			old.Title = strings.TrimSpace(t.Title)
			ts[i] = old
			return writeJSON(s.threadIndexPath(), ts)
		}
	}
	if t.AskerID != "" {
		found := false
		for _, b := range bots {
			if b.ID == t.AskerID {
				found = true
			}
		}
		if !found {
			return errors.New("asking bot not found")
		}
	}
	if t.RoomID != "" {
		r, err := s.roomLocked(t.RoomID)
		if err != nil {
			return err
		}
		if !contains(r.Members, t.BotID) {
			return errors.New("bot is not a room member")
		}
	}
	t.Created = time.Now()
	t.Cursor = ""
	t.Summary = ""
	t.SummaryThrough = ""
	t.Legacy = false
	t.Closed = false
	t.Engine = bot.Engine
	t.Model = bot.Model
	ts = append(ts, t)
	return writeJSON(s.threadIndexPath(), ts)
}

// CloseThread archives a conversation without deleting its transcript.
func (s *Store) CloseThread(id string) error {
	if !validID(id) {
		return errors.New("invalid thread id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ts, err := s.threadIndexLocked()
	if err != nil {
		return err
	}
	for i, t := range ts {
		if t.ID == id {
			if t.ID == t.BotID {
				return errors.New("cannot close the main conversation")
			}
			ts[i].Closed = true
			return writeJSON(s.threadIndexPath(), ts)
		}
	}
	return errors.New("thread not found")
}
func (s *Store) conversationPath(t Thread) string {
	if t.ID == t.BotID {
		return s.threadPath(t.BotID)
	}
	if t.Legacy && t.AskerID != "" {
		return s.handoffPath(t.BotID, t.AskerID)
	}
	return filepath.Join(s.dir, "threads", "conversations", t.ID+".ndjson")
}
func (s *Store) ThreadTranscript(id string) ([]Message, error) {
	t, err := s.Thread(id)
	if err != nil {
		return nil, err
	}
	return s.read(s.conversationPath(t))
}
func (s *Store) AppendThread(id string, m Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.threadLocked(id)
	if err != nil {
		return err
	}
	if t.Closed {
		return errors.New("thread is closed")
	}
	return s.appendLocked(s.conversationPath(t), m)
}

// invalidateThreadCursorsLocked permanently ends all native conversations for
// an edited engine/model, including an A→B→A edit while a turn is in flight.
func (s *Store) invalidateThreadCursorsLocked(bot Bot) error {
	var threads []Thread
	if err := readJSONFile(s.threadIndexPath(), &threads); err != nil {
		return err
	}
	changed := false
	for i := range threads {
		if threads[i].BotID == bot.ID {
			threads[i].Cursor = ""
			threads[i].Engine, threads[i].Model = bot.Engine, bot.Model
			threads[i].Revision++
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return writeJSON(s.threadIndexPath(), threads)
}

// SaveThreadCursor compares the current continuation and compaction boundary,
// so an old turn cannot restore a cursor after a model edit or compaction.
func (s *Store) SaveThreadCursor(expected Thread, cursor string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts, err := s.threadIndexLocked()
	if err != nil {
		return err
	}
	for i, t := range ts {
		if t.ID != expected.ID {
			continue
		}
		if t.Closed || t.Cursor != expected.Cursor || t.SummaryThrough != expected.SummaryThrough || t.Engine != expected.Engine || t.Model != expected.Model || t.Revision != expected.Revision {
			return nil
		}
		if t.ID == t.BotID || t.Legacy && t.AskerID != "" {
			bots, err := s.readBots()
			if err != nil {
				return err
			}
			for i := range bots {
				b := &bots[i]
				if b.ID != t.BotID {
					continue
				}
				if t.ID == t.BotID {
					b.Cursor = cursor
				} else {
					if b.Handoffs == nil {
						b.Handoffs = map[string]string{}
					}
					b.Handoffs[t.AskerID] = cursor
				}
			}
			if err := s.writeBots(bots); err != nil {
				return err
			}
		}
		ts[i].Cursor = cursor
		return writeJSON(s.threadIndexPath(), ts)
	}
	return nil
}

// HandoffThread reuses an open per-asker conversation or creates a new one
// after the previous thread was closed.
func (s *Store) HandoffThread(botID, askerID string) (Thread, error) {
	return s.relatedThread(botID, askerID, "")
}
func (s *Store) RoomThread(roomID, botID string) (Thread, error) {
	return s.relatedThread(botID, "", roomID)
}
func (s *Store) relatedThread(botID, askerID, roomID string) (Thread, error) {
	if !validID(botID) || (askerID != "" && !validID(askerID)) || (roomID != "" && !validID(roomID)) {
		return Thread{}, errors.New("invalid conversation scope")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ts, err := s.threadIndexLocked()
	if err != nil {
		return Thread{}, err
	}
	bots, err := s.readBots()
	if err != nil {
		return Thread{}, err
	}
	var bot *Bot
	var asker *Bot
	for i := range bots {
		if bots[i].ID == botID {
			bot = &bots[i]
		}
		if bots[i].ID == askerID {
			asker = &bots[i]
		}
	}
	if bot == nil || askerID != "" && asker == nil {
		return Thread{}, errors.New("bot not found")
	}
	title := ""
	if asker != nil {
		title = "From " + asker.Name
	}
	if roomID != "" {
		r, err := s.roomLocked(roomID)
		if err != nil {
			return Thread{}, err
		}
		if !contains(r.Members, botID) {
			return Thread{}, errors.New("bot is not a room member")
		}
		title = "# " + r.Name
	}
	for i := len(ts) - 1; i >= 0; i-- {
		t := ts[i]
		if !t.Closed && t.BotID == botID && t.AskerID == askerID && t.RoomID == roomID && t.ID != botID {
			return t, nil
		}
	}
	t := Thread{ID: NewID(), BotID: botID, AskerID: askerID, RoomID: roomID, Title: title, Created: time.Now(), Engine: bot.Engine, Model: bot.Model}
	if askerID != "" {
		first := true
		for _, old := range ts {
			if old.BotID == botID && old.AskerID == askerID {
				first = false
				break
			}
		}
		if first {
			sum := sha256.Sum256([]byte(botID + "/" + askerID))
			t.ID = "handoff-" + hex.EncodeToString(sum[:12])
			t.Legacy = true
		}
	}
	ts = append(ts, t)
	if err := writeJSON(s.threadIndexPath(), ts); err != nil {
		return Thread{}, err
	}
	return t, nil
}

// CloneBot duplicates a profile with fresh conversation and workspace identity.
func (s *Store) CloneBot(id string) (Bot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	bots, err := s.readBots()
	if err != nil {
		return Bot{}, err
	}
	for _, b := range bots {
		if b.ID != id {
			continue
		}
		b.ID = NewID()
		base := []rune(b.Name)
		for len(string(base)) > 230 {
			base = base[:len(base)-1]
		}
		for n := 1; ; n++ {
			b.Name = string(base) + " copy"
			if n > 1 {
				b.Name += fmt.Sprintf(" %d", n)
			}
			used := false
			for _, existing := range bots {
				if strings.EqualFold(existing.Name, b.Name) {
					used = true
					break
				}
			}
			if !used {
				break
			}
		}
		b.Cursor = ""
		b.Handoffs = nil
		b.Created = time.Now()
		b.Pinned = false
		b.Hidden = false
		b.Unread = false
		if err := s.writeBots(append(bots, b)); err != nil {
			return Bot{}, err
		}
		return b, nil
	}
	return Bot{}, errors.New("bot not found")
}
