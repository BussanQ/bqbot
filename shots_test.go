package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"bqbot/internal/store"
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
		a.st.SaveBot(ada)
		a.st.SaveBot(bo)
		now := time.Now()
		a.st.Append(ada.ID, store.Message{ID: "u1", Role: "user", Text: "List the files and ask Bo to check go.mod", At: now})
		a.st.Append(ada.ID, store.Message{ID: "r1", Role: "assistant", At: now,
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
