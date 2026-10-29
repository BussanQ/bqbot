package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"bqbot/internal/driver"
)

func TestProfileEditPreservesLatestCursor(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	original := Bot{ID: NewID(), Name: "Ada", Engine: "codex", Cursor: "before"}
	if err := s.SaveBot(original); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCursor(original, "", "after"); err != nil {
		t.Fatal(err)
	}
	original.Name = "Renamed"
	original.ApprovalMode = "auto-edit"
	if err := s.SaveProfile(original); err != nil {
		t.Fatal(err)
	}
	bots, err := s.Bots()
	if err != nil {
		t.Fatal(err)
	}
	if bots[0].Cursor != "after" || bots[0].Name != "Renamed" || bots[0].ApprovalMode != "auto-edit" {
		t.Fatalf("profile overwrote continuation: %+v", bots[0])
	}
	original.Model = "new-model"
	if err := s.SaveProfile(original); err != nil {
		t.Fatal(err)
	}
	bots, err = s.Bots()
	if err != nil {
		t.Fatal(err)
	}
	if bots[0].Cursor != "" {
		t.Fatal("model change kept incompatible continuation")
	}
}

func TestSettingsEventsAndSessionEdits(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	settings, err := s.Settings()
	if err != nil || len(settings) != 0 {
		t.Fatal(settings, err)
	}
	want := map[string]driver.EngineSettings{"claude": {Path: "/custom/claude", APIKeyEnv: "MY_API_KEY", Models: []string{"one"}}}
	if err = s.SaveSettings(want); err != nil {
		t.Fatal(err)
	}
	reopened, _ := Open(dir)
	got, err := reopened.Settings()
	if err != nil || got["claude"].Path != "/custom/claude" {
		t.Fatal(got, err)
	}
	info, _ := os.Stat(filepath.Join(dir, "settings.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	b := Bot{ID: NewID(), Name: "Ada", Engine: "claude", Cursor: "old", Effort: "high", ApprovalMode: "auto-edit"}
	s.SaveBot(b)
	edited := b
	edited.Model = "new"
	s.SaveBot(edited)
	if err = s.SaveCursor(b, "", "obsolete"); err != nil {
		t.Fatal(err)
	}
	bots, _ := s.Bots()
	if bots[0].Model != "new" || bots[0].Cursor != "" {
		t.Fatal(bots)
	}
	current := bots[0]
	current.Name = "renamed"
	s.SaveBot(current)
	if err = s.SaveCursor(bots[0], "", "fresh"); err != nil {
		t.Fatal(err)
	}
	bots, _ = s.Bots()
	if bots[0].Cursor != "fresh" || bots[0].Name != "renamed" {
		t.Fatal(bots)
	}
	if err = s.SaveCursor(current, "", "stale"); err != nil {
		t.Fatal(err)
	}
	bots, _ = s.Bots()
	if bots[0].Cursor != "fresh" {
		t.Fatal("stale cursor overwrote newer cursor")
	}
	peer := Bot{ID: NewID(), Engine: "echo"}
	s.SaveBot(peer)
	for _, pair := range [][2]string{{b.ID, ""}, {b.ID, peer.ID}, {peer.ID, b.ID}} {
		if err = s.AppendEvent(pair[0], pair[1], map[string]string{"type": "busy"}); err != nil {
			t.Fatal(err)
		}
	}
	events, err := s.Events(b.ID, peer.ID)
	if err != nil || len(events) != 1 || !json.Valid(events[0].Data) {
		t.Fatal(events, err)
	}
	if err = s.DeleteBot(b.ID); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{b.ID, ""}, {b.ID, peer.ID}, {peer.ID, b.ID}} {
		events, err = s.Events(pair[0], pair[1])
		if err != nil || len(events) != 0 {
			t.Fatal(events, err)
		}
	}
	if err = s.AppendEvent("../bad", "", nil); err == nil {
		t.Fatal("path traversal accepted")
	}
}
