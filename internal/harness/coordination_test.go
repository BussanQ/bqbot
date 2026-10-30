package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"bqbot/internal/driver"
	"bqbot/internal/mcp"
	"bqbot/internal/store"
)

func turnBackend(in driver.TurnInput) (*mcp.HTTPBackend, error) {
	if in.MCP == nil {
		return nil, fmt.Errorf("missing MCP grant")
	}
	data, err := os.ReadFile(in.MCP.Args[1])
	if err != nil {
		return nil, err
	}
	var g mcp.Grant
	err = json.Unmarshal(data, &g)
	return &mcp.HTTPBackend{URL: g.URL, Token: g.Token}, err
}
func coordinationBot(t *testing.T, r *rig, id string) store.Bot {
	t.Helper()
	b := store.Bot{ID: id, Name: strings.ToUpper(id), Engine: "test", Model: id, ApprovalMode: "ask"}
	if err := r.st.SaveBot(b); err != nil {
		t.Fatal(err)
	}
	return b
}
func TestCoordinationFourLayersAndFinalResult(t *testing.T) {
	next := map[string]string{"a": "b", "b": "c", "c": "d", "d": "e", "e": "f"}
	d := &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		if in.ApprovalMode != "full-auto" {
			return fmt.Errorf("permission inheritance lost")
		}
		if strings.HasPrefix(in.Text, "Handoff completed") {
			emit(driver.Event{Kind: driver.EvText, Text: "resumed " + in.Text})
			return nil
		}
		b, err := turnBackend(in)
		if err != nil {
			return err
		}
		if in.Model != "a" {
			if _, err = b.Coordinate(ctx, mcp.CoordinateRequest{Assignments: []mcp.AssignmentRequest{{Bot: "a", Task: "back edge"}}}); err == nil {
				return fmt.Errorf("ancestor accepted")
			}
		}
		text, err := b.Coordinate(ctx, mcp.CoordinateRequest{Assignments: []mcp.AssignmentRequest{{Bot: next[in.Model], Task: "next layer"}}})
		if in.Model == "e" {
			if err == nil || !strings.Contains(err.Error(), "depth") {
				return fmt.Errorf("fifth layer accepted: %v", err)
			}
			emit(driver.Event{Kind: driver.EvText, Text: "leaf-result"})
			return nil
		}
		if err != nil {
			return err
		}
		emit(driver.Event{Kind: driver.EvText, Text: text})
		return nil
	}}
	r := newRig(t, d)
	var root store.Bot
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		b := coordinationBot(t, r, id)
		if id == "a" {
			root = b
		}
	}
	root.ApprovalMode = "full-auto"
	r.st.SaveBot(root)
	if _, err := r.h.Send(root, "start"); err != nil {
		t.Fatal(err)
	}
	waitJobs(t, r.h)
	messages, _ := r.st.Transcript("a")
	if len(messages) != 4 || !strings.Contains(messages[3].Text, "leaf-result") {
		t.Fatalf("root finished before nested result: %+v", messages)
	}
	assignments, err := r.st.Assignments()
	if err != nil || len(assignments) != 4 {
		t.Fatal(assignments, err)
	}
	for _, a := range assignments {
		if a.Status != "completed" || !strings.Contains(a.Result, "leaf-result") {
			t.Fatalf("premature assignment result %+v", a)
		}
	}
}
func TestCoordinationBatchConsultNotificationAndStatusScope(t *testing.T) {
	var calls atomic.Int32
	var id atomic.Value
	d := &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		backend, err := turnBackend(in)
		if err != nil {
			return err
		}
		switch in.Model {
		case "a":
			receipt, err := backend.Coordinate(ctx, mcp.CoordinateRequest{Assignments: []mcp.AssignmentRequest{{Bot: "b", Task: "advise", Mode: "consult", Reply: "none", DoneWhen: "report risks"}, {Bot: "c", Task: "notify", Reply: "none"}}})
			if err != nil {
				return err
			}
			var parsed struct {
				Assignments []assignmentReceipt `json:"assignments"`
			}
			json.Unmarshal([]byte(receipt), &parsed)
			id.Store(parsed.Assignments[0].ID)
			if _, err = backend.AssignmentStatus(ctx, parsed.Assignments[0].ID); err != nil {
				return err
			}
			emit(driver.Event{Kind: driver.EvText, Text: "notifications sent"})
		case "b":
			if !strings.Contains(in.Text, "report risks") {
				return fmt.Errorf("acceptance criterion lost")
			}
			result, err := in.Approve(ctx, driver.ApprovalRequest{Kind: "tool", Tool: "write", Input: json.RawMessage(`{}`)})
			if err != nil || result.Outcome != "deny" {
				return fmt.Errorf("consult allowed a write")
			}
			calls.Add(1)
			emit(driver.Event{Kind: driver.EvText, Text: "advice"})
		case "c":
			emit(driver.Event{Kind: driver.EvText, Text: "noted"})
		case "d":
			if _, err = backend.AssignmentStatus(ctx, id.Load().(string)); err == nil {
				return fmt.Errorf("unrelated assignment exposed")
			}
			emit(driver.Event{Kind: driver.EvText, Text: "scoped"})
		}
		return nil
	}}
	r := newRig(t, d)
	a := coordinationBot(t, r, "a")
	for _, id := range []string{"b", "c", "d"} {
		coordinationBot(t, r, id)
	}
	a.ApprovalMode = "full-auto"
	r.st.SaveBot(a)
	r.h.Send(a, "batch")
	waitJobs(t, r.h)
	messages, _ := r.st.Transcript(a.ID)
	if len(messages) != 2 || calls.Load() != 1 {
		t.Fatal(messages, calls.Load())
	}
	outsider, _ := r.h.Bot("d")
	r.h.Send(outsider, "status")
	done := r.wait("d")
	if done.Err != nil || done.Reply.Text != "scoped" {
		t.Fatal(done)
	}
}
func TestCoordinationRework(t *testing.T) {
	var wakes atomic.Int32
	d := &testDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		if in.Model == "b" {
			text := "first draft"
			if strings.Contains(in.Text, "Correction requested:") {
				text = "fixed draft"
			}
			emit(driver.Event{Kind: driver.EvText, Text: text})
			return nil
		}
		backend, err := turnBackend(in)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(in.Text, "Handoff completed") {
			text, err := backend.Coordinate(ctx, mcp.CoordinateRequest{Assignments: []mcp.AssignmentRequest{{Bot: "b", Task: "draft"}}})
			emit(driver.Event{Kind: driver.EvText, Text: text})
			return err
		}
		if wakes.Add(1) == 1 {
			data, err := backend.AssignmentStatus(ctx, "")
			if err != nil {
				return err
			}
			var assignments []store.Assignment
			json.Unmarshal([]byte(data), &assignments)
			text, err := backend.Coordinate(ctx, mcp.CoordinateRequest{Assignments: []mcp.AssignmentRequest{{Bot: "b", Task: "fix the issue", Mode: "rework", AssignmentID: assignments[0].ID, DoneWhen: "issue fixed"}}})
			emit(driver.Event{Kind: driver.EvText, Text: text})
			return err
		}
		emit(driver.Event{Kind: driver.EvText, Text: in.Text})
		return nil
	}}
	r := newRig(t, d)
	a := coordinationBot(t, r, "a")
	coordinationBot(t, r, "b")
	r.h.Send(a, "start")
	waitJobs(t, r.h)
	assignments, _ := r.st.Assignments()
	if len(assignments) != 2 || assignments[1].ReworkOf != assignments[0].ID || assignments[0].TargetThreadID != assignments[1].TargetThreadID {
		t.Fatal(assignments)
	}
	messages, _ := r.st.Transcript("a")
	if len(messages) != 6 || !strings.Contains(messages[5].Text, "fixed draft") {
		t.Fatal(messages)
	}
}
