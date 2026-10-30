package webgo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bqbot/internal/driver"
	"bqbot/internal/harness"
	"bqbot/internal/store"
	"github.com/egoist/mygo/ui"
)

type approvalEngine struct{}

func (*approvalEngine) Name() string   { return "approval" }
func (*approvalEngine) Binary() string { return "" }
func (*approvalEngine) Run(ctx context.Context, in driver.TurnInput, emit func(driver.Event)) error {
	req := driver.ApprovalRequest{Kind: "tool", Tool: "Bash", Input: json.RawMessage(`{"command":"echo hello"}`)}
	if in.Text == "question" {
		req = driver.ApprovalRequest{Kind: "question", Question: "Which branch?", Options: []string{"main", "develop"}}
	}
	decision, err := in.Approve(ctx, req)
	if err != nil {
		return err
	}
	emit(driver.Event{Kind: driver.EvText, Text: decision.Outcome + " " + decision.Answer})
	return nil
}

func pumpUntil(t *testing.T, tt *ui.Tester, posts chan func(), done func() bool) {
	t.Helper()
	tt.Frame()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for !done() {
		select {
		case fn := <-posts:
			fn()
			tt.Frame()
		case <-deadline.C:
			t.Fatalf("UI condition not reached: %q", tt.Texts())
		}
	}
}

func TestApprovalCardsOverHTTP(t *testing.T) {
	for _, kind := range []string{"allow", "deny", "answer"} {
		t.Run(kind, func(t *testing.T) {
			st, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			b := store.Bot{ID: store.NewID(), Name: "Ada", Engine: "approval"}
			if err := st.SaveBot(b); err != nil {
				t.Fatal(err)
			}
			h := harness.New(st, &approvalEngine{})
			posts := make(chan func(), 1024)
			a, err := newTestApp(t, st, h, func(fn func()) { posts <- fn })
			if err != nil {
				t.Fatal(err)
			}
			tt := ui.NewTester(a.view, 1000, 760)
			prompt := "tool"
			if kind == "answer" {
				prompt = "question"
			}
			if !a.send(b, a.thread(b.ID), prompt) {
				t.Fatal(a.status)
			}
			pumpUntil(t, tt, posts, func() bool { return len(a.thread(b.ID).approvals) > 0 })
			switch kind {
			case "allow":
				err = tt.Click("Allow")
			case "deny":
				err = tt.Click("Deny")
			case "answer":
				if err = tt.Click("Your answer"); err != nil {
					t.Fatal(err)
				}
				tt.Type("main")
				err = tt.Click("Answer")
			}
			if err != nil {
				t.Fatal(err)
			}
			pumpUntil(t, tt, posts, func() bool { return !a.thread(b.ID).busy })
			messages, err := a.api.Transcript(b.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(messages) != 2 || messages[1].Role != "assistant" {
				t.Fatalf("messages %+v", messages)
			}
			want := kind
			if kind == "answer" {
				want = "answer main"
			}
			if messages[1].Text != want && messages[1].Text != want+" " {
				t.Fatalf("reply %q, want %q", messages[1].Text, want)
			}
			if len(a.thread(b.ID).approvals) != 0 {
				t.Fatal("approval remains after completion")
			}
		})
	}
}

func TestQueueAndStopInChat(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := store.Bot{ID: store.NewID(), Name: "Ada", Engine: "echo"}
	st.SaveBot(b)
	h := harness.New(st, &driver.Echo{Delay: 70 * time.Millisecond})
	posts := make(chan func(), 1024)
	a, err := newTestApp(t, st, h, func(fn func()) { posts <- fn })
	if err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(a.view, 1000, 760)
	thread := a.thread(b.ID)
	if !a.send(b, thread, "first turn") {
		t.Fatal(a.status)
	}
	if !a.send(b, thread, "second turn") {
		t.Fatal(a.status)
	}
	pumpUntil(t, tt, posts, func() bool {
		for _, m := range thread.msgs {
			if m.Role == "assistant" && m.Text == "Echo: second turn" {
				return !thread.busy
			}
		}
		return false
	})
	if len(thread.msgs) != 4 || thread.msgs[0].Text != "first turn" || thread.msgs[1].Text != "Echo: first turn" || thread.msgs[2].Text != "second turn" {
		t.Fatalf("chat order %+v", thread.msgs)
	}
	if !a.send(b, thread, "stop before completing this long response") {
		t.Fatal(a.status)
	}
	tt.Frame()
	if err := tt.Click("Stop"); err != nil {
		t.Fatal(err)
	}
	pumpUntil(t, tt, posts, func() bool { return !thread.busy })
}

func TestModelPermissionsAndSettings(t *testing.T) {
	a, _ := testApp(t)
	b := store.Bot{ID: store.NewID(), Name: "Ada", Engine: "echo", Cursor: "old"}
	testStore(a).SaveBot(b)
	a.bots = []store.Bot{b}
	a.selected = b.ID
	tt := ui.NewTester(a.view, 1000, 760)
	if err := tt.Click("Model & permissions"); err != nil {
		t.Fatal(err)
	}
	a.optionBot.Model = "new-model"
	a.optionBot.Effort = "high"
	a.optionBot.ApprovalMode = "auto-edit"
	tt.Frame()
	if err := tt.Click("Apply"); err != nil {
		t.Fatal(err)
	}
	bots, err := a.api.Bots()
	if err != nil {
		t.Fatal(err)
	}
	if bots[0].Model != "new-model" || bots[0].Cursor != "" || bots[0].ApprovalMode != "auto-edit" {
		t.Fatalf("bot %+v, status %s", bots[0], a.status)
	}
	if err := tt.Click("Engine settings"); err != nil {
		t.Fatal(err)
	}
	a.settingsPath = "/custom/bin/claude"
	if err := tt.Click("Save settings"); err != nil {
		t.Fatal(err)
	}
	settings, err := a.api.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if settings["claude"].Path != "/custom/bin/claude" {
		t.Fatalf("settings %+v", settings)
	}
}

func TestImageAttachmentOverHTTP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "picture.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	attachment, err := readAttachment(path)
	if err != nil {
		t.Fatal(err)
	}
	a, posts := testApp(t)
	b := store.Bot{ID: store.NewID(), Name: "Ada", Engine: "echo"}
	if err := testStore(a).SaveBot(b); err != nil {
		t.Fatal(err)
	}
	a.bots, a.selected = []store.Bot{b}, b.ID
	a.thread(b.ID).attachments = []driver.Attachment{attachment}
	tt := ui.NewTester(a.view, 1000, 760)
	if err := tt.Click("Send"); err != nil {
		t.Fatal(err)
	}
	pumpUntil(t, tt, posts, func() bool { return !a.thread(b.ID).busy })
	messages, err := a.api.Transcript(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || len(messages[0].Attachments) != 1 {
		t.Fatalf("messages %+v", messages)
	}
	decoded, err := base64.StdEncoding.DecodeString(messages[0].Attachments[0].Data)
	if err != nil || len(decoded) == 0 || len(a.thread(b.ID).attachments) != 0 {
		t.Fatal("image did not survive HTTP and transcript")
	}
}

func TestImageAttachmentValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-image.png")
	if err := os.WriteFile(path, []byte("not an image"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readAttachment(path); err == nil {
		t.Fatal("non-image accepted")
	}
}
