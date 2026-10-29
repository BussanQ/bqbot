package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Memory is a user-maintained note recalled in this conversation's context.
type Memory struct {
	ID      string    `json:"id"`
	Text    string    `json:"text"`
	Created time.Time `json:"created"`
}

func (s *Store) memoryPath(scopeID string) string {
	return filepath.Join(s.dir, "memories", scopeID+".json")
}
func (s *Store) scopeLocked(id string) error {
	if !validID(id) {
		return errors.New("invalid memory scope")
	}
	ts, err := s.threadIndexLocked()
	if err != nil {
		return err
	}
	for _, t := range ts {
		if t.ID == id {
			return nil
		}
	}
	_, err = s.roomLocked(id)
	return err
}
func (s *Store) memoriesLocked(id string) ([]Memory, error) {
	if err := s.scopeLocked(id); err != nil {
		return nil, err
	}
	v := []Memory{}
	err := readJSONFile(s.memoryPath(id), &v)
	return v, err
}
func (s *Store) Memories(scopeID string) ([]Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.memoriesLocked(scopeID)
}
func (s *Store) SaveMemory(scopeID string, m Memory) error {
	if !validID(m.ID) || strings.TrimSpace(m.Text) == "" || len(m.Text) > 8<<10 {
		return errors.New("invalid memory")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	notes, err := s.memoriesLocked(scopeID)
	if err != nil {
		return err
	}
	m.Text = strings.TrimSpace(m.Text)
	found := false
	for i, old := range notes {
		if old.ID == m.ID {
			m.Created = old.Created
			notes[i] = m
			found = true
			break
		}
	}
	if !found {
		if len(notes) >= 100 {
			return errors.New("memory limit reached")
		}
		m.Created = time.Now()
		notes = append(notes, m)
	}
	if err := os.MkdirAll(filepath.Dir(s.memoryPath(scopeID)), 0700); err != nil {
		return err
	}
	return writeJSON(s.memoryPath(scopeID), notes)
}
func (s *Store) DeleteMemory(scopeID, id string) error {
	if !validID(id) {
		return errors.New("invalid memory id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	notes, err := s.memoriesLocked(scopeID)
	if err != nil {
		return err
	}
	kept := notes[:0]
	found := false
	for _, m := range notes {
		if m.ID == id {
			found = true
		} else {
			kept = append(kept, m)
		}
	}
	if !found {
		return errors.New("memory not found")
	}
	return writeJSON(s.memoryPath(scopeID), kept)
}

// Recall ranks locally stored notes by literal keywords and recency. It uses
// no network service; the bounded result can safely be added to a turn prompt.
func (s *Store) Recall(scopeID, query string) ([]Memory, error) {
	notes, err := s.Memories(scopeID)
	if err != nil {
		return nil, err
	}
	terms := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) })
	score := func(m Memory) int {
		n := 0
		text := strings.ToLower(m.Text)
		for _, term := range terms {
			if term != "" && strings.Contains(text, term) {
				n++
			}
		}
		return n
	}
	sort.SliceStable(notes, func(i, j int) bool {
		a, b := score(notes[i]), score(notes[j])
		if a != b {
			return a > b
		}
		return notes[i].Created.After(notes[j].Created)
	})
	out := []Memory{}
	size := 0
	for _, m := range notes {
		if len(out) >= 8 {
			break
		}
		if size+len(m.Text) > 12<<10 {
			continue
		}
		out = append(out, m)
		size += len(m.Text)
	}
	return out, nil
}

// SearchResult locates the matching message in its original conversation.
type SearchResult struct {
	BotID    string  `json:"bot_id,omitempty"`
	ThreadID string  `json:"thread_id,omitempty"`
	RoomID   string  `json:"room_id,omitempty"`
	Message  Message `json:"message"`
}

