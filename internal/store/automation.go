package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Routine is an explicitly enabled automation using a bot's normal permissions.
type Routine struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Enabled         bool      `json:"enabled"`
	BotID           string    `json:"bot_id"`
	Prompt          string    `json:"prompt"`
	Kind            string    `json:"kind"`
	At              time.Time `json:"at,omitempty"`
	Time            string    `json:"time,omitempty"`
	Timezone        string    `json:"timezone,omitempty"`
	IntervalSeconds int       `json:"interval_seconds,omitempty"`
	NextAt          time.Time `json:"next_at,omitempty"`
}

func (v Routine) Validate() error {
	if !validID(v.ID) || !validID(v.BotID) || strings.TrimSpace(v.Name) == "" || len(v.Name) > 120 || strings.TrimSpace(v.Prompt) == "" || len(v.Prompt) > 1<<20 {
		return errors.New("routine ID, bot, name and prompt are required")
	}
	switch v.Kind {
	case "once":
		if v.At.IsZero() {
			return errors.New("once schedule requires an execution time")
		}
	case "interval":
		if v.IntervalSeconds < 1 || v.IntervalSeconds > 365*86400 {
			return errors.New("interval must be between 1 second and 365 days")
		}
	case "weekdays":
		if _, err := time.Parse("15:04", v.Time); err != nil {
			return errors.New("weekday time must be HH:MM")
		}
		if v.Timezone == "" {
			return errors.New("timezone is required")
		}
		if _, err := time.LoadLocation(v.Timezone); err != nil {
			return errors.New("invalid timezone")
		}
	case "webhook":
	default:
		return errors.New("routine kind must be once, weekdays, interval or webhook")
	}
	return nil
}

// Next returns the first occurrence strictly after after for recurring schedules.
func (v Routine) Next(after time.Time) time.Time {
	switch v.Kind {
	case "once":
		return v.At
	case "interval":
		return after.Add(time.Duration(v.IntervalSeconds) * time.Second)
	case "weekdays":
		loc, err := time.LoadLocation(v.Timezone)
		if err != nil {
			return time.Time{}
		}
		clock, err := time.Parse("15:04", v.Time)
		if err != nil {
			return time.Time{}
		}
		day := after.In(loc)
		for n := 0; n < 8; n++ {
			candidate := time.Date(day.Year(), day.Month(), day.Day()+n, clock.Hour(), clock.Minute(), 0, 0, loc)
			if candidate.After(after) && candidate.Weekday() != time.Saturday && candidate.Weekday() != time.Sunday {
				return candidate
			}
		}
	}
	return time.Time{}
}

// RoutineRun records the lifetime of the accepted turn, including queue time.
type RoutineRun struct {
	ID         string    `json:"id"`
	RoutineID  string    `json:"routine_id"`
	BotID      string    `json:"bot_id"`
	Source     string    `json:"source"`
	Status     string    `json:"status"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	MessageID  string    `json:"message_id,omitempty"`
	Result     string    `json:"result,omitempty"`
	Error      string    `json:"error,omitempty"`
}
type automationData struct {
	Routines []Routine    `json:"routines"`
	Runs     []RoutineRun `json:"runs"`
}

func (s *Store) readAutomationLocked() (automationData, error) {
	var v automationData
	b, err := os.ReadFile(filepath.Join(s.dir, "automation.json"))
	if errors.Is(err, os.ErrNotExist) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	err = json.Unmarshal(b, &v)
	return v, err
}
func (s *Store) writeAutomationLocked(v automationData) error {
	return writeJSON(filepath.Join(s.dir, "automation.json"), v)
}
func (s *Store) Routines() ([]Routine, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, e := s.readAutomationLocked()
	return v.Routines, e
}
func (s *Store) SaveRoutine(v Routine) error {
	if err := v.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.readAutomationLocked()
	if err != nil {
		return err
	}
	for i, old := range data.Routines {
		if old.ID == v.ID {
			data.Routines[i] = v
			return s.writeAutomationLocked(data)
		}
	}
	data.Routines = append(data.Routines, v)
	return s.writeAutomationLocked(data)
}
func (s *Store) DeleteRoutine(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.readAutomationLocked()
	if err != nil {
		return err
	}
	for i, v := range data.Routines {
		if v.ID == id {
			data.Routines = append(data.Routines[:i], data.Routines[i+1:]...)
			return s.writeAutomationLocked(data)
		}
	}
	return errors.New("routine not found")
}
func (s *Store) RoutineRuns(id string) ([]RoutineRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.readAutomationLocked()
	if err != nil {
		return nil, err
	}
	runs := []RoutineRun{}
	for i := len(data.Runs) - 1; i >= 0; i-- {
		r := data.Runs[i]
		if id == "" || r.RoutineID == id {
			runs = append(runs, r)
		}
	}
	return runs, nil
}
func (s *Store) SaveRoutineRun(run RoutineRun) error {
	if !validID(run.ID) || !validID(run.RoutineID) {
		return errors.New("invalid run ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.readAutomationLocked()
	if err != nil {
		return err
	}
	for i, v := range data.Runs {
		if v.ID == run.ID {
			data.Runs[i] = run
			return s.writeAutomationLocked(data)
		}
	}
	data.Runs = append(data.Runs, run)
	// Bound completed history without dropping any still-running record.
	if len(data.Runs) > 500 {
		for i, v := range data.Runs {
			if v.Status != "running" {
				data.Runs = append(data.Runs[:i], data.Runs[i+1:]...)
				break
			}
		}
	}
	return s.writeAutomationLocked(data)
}
func (s *Store) RecordRoutineTrigger(r Routine, run RoutineRun, advance bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.readAutomationLocked()
	if err != nil {
		return err
	}
	found := false
	for i, v := range data.Routines {
		if v.ID == r.ID {
			found = true
			if advance {
				data.Routines[i] = r
			}
			break
		}
	}
	if !found {
		return errors.New("routine not found")
	}
	data.Runs = append(data.Runs, run)
	if len(data.Runs) > 500 {
		for i, v := range data.Runs {
			if v.Status != "running" {
				data.Runs = append(data.Runs[:i], data.Runs[i+1:]...)
				break
			}
		}
	}
	return s.writeAutomationLocked(data)
}
func (s *Store) RecoverRoutineRuns(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.readAutomationLocked()
	if err != nil {
		return err
	}
	changed := false
	for i := range data.Runs {
		r := &data.Runs[i]
		if r.Status == "running" {
			r.Status = "interrupted"
			r.Error = "service restarted before the turn completed"
			r.FinishedAt = now
			changed = true
		}
	}
	if changed {
		return s.writeAutomationLocked(data)
	}
	return nil
}

// WebhookConfig is private service state. Never serialize Token to a read API.
type WebhookConfig struct {
	Enabled bool   `json:"enabled"`
	Token   string `json:"token"`
}

func (s *Store) WebhookConfig() (WebhookConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var v WebhookConfig
	b, err := os.ReadFile(filepath.Join(s.dir, "webhook.json"))
	if errors.Is(err, os.ErrNotExist) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	err = json.Unmarshal(b, &v)
	return v, err
}
func (s *Store) SaveWebhookConfig(v WebhookConfig) error {
	if v.Enabled && len(v.Token) != 64 {
		return fmt.Errorf("webhook requires a generated token")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSON(filepath.Join(s.dir, "webhook.json"), v)
}
