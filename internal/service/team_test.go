package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bqbot/internal/driver"
	"bqbot/internal/store"
	"bqbot/internal/team"
)

func TestTeamApplyExportAndConflict(t *testing.T) {
	_, api, h := newTestService(t, nil)
	d := team.Document{Version: 1, Name: "Group", Bots: []team.Bot{{Name: "First", Engine: "echo"}, {Name: "Second", Engine: "echo"}}, Rooms: []team.Room{{Name: "Group", Members: []string{"First", "Second"}, Lead: "First"}}, Routines: []team.Routine{{Name: "Daily", Bot: "First", Kind: "interval", IntervalSeconds: 60, Prompt: "hello"}}}
	markdown, err := team.Format(d)
	if err != nil {
		t.Fatal(err)
	}
	v, err := api.ApplyTeam(markdown)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Bots) != 2 || len(v.Rooms) != 1 || len(v.Routines) != 1 || v.Routines[0].Enabled || v.Bots[0].ApprovalMode != "ask" {
		t.Fatalf("invalid import: %#v", v)
	}
	if _, err := api.ApplyTeam(markdown); err == nil {
		t.Fatal("duplicate names accepted")
	}
	bots, _ := h.Store.Bots()
	if len(bots) != 2 {
		t.Fatal("conflict partially wrote bots")
	}
	exported, err := api.ExportTeam("Share", v.Rooms[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := team.Parse(exported)
	if err != nil || len(parsed.Bots) != 2 || len(parsed.Routines) != 1 {
		t.Fatalf("bad export: %v %s", err, exported)
	}
}
func TestTeamWriteFailureRollsBackFreshObjects(t *testing.T) {
	_, api, h := newTestService(t, nil)
	work, err := h.Store.WorkDir("probe")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(filepath.Dir(work))
	if err := os.WriteFile(filepath.Join(root, "automation.json"), []byte("not JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	d := team.Document{Version: 1, Name: "Rollback", Bots: []team.Bot{{Name: "Fresh", Engine: "echo"}}, Routines: []team.Routine{{Name: "r", Bot: "Fresh", Kind: "webhook", Prompt: "test"}}}
	markdown, err := team.Format(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.ApplyTeam(markdown); err == nil {
		t.Fatal("broken automation store accepted")
	}
	bots, err := h.Store.Bots()
	if err != nil || len(bots) != 0 {
		t.Fatalf("partial bot remained: %#v %v", bots, err)
	}
}

func TestTeamValidationBeforeWrites(t *testing.T) {
	_, api, h := newTestService(t, nil)
	d := team.Document{Version: 1, Name: "T", Bots: []team.Bot{{Name: "Valid", Engine: "echo"}, {Name: "Invalid", Engine: "missing"}}}
	text, _ := team.Format(d)
	if _, err := api.ApplyTeam(text); err == nil {
		t.Fatal("bad engine accepted")
	}
	bots, _ := h.Store.Bots()
	if len(bots) != 0 {
		t.Fatal("partial import", bots)
	}
}
func TestHeadlessRunIsolatedThreadAndFailure(t *testing.T) {
	_, api, h := newTestService(t, nil)
	b := mustSave(t, api, "bot")
	h.Store.Append(b.ID, store.Message{ID: store.NewID(), Role: "assistant", Text: "unrelated-old-reply", At: time.Now()})
	v, err := api.Run(context.Background(), RunRequest{Bot: b.ID, Text: "own prompt", TimeoutSeconds: 3})
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != "completed" || v.Text != "own prompt" || v.ThreadID == b.ID || len(v.Messages) != 2 {
		t.Fatalf("wrong result %#v", v)
	}
}
func TestHeadlessRunEngineFailureAndCancel(t *testing.T) {
	d := &fakeDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		if in.Text == "fail" {
			return errors.New("engine failed")
		}
		<-ctx.Done()
		return ctx.Err()
	}}
	_, api, h := newTestService(t, d)
	b := mustSave(t, api, "bot")
	v, err := api.Run(context.Background(), RunRequest{Bot: b.ID, Text: "fail", TimeoutSeconds: 3})
	if err != nil || v.Status != "failed" || v.Error != "engine failed" {
		t.Fatalf("failure=%#v err=%v", v, err)
	}
	meta, err := h.Store.Thread(v.ThreadID)
	if err != nil || !meta.Closed {
		t.Fatalf("run thread not closed: %#v %v", meta, err)
	}
}

func TestHeadlessRunApprovalAndTimeout(t *testing.T) {
	d := &fakeDriver{run: func(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
		if strings.Contains(in.Text, "approval") {
			_, err := in.Approve(ctx, driver.ApprovalRequest{ID: "approval", Kind: "tool", Tool: "write", Input: []byte(`{"path":"file"}`)})
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}}
	_, api, _ := newTestService(t, d)
	b := mustSave(t, api, "bot")
	v, err := api.Run(context.Background(), RunRequest{Bot: b.ID, Text: "approval", TimeoutSeconds: 3})
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != "approval_required" {
		t.Fatalf("approval result %#v", v)
	}
	v, err = api.Run(context.Background(), RunRequest{Bot: b.ID, Text: "wait", TimeoutSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != "timeout" {
		t.Fatalf("timeout result %#v", v)
	}
}
