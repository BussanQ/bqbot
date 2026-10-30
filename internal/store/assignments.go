package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Assignment is the durable progress and result of one delegated task.
type Assignment struct {
	ID             string    `json:"id"`
	SourceBotID    string    `json:"source_bot_id"`
	SourceThreadID string    `json:"source_thread_id"`
	TargetBotID    string    `json:"target_bot_id"`
	TargetThreadID string    `json:"target_thread_id"`
	RootBotID      string    `json:"root_bot_id"`
	RootThreadID   string    `json:"root_thread_id"`
	RoomID         string    `json:"room_id,omitempty"`
	ParentID       string    `json:"parent_id,omitempty"`
	ReworkOf       string    `json:"rework_of,omitempty"`
	Task           string    `json:"task"`
	Mode           string    `json:"mode"`
	Consult        bool      `json:"consult,omitempty"`
	Reply          string    `json:"reply"`
	DoneWhen       string    `json:"done_when,omitempty"`
	Depth          int       `json:"depth"`
	Status         string    `json:"status"`
	Result         string    `json:"result,omitempty"`
	Error          string    `json:"error,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (s *Store) assignmentsLocked() ([]Assignment, error) {
	var v []Assignment
	b, err := os.ReadFile(filepath.Join(s.dir, "assignments.json"))
	if errors.Is(err, os.ErrNotExist) {
		return []Assignment{}, nil
	}
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(b, &v)
	return v, err
}
func (s *Store) Assignments() ([]Assignment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.assignmentsLocked()
}
func (s *Store) SaveAssignment(v Assignment) error {
	if !validID(v.ID) {
		return errors.New("invalid assignment ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.assignmentsLocked()
	if err != nil {
		return err
	}
	v.UpdatedAt = time.Now()
	for i, a := range list {
		if a.ID == v.ID {
			list[i] = v
			return writeJSON(filepath.Join(s.dir, "assignments.json"), list)
		}
	}
	list = append(list, v)
	return writeJSON(filepath.Join(s.dir, "assignments.json"), list)
}
func (s *Store) RecoverAssignments() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.assignmentsLocked()
	if err != nil {
		return err
	}
	changed := false
	for i := range list {
		a := &list[i]
		if a.Status == "queued" || a.Status == "running" || a.Status == "waiting" {
			a.Status = "interrupted"
			a.Error = "service restarted before assignment completed"
			a.UpdatedAt = time.Now()
			changed = true
		}
	}
	if changed {
		return writeJSON(filepath.Join(s.dir, "assignments.json"), list)
	}
	return nil
}
