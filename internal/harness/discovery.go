package harness

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"

	"bqbot/internal/store"
)

func (hd *handoff) allowedTarget(from *turn, id string) bool {
	if id == from.bot.ID || slices.Contains(from.ancestors, id) || from.depth >= 4 {
		return false
	}
	if from.roomID == "" {
		return true
	}
	room, err := hd.h.Store.Room(from.roomID)
	return err == nil && member(room, id)
}

// A tool grant can see its bot's private threads, handoffs it asked for, and
// threads in its current room. It cannot enumerate unrelated private threads.
func (hd *handoff) allowedThread(from *turn, t store.Thread) bool {
	if from.roomID != "" {
		return t.RoomID == from.roomID
	}
	return t.RoomID == "" && (t.BotID == from.bot.ID || t.AskerID == from.bot.ID)
}
func (hd *handoff) serveThreads(w http.ResponseWriter, r *http.Request) {
	from := hd.caller(r, false)
	if from == nil {
		reply(w, 401, "", errors.New("invalid tool grant"))
		return
	}
	bots, err := hd.h.Store.Bots()
	if err != nil {
		reply(w, 500, "", err)
		return
	}
	var threads []store.Thread
	for _, b := range bots {
		list, e := hd.h.Store.Threads(b.ID)
		if e != nil {
			reply(w, 500, "", e)
			return
		}
		for _, t := range list {
			if hd.allowedThread(from, t) {
				t.Cursor = ""
				t.Summary = ""
				t.SummaryThrough = ""
				threads = append(threads, t)
			}
		}
	}
	data, _ := json.Marshal(threads)
	reply(w, 200, string(data), nil)
}
func (hd *handoff) serveCloseThread(w http.ResponseWriter, r *http.Request) {
	from := hd.caller(r, false)
	if from == nil {
		reply(w, 401, "", errors.New("invalid tool grant"))
		return
	}
	if from.consult {
		reply(w, 403, "", errors.New("consult cannot close threads"))
		return
	}
	var req struct {
		ThreadID string `json:"thread_id"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		reply(w, 400, "", errors.New("invalid thread request"))
		return
	}
	hd.h.mu.Lock()
	defer hd.h.mu.Unlock()
	if from.ctx.Err() != nil {
		reply(w, 401, "", errors.New("expired tool grant"))
		return
	}
	thread, err := hd.h.Store.Thread(req.ThreadID)
	if err != nil || !hd.allowedThread(from, thread) {
		reply(w, 403, "", errors.New("thread is outside this grant"))
		return
	}
	// Closing a caller or its bot's queued work from inside a tool can deadlock
	// the driver waiting for the tool response. Only inactive scopes may close.
	active := thread.ID == from.thread.ID || thread.ID == thread.BotID || hd.h.closingScopes[thread.ID]
	for _, t := range hd.h.running {
		if t.thread.ID == thread.ID {
			active = true
		}
	}
	for j := range hd.h.jobs {
		if j.threadID == thread.ID || j.target.thread.ID == thread.ID {
			for _, t := range []*turn{j.target, j.wake} {
				if t != nil && hd.h.running[t.bot.ID] == t {
					active = true
				}
			}
		}
	}
	if active {
		reply(w, 409, "", errors.New("cannot close an active thread from a tool"))
		return
	}
	// Admission cannot race this check; cancel only queued/inactive work and
	// close under the same mutex. A tool callback never waits on an engine.
	hd.h.cancelScopeLocked(thread.ID)
	err = hd.h.Store.CloseThread(thread.ID)
	if err == nil {
		hd.h.metadataLocked(Update{BotID: thread.BotID, ThreadID: thread.ID, RoomID: thread.RoomID})
	}
	reply(w, 200, "Thread closed.", err)
}
func (hd *handoff) serveRoomTargets(w http.ResponseWriter, r *http.Request) {
	from := hd.caller(r, false)
	if from == nil {
		reply(w, 401, "", errors.New("invalid tool grant"))
		return
	}
	var targets []map[string]string
	if from.roomID != "" {
		room, err := hd.h.Store.Room(from.roomID)
		if err != nil {
			reply(w, 500, "", err)
			return
		}
		for _, id := range room.Members {
			if hd.allowedTarget(from, id) {
				b, e := hd.h.Bot(id)
				if e == nil {
					targets = append(targets, map[string]string{"id": b.ID, "name": b.Name, "room_id": room.ID})
				}
			}
		}
	}
	data, _ := json.Marshal(targets)
	reply(w, 200, string(data), nil)
}
