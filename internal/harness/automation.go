package harness

import (
	"bqbot/internal/store"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type automationWork struct {
	turn *turn
	done chan struct{}
}

// Automation schedules work through the same FIFO and approvals as chat.
type Automation struct {
	h         *Harness
	mu        sync.Mutex
	active    map[string]*automationWork
	closed    bool
	cancel    context.CancelFunc
	done      chan struct{}
	lastError string
}

func NewAutomation(h *Harness) (*Automation, error) {
	if err := h.Store.RecoverRoutineRuns(time.Now()); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &Automation{h: h, active: map[string]*automationWork{}, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(a.done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case now := <-ticker.C:
				a.Poll(now)
			case <-ctx.Done():
				return
			}
		}
	}()
	return a, nil
}
func (a *Automation) Close() {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.closed = true
	a.cancel()
	var waits []chan struct{}
	for _, w := range a.active {
		a.h.cancelAutomation(w.turn)
		waits = append(waits, w.done)
	}
	a.mu.Unlock()
	for _, done := range waits {
		<-done
	}
	<-a.done
}
func (a *Automation) Error() string { a.mu.Lock(); defer a.mu.Unlock(); return a.lastError }
func (a *Automation) Save(v store.Routine) (store.Routine, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return v, errors.New("automation closed")
	}
	if v.ID == "" {
		v.ID = store.NewID()
	}
	if err := v.Validate(); err != nil {
		return v, err
	}
	if _, err := a.h.Bot(v.BotID); err != nil {
		return v, err
	}
	v.NextAt = time.Time{}
	if v.Enabled {
		v.NextAt = v.Next(time.Now())
	}
	return v, a.h.Store.SaveRoutine(v)
}
func (a *Automation) Delete(id string) error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return errors.New("automation closed")
	}
	err := a.h.Store.DeleteRoutine(id)
	work := a.active[id]
	if err == nil && work != nil {
		a.h.cancelAutomation(work.turn)
	}
	a.mu.Unlock()
	if err == nil && work != nil {
		<-work.done
	}
	return err
}
func (a *Automation) Poll(now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	routines, err := a.h.Store.Routines()
	if err != nil {
		a.lastError = err.Error()
		return
	}
	for _, r := range routines {
		if !r.Enabled || r.Kind == "webhook" || r.NextAt.IsZero() || r.NextAt.After(now) {
			continue
		}
		if _, err = a.fireLocked(r, "schedule", now, true); err != nil {
			a.lastError = err.Error()
		}
	}
}
func (a *Automation) Run(id, source string) (store.RoutineRun, error) {
	return a.RunWithText(id, source, "")
}
func (a *Automation) RunWithText(id, source, text string) (store.RoutineRun, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return store.RoutineRun{}, errors.New("automation closed")
	}
	routines, err := a.h.Store.Routines()
	if err != nil {
		return store.RoutineRun{}, err
	}
	for _, r := range routines {
		if r.ID == id {
			if source == "webhook" && (!r.Enabled || r.Kind != "webhook") {
				return store.RoutineRun{}, errors.New("webhook routine is disabled or has another trigger")
			}
			if text != "" {
				r.Prompt += "\n\nWebhook context:\n" + text
			}
			return a.fireLocked(r, source, time.Now(), false)
		}
	}
	return store.RoutineRun{}, errors.New("routine not found")
}
func (a *Automation) fireLocked(r store.Routine, source string, now time.Time, scheduled bool) (store.RoutineRun, error) {
	run := store.RoutineRun{ID: store.NewID(), RoutineID: r.ID, BotID: r.BotID, Source: source, Status: "running", StartedAt: now}
	if a.active[r.ID] != nil {
		run.Status = "skipped"
		run.Error = "previous run has not finished"
		run.FinishedAt = now
	}
	if scheduled {
		if r.Kind == "once" {
			r.Enabled = false
			r.NextAt = time.Time{}
		} else {
			r.NextAt = r.Next(now)
		}
	}
	if err := a.h.Store.RecordRoutineTrigger(r, run, scheduled); err != nil {
		return run, err
	}
	if run.Status == "skipped" {
		return run, nil
	}
	fail := func(err error) (store.RoutineRun, error) {
		run.Status = "failed"
		run.Error = err.Error()
		run.FinishedAt = time.Now()
		saveErr := a.h.Store.SaveRoutineRun(run)
		return run, errors.Join(err, saveErr)
	}
	bot, err := a.h.Bot(r.BotID)
	if err != nil {
		return fail(err)
	}
	meta, err := a.h.Store.Thread(bot.ID)
	if err != nil {
		return fail(err)
	}
	t := newTurn(context.Background(), bot, store.Message{ID: store.NewID(), Role: "user", Text: r.Prompt, From: "Automation: " + r.Name, At: now}, nil)
	t.thread = meta
	run.MessageID = t.user.ID
	if err = a.h.Store.SaveRoutineRun(run); err != nil {
		t.cancel()
		return fail(err)
	}
	a.h.mu.Lock()
	err = a.h.enqueueLocked(t)
	a.h.mu.Unlock()
	if err != nil {
		return fail(err)
	}
	work := &automationWork{turn: t, done: make(chan struct{})}
	a.active[r.ID] = work
	go func(run store.RoutineRun) {
		defer close(work.done)
		result, turnErr := a.h.waitAutomation(t)
		run.Result = result.Text
		run.FinishedAt = time.Now()
		run.Status = "completed"
		if turnErr != nil {
			run.Error = turnErr.Error()
			run.Status = "failed"
			if errors.Is(turnErr, context.Canceled) {
				run.Status = "canceled"
			}
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if err := a.h.Store.SaveRoutineRun(run); err != nil {
			a.lastError = fmt.Sprintf("save routine history: %v", err)
		}
		delete(a.active, r.ID)
	}(run)
	return run, nil
}

func (h *Harness) cancelAutomation(t *turn) {
	h.mu.Lock()
	if t.chain != nil {
		h.cancelJobsLocked(func(j *asyncJob) bool { return j.source != nil && j.source.chain == t.chain })
	}
	t.cancel()
	if h.running[t.bot.ID] == t {
		h.cancelApprovalsLocked(t)
	} else {
		h.cancelQueuedLocked(t)
	}
	h.mu.Unlock()
}
func (h *Harness) waitAutomation(t *turn) (store.Message, error) {
	<-t.done
	for {
		h.mu.Lock()
		var wait <-chan struct{}
		if t.chain != nil {
			for j := range h.jobs {
				if j.source == nil || j.source.chain != t.chain {
					continue
				}
				for _, participant := range []*turn{j.target, j.wake} {
					if participant == nil {
						continue
					}
					select {
					case <-participant.done:
					default:
						wait = participant.done
					}
				}
			}
		}
		if wait == nil {
			latest := t
			if t.chain != nil && t.chain.latest != nil {
				latest = t.chain.latest
			}
			result, err := latest.result, latest.err
			if t.chain != nil && t.chain.canceled {
				err = context.Canceled
			}
			h.mu.Unlock()
			return result, err
		}
		h.mu.Unlock()
		<-wait
	}
}
