package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"bqbot/internal/store"
	"bqbot/internal/team"
)

var teamImportMu sync.Mutex

type TeamImportResult struct {
	Bots     []store.Bot     `json:"bots"`
	Rooms    []store.Room    `json:"rooms"`
	Routines []store.Routine `json:"routines"`
}

type RunRequest struct {
	Bot            string `json:"bot"`
	Text           string `json:"text"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}
type RunResult struct {
	ThreadID string          `json:"thread_id"`
	UserID   string          `json:"user_id"`
	Status   string          `json:"status"`
	Text     string          `json:"text"`
	Error    string          `json:"error,omitempty"`
	Messages []store.Message `json:"messages,omitempty"`
}

func (s *Server) teamRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/team/presets", func(w http.ResponseWriter, r *http.Request) { result(w, team.Presets(), nil) })
	mux.HandleFunc("POST /v1/team/preview", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Markdown string `json:"markdown"`
		}
		if !decode(w, r, &req) {
			return
		}
		v, e := team.Parse(req.Markdown)
		if e != nil {
			fail(w, invalid(e.Error()))
			return
		}
		result(w, v, nil)
	})
	mux.HandleFunc("POST /v1/team/apply", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Markdown string `json:"markdown"`
		}
		if !decode(w, r, &req) {
			return
		}
		d, e := team.Parse(req.Markdown)
		if e != nil {
			fail(w, invalid(e.Error()))
			return
		}
		v, e := s.applyTeam(d)
		result(w, v, e)
	})
	mux.HandleFunc("POST /v1/team/export", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name   string `json:"name"`
			RoomID string `json:"room_id,omitempty"`
		}
		if !decode(w, r, &req) {
			return
		}
		v, e := s.exportTeam(req.Name, req.RoomID)
		result(w, map[string]string{"markdown": v}, e)
	})
	mux.HandleFunc("POST /v1/run", func(w http.ResponseWriter, r *http.Request) {
		var req RunRequest
		if !decode(w, r, &req) {
			return
		}
		v, e := s.runOnce(r.Context(), req)
		result(w, v, e)
	})
}
func (s *Server) applyTeam(d team.Document) (out TeamImportResult, err error) {
	teamImportMu.Lock()
	defer teamImportMu.Unlock()
	if err = d.Validate(); err != nil {
		return out, invalid(err.Error())
	}
	settings, err := s.h.Store.Settings()
	if err != nil {
		return out, err
	}
	existing, err := s.h.Store.Bots()
	if err != nil {
		return out, err
	}
	used := map[string]bool{}
	for _, b := range existing {
		used[b.Name] = true
	}
	ids := map[string]string{}
	for _, b := range d.Bots {
		if used[b.Name] {
			return out, invalid("bot name already exists: " + b.Name)
		}
		v := store.Bot{ID: store.NewID(), Name: b.Name, Engine: b.Engine, Model: b.Model, Persona: b.Persona, Avatar: b.Avatar, ApprovalMode: "ask", Created: time.Now()}
		if err = s.validateBot(v, settings); err != nil {
			return out, err
		}
		ids[b.Name] = v.ID
		out.Bots = append(out.Bots, v)
	}
	rooms, err := s.h.Store.Rooms()
	if err != nil {
		return out, err
	}
	roomNames := map[string]bool{}
	for _, r := range rooms {
		roomNames[r.Name] = true
	}
	for _, r := range d.Rooms {
		if roomNames[r.Name] {
			return out, invalid("room name already exists: " + r.Name)
		}
		v := store.Room{ID: store.NewID(), Name: r.Name, Instructions: r.Instructions, LeadID: ids[r.Lead], ReplyMode: r.ReplyMode}
		for _, n := range r.Members {
			v.Members = append(v.Members, ids[n])
		}
		out.Rooms = append(out.Rooms, v)
	}
	for _, r := range d.Routines {
		out.Routines = append(out.Routines, r.Local(store.NewID(), ids[r.Bot]))
	}
	// All validation precedes writes. Track only our fresh IDs for rollback; no
	// existing bot, transcript or routine is ever overwritten by an import.
	var botsDone, roomsDone, routinesDone []string
	defer func() {
		if err == nil {
			return
		}
		var cleanup error
		for _, id := range routinesDone {
			cleanup = errors.Join(cleanup, s.h.Store.DeleteRoutine(id))
		}
		for _, id := range roomsDone {
			cleanup = errors.Join(cleanup, s.h.DeleteRoom(id))
		}
		for _, id := range botsDone {
			cleanup = errors.Join(cleanup, s.h.DeleteBot(id))
		}
		if cleanup != nil {
			err = fmt.Errorf("team import failed (%v); rollback incomplete: %w", err, cleanup)
		}
		out = TeamImportResult{}
	}()
	for _, b := range out.Bots {
		if err = s.h.SaveProfile(b); err != nil {
			return
		}
		botsDone = append(botsDone, b.ID)
	}
	for i, r := range out.Rooms {
		if err = s.h.SaveRoom(r); err != nil {
			return
		}
		roomsDone = append(roomsDone, r.ID)
		out.Rooms[i], err = s.h.Store.Room(r.ID)
		if err != nil {
			return
		}
	}
	for _, r := range out.Routines {
		if err = s.h.Store.SaveRoutine(r); err != nil {
			return
		}
		routinesDone = append(routinesDone, r.ID)
	}
	return
}
func (s *Server) exportTeam(name, roomID string) (string, error) {
	bots, err := s.h.Store.Bots()
	if err != nil {
		return "", err
	}
	rooms, err := s.h.Store.Rooms()
	if err != nil {
		return "", err
	}
	routines, err := s.h.Store.Routines()
	if err != nil {
		return "", err
	}
	if roomID != "" {
		var selected *store.Room
		for i := range rooms {
			if rooms[i].ID == roomID {
				selected = &rooms[i]
				break
			}
		}
		if selected == nil {
			return "", invalid("room not found")
		}
		keep := map[string]bool{}
		for _, id := range selected.Members {
			keep[id] = true
		}
		filtered := []store.Bot{}
		for _, b := range bots {
			if keep[b.ID] {
				filtered = append(filtered, b)
			}
		}
		bots = filtered
		rooms = []store.Room{*selected}
		if name == "" {
			name = selected.Name
		}
	}
	if name == "" {
		name = "bqbot team"
	}
	d, err := team.Export(name, bots, rooms, routines)
	if err != nil {
		return "", err
	}
	return team.Format(d)
}
func (s *Server) runOnce(parent context.Context, req RunRequest) (RunResult, error) {
	out := RunResult{Status: "failed"}
	if strings.TrimSpace(req.Text) == "" || len(req.Text) > 1<<20 || req.TimeoutSeconds < 0 || req.TimeoutSeconds > 86400 {
		return out, invalid("invalid run prompt or timeout")
	}
	bots, err := s.h.Store.Bots()
	if err != nil {
		return out, err
	}
	var chosen *store.Bot
	for i := range bots {
		if bots[i].ID == req.Bot {
			chosen = &bots[i]
			break
		}
	}
	if chosen == nil {
		for i := range bots {
			if bots[i].Name == req.Bot {
				if chosen != nil {
					return out, invalid("ambiguous bot name")
				}
				chosen = &bots[i]
			}
		}
	}
	if chosen == nil {
		return out, invalid("bot not found")
	}
	if req.TimeoutSeconds == 0 {
		req.TimeoutSeconds = 300
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(req.TimeoutSeconds)*time.Second)
	defer cancel()
	thread, err := s.h.CreateThread(chosen.ID, "CLI run "+time.Now().Format(time.RFC3339))
	if err != nil {
		return out, err
	}
	out.ThreadID = thread.ID
	updates, unsubscribe := s.h.Subscribe()
	defer unsubscribe()
	defer s.h.StopThread(thread.ID)
	user, err := s.h.SendThread(thread.ID, req.Text, nil, "queue")
	if err != nil {
		return out, err
	}
	out.UserID = user.ID
	completed := false
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	finish := func() (RunResult, error) {
		if e := s.h.CloseThread(thread.ID); e != nil {
			return out, e
		}
		out.Messages, err = s.h.Store.ThreadTranscript(thread.ID)
		if err != nil {
			return out, err
		}
		for _, m := range out.Messages {
			if m.Role == "assistant" {
				out.Text = m.Text
			}
			if m.Role == "error" && out.Status == "completed" {
				out.Status, out.Error = "failed", m.Text
			}
		}
		if out.Status == "completed" && strings.HasSuffix(out.Text, "*(stopped)*") {
			out.Status, out.Error = "canceled", "run stopped"
		}
		return out, nil
	}
	for {
		select {
		case <-ctx.Done():
			out.Status = "canceled"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				out.Status = "timeout"
			}
			out.Error = ctx.Err().Error()
			return finish()
		case u, ok := <-updates:
			if !ok {
				return out, errors.New("service closed")
			}
			if u.ThreadID != thread.ID {
				continue
			}
			if u.Approval != nil {
				out.Status = "approval_required"
				out.Error = "interactive approval required; run canceled"
				return finish()
			}
			if u.Error != "" {
				out.Error = u.Error
				return finish()
			}
			if u.Done {
				completed = true
				if u.Reply != nil {
					out.Text = u.Reply.Text
				}
			}
		case <-ticker.C:
			// Wait for delegated work and automatic continuations in this invocation.
			assignments, e := s.h.Store.Assignments()
			if e != nil {
				return out, e
			}
			waiting := false
			chainThreads := map[string]bool{thread.ID: true}
			for _, a := range assignments {
				if a.RootThreadID != thread.ID {
					continue
				}
				chainThreads[a.TargetThreadID] = true
				if a.Status == "queued" || a.Status == "running" || a.Status == "waiting" {
					waiting = true
				}
			}
			for _, u := range s.h.Snapshot() {
				if chainThreads[u.ThreadID] && u.Approval != nil {
					out.Status, out.Error = "approval_required", "delegated work requires interactive approval; run canceled"
					return finish()
				}
				if u.ThreadID == thread.ID && (u.Busy || u.Queued > 0) {
					waiting = true
				}
			}
			if completed && !waiting {
				out.Status = "completed"
				return finish()
			}
		}
	}
}

func (c *Client) PreviewTeam(markdown string) (v team.Document, err error) {
	err = c.do("POST", "/v1/team/preview", map[string]string{"markdown": markdown}, &v)
	return
}
func (c *Client) ApplyTeam(markdown string) (v TeamImportResult, err error) {
	err = c.do("POST", "/v1/team/apply", map[string]string{"markdown": markdown}, &v)
	return
}
func (c *Client) TeamPresets() (v []team.Document, err error) {
	err = c.do("GET", "/v1/team/presets", nil, &v)
	return
}
func (c *Client) ExportTeam(name, roomID string) (string, error) {
	var out struct {
		Markdown string `json:"markdown"`
	}
	err := c.do("POST", "/v1/team/export", map[string]string{"name": name, "room_id": roomID}, &out)
	return out.Markdown, err
}

// Run uses the caller's cancellation/deadline rather than the normal 45s request limit.
func (c *Client) Run(ctx context.Context, req RunRequest) (v RunResult, err error) {
	clone := *c
	client := *c.http
	transport := c.http.Transport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 0
	defer transport.CloseIdleConnections()
	client.Transport = transport
	clone.http = &client
	resp, err := clone.request(ctx, "POST", "/v1/run", req)
	if err != nil {
		return v, err
	}
	defer resp.Body.Close()
	err = decodeRunResponse(resp.Body, &v)
	return
}

func decodeRunResponse(r io.Reader, v *RunResult) error {
	data, err := io.ReadAll(io.LimitReader(r, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxResponseBytes {
		return errors.New("run response exceeds limit")
	}
	return json.Unmarshal(data, v)
}
