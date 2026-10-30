package harness

import (
	"bqbot/internal/driver"
	"bqbot/internal/store"
	"context"
	"testing"
	"time"
)

func TestAutomationSkipUntilCompletionAndDeleteCancels(t *testing.T) {
	started := make(chan struct{}, 4)
	r := newRig(t, &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}})
	bot := r.bot("Worker", "test", "")
	a, err := NewAutomation(r.h)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	routine, err := a.Save(store.Routine{Name: "Daily", Enabled: true, BotID: bot.ID, Prompt: "work", Kind: "interval", IntervalSeconds: 3600})
	if err != nil {
		t.Fatal(err)
	}
	first, err := a.Run(routine.ID, "manual")
	if err != nil || first.Status != "running" {
		t.Fatal(first, err)
	}
	receive(t, started)
	second, err := a.Run(routine.ID, "manual")
	if err != nil || second.Status != "skipped" {
		t.Fatal(second, err)
	}
	if err = a.Delete(routine.ID); err != nil {
		t.Fatal(err)
	}
	history, err := r.st.RoutineRuns(routine.ID)
	if err != nil || len(history) != 2 {
		t.Fatal(history, err)
	}
	if history[1].Status != "canceled" || r.h.Busy(bot.ID) {
		t.Fatal(history)
	}
}
func TestAutomationOnceAndRestartRecovery(t *testing.T) {
	r := newRig(t, &driver.Echo{Delay: time.Millisecond})
	b := r.bot("Echo", "echo", "")
	a, err := NewAutomation(r.h)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	routine, err := a.Save(store.Routine{Name: "Once", Enabled: true, BotID: b.ID, Prompt: "one shot", Kind: "once", At: now.Add(-time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	a.Poll(now)
	eventually(t, func() bool {
		history, _ := r.st.RoutineRuns(routine.ID)
		return len(history) == 1 && history[0].Status == "completed"
	})
	a.Poll(now.Add(time.Hour))
	history, _ := r.st.RoutineRuns(routine.ID)
	if len(history) != 1 {
		t.Fatal("once schedule ran twice", history)
	}
	a.Close()
	if err = r.st.SaveRoutineRun(store.RoutineRun{ID: "interrupted", RoutineID: routine.ID, Status: "running", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewAutomation(r.h)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	history, _ = r.st.RoutineRuns(routine.ID)
	if history[0].Status != "interrupted" {
		t.Fatal(history)
	}
	routines, _ := r.st.Routines()
	if routines[0].Enabled {
		t.Fatal("once schedule reenabled on restart")
	}
}
func TestAutomationWeekdaysTimezone(t *testing.T) {
	r := store.Routine{Kind: "weekdays", Time: "09:00", Timezone: "Asia/Shanghai"}
	friday := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	next := r.Next(friday)
	if next.Weekday() != time.Monday || next.Hour() != 9 || next.Day() != 2 {
		t.Fatalf("next weekday %s", next)
	}
	before := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	if got := r.Next(before); got.Weekday() != time.Friday || got.Hour() != 9 {
		t.Fatal(got)
	}
}
