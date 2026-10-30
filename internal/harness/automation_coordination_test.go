package harness

import (
	"context"
	"strings"
	"testing"

	"bqbot/internal/driver"
	"bqbot/internal/mcp"
	"bqbot/internal/store"
)

func TestAutomationWaitsForCoordinationChain(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	r := newRig(t, &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		if in.Model == "worker" {
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			emit(driver.Event{Kind: driver.EvText, Text: "delegated result"})
			return nil
		}
		if strings.HasPrefix(in.Text, "Handoff completed") {
			emit(driver.Event{Kind: driver.EvText, Text: "final result: " + in.Text})
			return nil
		}
		backend, err := turnBackend(in)
		if err != nil {
			return err
		}
		_, err = backend.Coordinate(ctx, mcp.CoordinateRequest{Assignments: []mcp.AssignmentRequest{{Bot: "worker", Task: "do work"}}})
		if err == nil {
			emit(driver.Event{Kind: driver.EvText, Text: "delegated"})
		}
		return err
	}})
	root := coordinationBot(t, r, "root")
	coordinationBot(t, r, "worker")
	automation, err := NewAutomation(r.h)
	if err != nil {
		t.Fatal(err)
	}
	defer automation.Close()
	routine, err := automation.Save(store.Routine{Name: "chain", BotID: root.ID, Prompt: "coordinate", Kind: "interval", IntervalSeconds: 3600})
	if err != nil {
		t.Fatal(err)
	}
	first, err := automation.Run(routine.ID, "manual")
	if err != nil {
		t.Fatal(err)
	}
	receive(t, started)
	eventually(t, func() bool { return !r.h.Busy(root.ID) })
	second, err := automation.Run(routine.ID, "manual")
	if err != nil || second.Status != "skipped" {
		t.Fatal("overlap escaped coordination chain", second, err)
	}
	history, err := r.st.RoutineRuns(routine.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range history {
		if run.ID == first.ID && run.Status != "running" {
			t.Fatal("premature completion", run)
		}
	}
	close(release)
	eventually(t, func() bool {
		runs, _ := r.st.RoutineRuns(routine.ID)
		for _, run := range runs {
			if run.ID == first.ID {
				return run.Status == "completed" && strings.Contains(run.Result, "delegated result")
			}
		}
		return false
	})
}
