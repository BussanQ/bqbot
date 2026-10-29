package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"bqbot/internal/driver"
	"bqbot/internal/harness"
	"bqbot/internal/store"
	"encoding/json"
)

// With BQ_FRAMES set, saves a chat with a handoff, tools and code, in the
// light and the dark, under the traffic lights of a hidden title bar.
func TestChatFrames(t *testing.T) {
	dir := os.Getenv("BQ_FRAMES")
	if dir == "" {
		t.Skip()
	}
	for _, dark := range []bool{false, true} {
		a, _ := testApp(t)
		ada := store.Bot{ID: store.NewID(), Name: "Ada", Engine: "echo", Model: "sonnet"}
		bo := store.Bot{ID: store.NewID(), Name: "Bo", Engine: "echo"}
		testStore(a).SaveBot(ada)
		testStore(a).SaveBot(bo)
		now := time.Now()
		testStore(a).Append(ada.ID, store.Message{ID: "u1", Role: "user", Text: "List the files and ask Bo to check go.mod", At: now})
		testStore(a).Append(ada.ID, store.Message{ID: "r1", Role: "assistant", At: now,
			Text:  "Done. Files:\n\n- `main.go`\n- `view.go`\n\nBo says **go.mod** is fine.\n\n```go\nmodule bqbot\n```",
			Tools: []string{"已转交给 Bo", "Bash ls", "Read go.mod"}})
		a.bots, a.selected = []store.Bot{ada, bo}, ada.ID
		a.threads[bo.ID] = &thread{loaded: true, unread: 2}
		tt := ui.NewTester(a.view, 1000, 680)
		tt.SetTitleBar(ui.TitleBar{Height: 52, Left: 78})
		tt.SetDark(dark)
		tt.Advance(time.Second)
		tt.Frame()
		name := "bqbot-light.png"
		if dark {
			name = "bqbot-dark.png"
		}
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		png.Encode(f, tt.Image())
		f.Close()
	}
}

// TestPhase2Frames renders the same approval and model controls exercised by UI tests.
func TestPhase2Frames(t *testing.T) {
	dir := os.Getenv("BQ_FRAMES")
	if dir == "" {
		t.Skip("BQ_FRAMES is not set")
	}
	for _, dark := range []bool{false, true} {
		a, _ := testApp(t)
		b := store.Bot{ID: store.NewID(), Name: "Ada", Engine: "claude", Model: "sonnet", Effort: "high", ApprovalMode: "ask"}
		if err := testStore(a).SaveBot(b); err != nil {
			t.Fatal(err)
		}
		a.bots, a.selected, a.engines = []store.Bot{b}, b.ID, []string{"claude", "codex", "echo"}
		a.thread(b.ID).msgs = []store.Message{{ID: "u1", Role: "user", Text: "Update the README and ask me which branch to use.", At: time.Now()}}
		a.fold(harness.Update{BotID: b.ID, Busy: true, Reply: &store.Message{ID: "r1", Role: "assistant", Text: "I’m ready to update the README. Please confirm this edit.", At: time.Now()}, Approval: &driver.ApprovalRequest{ID: "edit-1", Kind: "tool", Tool: "Edit", Input: json.RawMessage(`{"file_path":"README.md","old_string":"Phase 1","new_string":"Phase 2"}`)}})
		tt := ui.NewTester(a.view, 1080, 760)
		tt.SetTitleBar(ui.TitleBar{Height: 52, Left: 78})
		tt.SetDark(dark)
		tt.Frame()
		suffix := "light"
		if dark {
			suffix = "dark"
		}
		for _, screen := range []string{"approval", "models"} {
			if screen == "models" {
				a.openOptions(b)
				tt.Frame()
			}
			tt.Advance(time.Second)
			tt.Frame()
			f, err := os.Create(filepath.Join(dir, "phase2-"+screen+"-"+suffix+".png"))
			if err != nil {
				t.Fatal(err)
			}
			err = png.Encode(f, tt.Image())
			closeErr := f.Close()
			if err != nil {
				t.Fatal(err)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
		}
	}
}
