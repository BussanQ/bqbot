package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"

	"bqbot/internal/driver"
	"bqbot/internal/store"
)

type roomRoute struct {
	room   store.Room
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

func (h *Harness) cancelRoutesLocked(match func(*roomRoute) bool) []<-chan struct{} {
	var waits []<-chan struct{}
	for route := range h.routing {
		if match(route) {
			route.cancel()
			waits = append(waits, route.done)
		}
	}
	return waits
}

// SaveRoom serializes channel edits with admission and execution preparation.
func (h *Harness) SaveRoom(room store.Room) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.closingScopes[room.ID] {
		return errors.New("channel is closing")
	}
	for _, id := range room.Members {
		if h.deleting[id] {
			return errors.New("member is being deleted")
		}
	}
	if err := h.Store.SaveRoom(room); err != nil {
		return err
	}
	h.cancelRoutesLocked(func(r *roomRoute) bool { return r.room.ID == room.ID })
	// Reject removed members before queued preparation can acquire the lock.
	h.cancelJobsLocked(func(j *asyncJob) bool {
		return j.roomID == room.ID && (!member(room, j.botID) || !member(room, j.target.bot.ID))
	})
	for _, t := range h.running {
		if t.roomID == room.ID && !member(room, t.bot.ID) {
			t.cancel()
			h.cancelApprovalsLocked(t)
		}
	}
	for _, q := range h.queues {
		for _, t := range append([]*turn(nil), q...) {
			if t.roomID == room.ID && !member(room, t.bot.ID) {
				h.cancelQueuedLocked(t)
			}
		}
	}
	saved, err := h.Store.Room(room.ID)
	if err != nil {
		return err
	}
	h.metadataLocked(Update{RoomID: room.ID, Room: &saved})
	return nil
}
func (h *Harness) DeleteRoom(id string) error {
	h.mu.Lock()
	if _, err := h.Store.Room(id); err != nil {
		h.mu.Unlock()
		return err
	}
	if h.closingScopes[id] {
		h.mu.Unlock()
		return errors.New("channel is closing")
	}
	h.closingScopes[id] = true
	waits := h.cancelScopeLocked(id)
	h.mu.Unlock()
	for _, done := range waits {
		<-done
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	err := h.Store.DeleteRoom(id)
	delete(h.closingScopes, id)
	if err == nil {
		h.metadataLocked(Update{RoomID: id})
	}
	return err
}

// SendRoom records one shared user entry and schedules each explicitly mentioned
// member, or the selected lead/Auto member. Routing is bounded and cancellable.
func (h *Harness) SendRoom(id, text string, attachments []driver.Attachment) (store.Message, error) {
	if err := validateAttachments(attachments); err != nil {
		return store.Message{}, err
	}
	text = strings.TrimSpace(text)
	if len(text) > 1<<20 || (text == "" && len(attachments) == 0) {
		return store.Message{}, errors.New("empty or oversized message")
	}
	h.mu.Lock()
	if h.closed || h.closingScopes[id] {
		h.mu.Unlock()
		return store.Message{}, errors.New("channel is closing")
	}
	room, err := h.Store.Room(id)
	if err != nil {
		h.mu.Unlock()
		return store.Message{}, err
	}
	bots, err := h.Store.Bots()
	if err != nil {
		h.mu.Unlock()
		return store.Message{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	route := &roomRoute{room: room, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	h.routing[route] = true
	h.mu.Unlock()
	targets := roomMentions(room, bots, text)
	if len(targets) == 0 {
		target := room.LeadID
		if room.ReplyMode == "auto" {
			target = h.routeRoomContext(ctx, room, bots, text)
		}
		if target != "" {
			targets = []string{target}
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	defer func() { delete(h.routing, route); cancel(); close(route.done) }()
	if ctx.Err() != nil {
		return store.Message{}, ctx.Err()
	}
	if h.closed || h.closingScopes[id] {
		return store.Message{}, errors.New("channel is closing")
	}
	latest, err := h.Store.Room(id)
	if err != nil {
		return store.Message{}, err
	}
	// Never execute a stale route after membership/configuration changed.
	if !sameRoomConfig(room, latest) {
		return store.Message{}, errors.New("channel changed during routing; retry message")
	}
	user := store.Message{ID: store.NewID(), Role: "user", Text: text, Attachments: append([]driver.Attachment(nil), attachments...), At: time.Now()}
	var turns []*turn
	for _, botID := range targets {
		if !member(room, botID) || h.deleting[botID] {
			return store.Message{}, errors.New("channel target unavailable")
		}
		bot, e := h.Bot(botID)
		if e != nil {
			return store.Message{}, e
		}
		if h.Drivers[bot.Engine] == nil {
			return store.Message{}, errors.New("channel member engine unavailable")
		}
		thread, e := h.Store.RoomThread(id, botID)
		if e != nil {
			return store.Message{}, e
		}
		t := newTurn(context.Background(), bot, user, nil)
		t.thread = thread
		t.roomID = id
		t.roomUserRecorded = true
		turns = append(turns, t)
	}
	if len(turns) == 0 {
		return store.Message{}, errors.New("channel has no reply target")
	}
	if err = h.Store.AppendRoom(id, user); err != nil {
		return store.Message{}, err
	}
	// Publish exactly one shared user entry; execution/queues refer to its ID.
	h.sequence++
	h.broadcastLocked(Update{Sequence: h.sequence, RoomID: id, User: clone(user)})
	for _, t := range turns {
		if err = h.enqueueLocked(t); err != nil {
			return user, err
		}
	}
	return user, nil
}
func sameRoomConfig(a, b store.Room) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
func member(room store.Room, id string) bool {
	for _, v := range room.Members {
		if v == id {
			return true
		}
	}
	return false
}
func roomMentions(room store.Room, bots []store.Bot, text string) []string {
	var result []string
	lower := strings.ToLower(text)
	for _, b := range bots {
		if !member(room, b.ID) {
			continue
		}
		for _, name := range []string{b.ID, b.Name} {
			needle := "@" + strings.ToLower(name)
			found := false
			for start := 0; start < len(lower); {
				i := strings.Index(lower[start:], needle)
				if i < 0 {
					break
				}
				i += start
				end := i + len(needle)
				before := i == 0 || mentionBoundary([]rune(lower[:i])[len([]rune(lower[:i]))-1])
				after := end == len(lower) || mentionBoundary([]rune(lower[end:])[0])
				if before && after {
					found = true
					break
				}
				start = end
			}
			if found {
				result = append(result, b.ID)
				break
			}
		}
	}
	return result
}
func mentionBoundary(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune(",.:;!?，。；：！？()[]{}", r)
}

// routeRoom only accepts an exact eligible ID. Every error, uncertainty, missing
// configuration or credential falls back to lead. Provider bodies never escape.
func (h *Harness) routeRoom(room store.Room, bots []store.Bot, text string) string {
	return h.routeRoomContext(context.Background(), room, bots, text)
}
func (h *Harness) routeRoomContext(parent context.Context, room store.Room, bots []store.Bot, text string) string {
	fallback := room.LeadID
	cfg, err := h.Store.DecisionSettings()
	if err != nil || cfg.Model == "" {
		return fallback
	}
	key := os.Getenv(cfg.APIKeyEnv)
	if key == "" {
		return fallback
	}
	var candidates []map[string]string
	for _, b := range bots {
		if member(room, b.ID) {
			candidates = append(candidates, map[string]string{"id": b.ID, "name": b.Name, "description": clip(b.Persona, 300)})
		}
	}
	roster, _ := json.Marshal(candidates)
	body, _ := json.Marshal(map[string]any{"model": cfg.Model, "stream": false, "temperature": 0, "max_tokens": 100, "messages": []map[string]string{
		{"role": "system", "content": "Choose the single best channel responder. Return only JSON {\"bot_id\":\"ID\"}. If uncertain return {}. Only use IDs in this roster: " + string(roster)},
		{"role": "user", "content": clip(room.Instructions, 2000) + "\n\n" + clip(text, 8000)},
	}})
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return fallback
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return fallback
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fallback
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&response) != nil || len(response.Choices) != 1 {
		return fallback
	}
	var choice struct {
		BotID string `json:"bot_id"`
	}
	if json.Unmarshal([]byte(response.Choices[0].Message.Content), &choice) != nil || !member(room, choice.BotID) {
		return fallback
	}
	return choice.BotID
}