// Search performs a case-insensitive literal search; it includes closed threads
// and room transcripts, and limits the response to the newest 100 matches.
func (s *Store) Search(query string) ([]SearchResult, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" || len(query) > 512 {
		return nil, errors.New("search requires 1–512 bytes")
	}
	ts, err := s.Threads("")
	if err != nil {
		return nil, err
	}
	out := []SearchResult{}
	add := func(rows []Message, botID, threadID, roomID string) {
		for _, m := range rows {
			if strings.Contains(strings.ToLower(m.Text), query) {
				out = append(out, SearchResult{BotID: botID, ThreadID: threadID, RoomID: roomID, Message: m})
			}
		}
	}
	for _, t := range ts {
		if t.RoomID != "" {
			continue
		}
		rows, err := s.ThreadTranscript(t.ID)
		if err != nil {
			return nil, err
		}
		add(rows, t.BotID, t.ID, "")
	}
	rooms, err := s.Rooms()
	if err != nil {
		return nil, err
	}
	for _, r := range rooms {
		rows, err := s.RoomTranscript(r.ID)
		if err != nil {
			return nil, err
		}
		add(rows, "", "", r.ID)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Message.At.After(out[j].Message.At) })
	if len(out) > 100 {
		out = out[:100]
	}
	return out, nil
}

// CompactThread preserves the transcript, retaining the last twelve entries
// verbatim for replay and a bounded extractive digest of the older entries.
// This is local lossy context compression, not a model-generated summary.
func (s *Store) CompactThread(id string) (Thread, error) {
	if !validID(id) {
		return Thread{}, errors.New("invalid thread id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ts, err := s.threadIndexLocked()
	if err != nil {
		return Thread{}, err
	}
	for i, t := range ts {
		if t.ID != id {
			continue
		}
		if t.Closed {
			return Thread{}, errors.New("thread is closed")
		}
		rows, err := readMessages(s.conversationPath(t))
		if err != nil {
			return Thread{}, err
		}
		end := len(rows) - 12
		if end <= 0 {
			return t, nil
		}
		start := 0
		if t.SummaryThrough != "" {
			for j, m := range rows {
				if m.ID == t.SummaryThrough {
					start = j + 1
					break
				}
			}
		}
		if start >= end {
			return t, nil
		}
		var summary strings.Builder
		if t.Summary != "" {
			summary.WriteString(t.Summary)
			summary.WriteByte('\n')
		}
		for _, m := range rows[start:end] {
			if m.Role != "user" && m.Role != "assistant" {
				continue
			}
			text := truncateRunes(m.Text, 600)
			fmt.Fprintf(&summary, "%s: %s\n", m.Role, text)
			if len(m.Attachments) > 0 {
				fmt.Fprintf(&summary, "[%d image attachments retained in transcript]\n", len(m.Attachments))
			}
		}
		text := []rune(summary.String())
		if len(text) > 8000 {
			text = append([]rune("[Earlier excerpts omitted]\n"), text[len(text)-7970:]...)
		}
		t.Summary = string(text)
		t.SummaryThrough = rows[end-1].ID
		t.Cursor = ""
		if t.ID == t.BotID || t.Legacy && t.AskerID != "" {
			bots, err := s.readBots()
			if err != nil {
				return Thread{}, err
			}
			for j := range bots {
				if bots[j].ID != t.BotID {
					continue
				}
				if t.ID == t.BotID {
					bots[j].Cursor = ""
				} else {
					delete(bots[j].Handoffs, t.AskerID)
				}
			}
			if err := s.writeBots(bots); err != nil {
				return Thread{}, err
			}
		}
		ts[i] = t
		if err := writeJSON(s.threadIndexPath(), ts); err != nil {
			return Thread{}, err
		}
		return t, nil
	}
	return Thread{}, errors.New("thread not found")
}
func truncateRunes(text string, limit int) string {
	r := []rune(text)
	if len(r) <= limit {
		return text
	}
	return string(r[:limit]) + "…"
}
