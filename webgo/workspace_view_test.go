package webgo

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bqbot/internal/harness"
	"bqbot/internal/store"
	"github.com/egoist/mygo/ui"
)

func phase3Bots(t *testing.T) (*app, chan func(), store.Bot, store.Bot) {
	t.Helper()
	a, posts := testApp(t)
	ada := store.Bot{ID: store.NewID(), Name: "Ada", Engine: "echo", Avatar: "AL", Cursor: "native-session-ada"}
	bo := store.Bot{ID: store.NewID(), Name: "Bo", Engine: "echo", Avatar: "B"}
	for _, b := range []store.Bot{ada, bo} {
		if err := a.api.SaveBot(b); err != nil {
			t.Fatal(err)
		}
	}
	a.bots = []store.Bot{ada, bo}
	a.selectBot(ada.ID)
	return a, posts, ada, bo
}
func clickUI(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	if err := tt.Click(label); err != nil {
		t.Fatal(err)
	}
}
func typeUI(t *testing.T, tt *ui.Tester, label, value string) {
	t.Helper()
	clickUI(t, tt, label)
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type(value)
}
func botMenuUI(t *testing.T, tt *ui.Tester, bot, item string) {
	t.Helper()
	if err := tt.RightClick(bot); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(item); err != nil {
		t.Fatal(err)
	}
}
func navigationUI(t *testing.T, tt *ui.Tester, item string) {
	t.Helper()
	clickUI(t, tt, "Navigation options")
	if err := tt.ChooseMenuItem(item); err != nil {
		t.Fatal(err)
	}
}

func TestPhase3BotManagement(t *testing.T) {
	a, _, ada, _ := phase3Bots(t)
	tt := ui.NewTester(a.view, 1100, 850)
	botMenuUI(t, tt, "Ada", "Pin")
	botMenuUI(t, tt, "Ada", "Mark unread")
	b, _ := a.bot(ada.ID)
	if !b.Pinned || !b.Unread {
		t.Fatalf("flags %+v", b)
	}
	botMenuUI(t, tt, "Ada", "Copy native session ID")
	if tt.Clipboard() != ada.Cursor {
		t.Fatalf("clipboard %q", tt.Clipboard())
	}
	botMenuUI(t, tt, "Ada", "Edit…")
	typeUI(t, tt, "Avatar", "AX")
	clickUI(t, tt, "Save")
	b, _ = a.bot(ada.ID)
	if b.Avatar != "AX" {
		t.Fatalf("avatar %+v", b)
	}
	botMenuUI(t, tt, "Ada", "Duplicate")
	bots, err := a.api.Bots()
	if err != nil {
		t.Fatal(err)
	}
	if len(bots) != 3 {
		t.Fatalf("bots %+v", bots)
	}
	clone := a.selected
	for _, b := range bots {
		if b.ID == clone && (b.Cursor != "" || b.Avatar != "AX") {
			t.Fatalf("clone %+v", b)
		}
	}
	botMenuUI(t, tt, "Ada", "Hide")
	b, _ = a.bot(ada.ID)
	if !b.Hidden {
		t.Fatal("not hidden")
	}
	navigationUI(t, tt, "Show hidden bots")
	botMenuUI(t, tt, "Ada", "Restore")
	b, _ = a.bot(ada.ID)
	if b.Hidden {
		t.Fatal("not restored")
	}
	clickUI(t, tt, "Ada")
	if b, _ := a.bot(ada.ID); b.Unread {
		t.Fatal("select did not mark read")
	}
}

func TestPhase3ThreadSendMemorySearchAndCompact(t *testing.T) {
	a, posts, ada, _ := phase3Bots(t)
	tt := ui.NewTester(a.view, 1100, 850)
	clickUI(t, tt, "New thread")
	typeUI(t, tt, "Thread title", "Research")
	clickUI(t, tt, "Create thread")
	id := a.conversationID()
	if id == ada.ID {
		t.Fatal("still in main conversation")
	}
	typeUI(t, tt, "Message", "unique research phrase")
	clickUI(t, tt, "Send")
	pumpUntil(t, tt, posts, func() bool { return !a.thread(id).busy && len(a.thread(id).msgs) >= 2 })
	if !tt.HasText("Echo: unique research phrase") {
		t.Fatalf("texts %q", tt.Texts())
	}
	main, err := a.api.Transcript(ada.ID)
	if err != nil || len(main) != 0 {
		t.Fatalf("main leaked: %+v %v", main, err)
	}
	clickUI(t, tt, "Memory")
	typeUI(t, tt, "Memory text", "Prefer concise research notes.")
	clickUI(t, tt, "Save memory")
	memories, err := a.api.Memories(id)
	if err != nil || len(memories) != 1 {
		t.Fatalf("memories %+v %v", memories, err)
	}
	clickUI(t, tt, "Edit memory "+memories[0].ID)
	typeUI(t, tt, "Memory text", "Prefer detailed research notes.")
	clickUI(t, tt, "Save memory")
	if !tt.HasText("Prefer detailed research notes.") {
		t.Fatal(tt.Texts())
	}
	clickUI(t, tt, "Delete memory "+memories[0].ID)
	if !tt.HasText("No memories yet.") {
		t.Fatal(tt.Texts())
	}
	clickUI(t, tt, "Close")
	// More than twelve messages makes compaction observable without extra turns.
	for i := 0; i < 16; i++ {
		if err := testStore(a).AppendThread(id, store.Message{ID: fmt.Sprintf("older-%02d", i), Role: "user", Text: fmt.Sprintf("Background note %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	clickUI(t, tt, "Compact context")
	clickUI(t, tt, "Compact")
	meta, ok := a.threadMeta(id)
	if !ok || meta.Summary == "" || meta.Cursor != "" {
		t.Fatalf("compaction %+v", meta)
	}
	clickUI(t, tt, "Ada")
	clickUI(t, tt, "Search messages")
	typeUI(t, tt, "Search transcript text", "unique research phrase")
	clickUI(t, tt, "Search")
	if len(a.searchResults) < 2 {
		t.Fatalf("search %+v %s", a.searchResults, a.searchError)
	}
	clickUI(t, tt, "Search result 1")
	if a.conversationID() != id || a.highlightID == "" {
		t.Fatalf("navigation %s %s", a.conversationID(), a.highlightID)
	}
	clickUI(t, tt, "Clear highlight")
	clickUI(t, tt, "Search messages")
	clickUI(t, tt, "Clear results")
	if len(a.searchResults) != 0 || a.searchQuery != "" {
		t.Fatal("search not cleared")
	}
	clickUI(t, tt, "Close")
	clickUI(t, tt, "Close thread")
	if a.conversationID() != ada.ID {
		t.Fatal("did not return to main")
	}
	navigationUI(t, tt, "Show closed threads")
	clickUI(t, tt, "Thread: Research")
	if !tt.HasText("This thread is closed. Create a new thread to continue.") {
		t.Fatal(tt.Texts())
	}
}

func TestPhase3ChannelInteractions(t *testing.T) {
	a, posts, ada, bo := phase3Bots(t)
	tt := ui.NewTester(a.view, 1100, 900)
	clickUI(t, tt, "New channel")
	typeUI(t, tt, "Channel name", "Planning")
	clickUI(t, tt, "Member: Ada")
	clickUI(t, tt, "Member: Bo")
	clickUI(t, tt, "Save channel")
	roomID := a.selectedRoom
	if roomID == "" || len(a.rooms) != 1 {
		t.Fatalf("room %s error %s", roomID, a.roomError)
	}
	clickUI(t, tt, "Mention member")
	if err := tt.ChooseMenuItem("Bo"); err != nil {
		t.Fatal(err)
	}
	rt := a.roomConversation(roomID)
	if !strings.Contains(rt.draft, "@"+bo.ID) {
		t.Fatalf("mention %q", rt.draft)
	}
	clickUI(t, tt, "Message")
	tt.Type("please review")
	clickUI(t, tt, "Send")
	pumpUntil(t, tt, posts, func() bool { return !rt.busy && len(rt.msgs) >= 2 })
	if len(a.thread(ada.ID).msgs) != 0 || len(a.thread(bo.ID).msgs) != 0 {
		t.Fatal("room leaked to main")
	}
	found := false
	for _, m := range rt.msgs {
		if m.Role == "assistant" && m.BotID == bo.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("speaker %+v", rt.msgs)
	}
	clickUI(t, tt, "Edit channel")
	typeUI(t, tt, "Channel name", "Planning revised")
	clickUI(t, tt, "Save channel")
	if !tt.HasText("# Planning revised") {
		t.Fatal(tt.Texts())
	}
	clickUI(t, tt, "Memory")
	typeUI(t, tt, "Memory text", "Ship on Friday.")
	clickUI(t, tt, "Save memory")
	memories, err := a.api.Memories(roomID)
	if err != nil || len(memories) != 1 {
		t.Fatalf("room memory %+v %v", memories, err)
	}
	clickUI(t, tt, "Close")
	clickUI(t, tt, "Delete channel")
	clickUI(t, tt, "Delete channel permanently")
	if len(a.rooms) != 0 || a.selectedRoom != "" {
		t.Fatal("channel not deleted")
	}
	navigationUI(t, tt, "Decision model…")
	typeUI(t, tt, "Decision base URL", "http://127.0.0.1:9999/v1")
	typeUI(t, tt, "Decision model ID", "local-decision")
	typeUI(t, tt, "Decision API key environment variable", "BQBOT_TEST_DECISION_KEY")
	clickUI(t, tt, "Save decision model")
	decision, err := a.api.DecisionSettings()
	if err != nil || decision.Model != "local-decision" || decision.APIKeyEnv != "BQBOT_TEST_DECISION_KEY" {
		t.Fatalf("decision %+v %v", decision, err)
	}
}

func TestPhase3HandoffAndEventIsolation(t *testing.T) {
	a, _, ada, bo := phase3Bots(t)
	if err := testStore(a).AppendHandoff(bo.ID, ada.ID, store.Message{ID: "legacy-question", Role: "user", Text: "Legacy handoff", From: ada.Name}); err != nil {
		t.Fatal(err)
	}
	a.refreshThreads(bo.ID)
	var handoff store.Thread
	for _, th := range a.botThreads[bo.ID] {
		if th.AskerID == ada.ID {
			handoff = th
		}
	}
	if handoff.ID == "" {
		t.Fatal("missing migrated handoff")
	}
	tt := ui.NewTester(a.view, 1100, 850)
	clickUI(t, tt, "Thread: "+a.threadTitleFor(handoff))
	if !tt.HasText("Legacy handoff") {
		t.Fatal(tt.Texts())
	}
	named, err := a.api.CreateThread(bo.ID, "Separate")
	if err != nil {
		t.Fatal(err)
	}
	a.refreshThreads(bo.ID)
	a.fold(harness.Update{BotID: bo.ID, ThreadID: named.ID, Sequence: 10, Busy: true, Reply: &store.Message{ID: "named-live", Role: "assistant", Text: "Named reply"}})
	a.fold(harness.Update{BotID: bo.ID, ThreadID: handoff.ID, Sequence: 11, Busy: true, Reply: &store.Message{ID: "handoff-live", Role: "assistant", Text: "Handoff reply"}})
	a.fold(harness.Update{BotID: bo.ID, ThreadID: named.ID, Sequence: 9, Done: true})
	if !a.thread(named.ID).busy || a.thread(named.ID).live.Text != "Named reply" || a.thread(handoff.ID).live.Text != "Handoff reply" || a.thread(bo.ID).live != nil {
		t.Fatal("cross-thread or stale event corrupted state")
	}
	room := store.Room{ID: store.NewID(), Name: "Events", Members: []string{ada.ID, bo.ID}, LeadID: ada.ID, ReplyMode: "lead"}
	if err := a.api.SaveRoom(room); err != nil {
		t.Fatal(err)
	}
	a.refreshNavigation()
	a.fold(harness.Update{BotID: ada.ID, RoomID: room.ID, ThreadID: "room-ada", Sequence: 12, Busy: true, Reply: &store.Message{ID: "ra", Role: "assistant", Text: "Ada room reply"}})
	a.fold(harness.Update{BotID: bo.ID, RoomID: room.ID, ThreadID: "room-bo", Sequence: 13, Busy: true, Reply: &store.Message{ID: "rb", Role: "assistant", Text: "Bo room reply"}})
	rt := a.roomConversation(room.ID)
	if len(rt.lives) != 2 || a.thread(ada.ID).live != nil {
		t.Fatal("room streams not isolated")
	}
	tt.Frame()
	clickUI(t, tt, "Channel: Events")
	tt.Advance(time.Second)
	tt.Frame()
	if !tt.HasText("Ada room reply") || !tt.HasText("Bo room reply") {
		t.Fatal(tt.Texts())
	}
}

func TestPhase3RoomSnapshotAndConcurrentCompletion(t *testing.T) {
	a, _, ada, bo := phase3Bots(t)
	room := store.Room{ID: store.NewID(), Name: "Concurrent", Members: []string{ada.ID, bo.ID}, LeadID: ada.ID, ReplyMode: "lead"}
	if err := a.api.SaveRoom(room); err != nil {
		t.Fatal(err)
	}
	a.refreshNavigation()
	user := store.Message{ID: "question", Role: "user", Text: "Both reply"}
	first := store.Message{ID: "first-live", Role: "assistant", BotID: ada.ID, Text: "First answer"}
	second := store.Message{ID: "second-live", Role: "assistant", BotID: bo.ID, Text: "Second answer"}
	messages := []store.Message{user, first, second}
	a.fold(harness.Update{RoomID: room.ID, Sync: true, Sequence: 20, Messages: messages, Busy: true})
	a.fold(harness.Update{RoomID: room.ID, BotID: ada.ID, ThreadID: "speaker-a", Sync: true, Sequence: 20, Messages: messages, Reply: &first, Busy: true})
	a.fold(harness.Update{RoomID: room.ID, BotID: bo.ID, ThreadID: "speaker-b", Sync: true, Sequence: 20, Messages: messages, Reply: &second, Busy: true})
	rt := a.roomConversation(room.ID)
	if len(rt.msgs) != 1 || len(rt.lives) != 2 || !rt.busy {
		t.Fatalf("snapshot duplicated live messages: committed=%d live=%d busy=%v", len(rt.msgs), len(rt.lives), rt.busy)
	}
	a.fold(harness.Update{RoomID: room.ID, BotID: ada.ID, ThreadID: "speaker-a", Sequence: 21, Reply: &first, Done: true, Busy: true})
	a.fold(harness.Update{RoomID: room.ID, BotID: bo.ID, ThreadID: "speaker-b", Sequence: 22, Reply: &second, Done: true, Busy: false})
	if rt.busy || len(rt.lives) != 0 || len(rt.msgs) != 3 {
		t.Fatalf("completed channel stayed busy or lost replies: %+v", rt)
	}
	a.selectRoom(room.ID)
	tt := ui.NewTester(a.view, 1100, 850)
	if !tt.HasText("First answer") || !tt.HasText("Second answer") {
		t.Fatal(tt.Texts())
	}
}

// BQ_FRAMES writes real native layouts, using local fixtures and no providers.
func TestPhase3Frames(t *testing.T) {
	dir := os.Getenv("BQ_FRAMES")
	if dir == "" {
		t.Skip("BQ_FRAMES is not set")
	}
	for _, dark := range []bool{false, true} {
		a, _, ada, bo := phase3Bots(t)
		ada.Pinned = true
		a.persistBot(ada)
		named := store.Thread{ID: store.NewID(), BotID: ada.ID, Title: "Release research"}
		if err := testStore(a).SaveThread(named); err != nil {
			t.Fatal(err)
		}
		room := store.Room{ID: store.NewID(), Name: "Product studio", Members: []string{ada.ID, bo.ID}, LeadID: ada.ID, ReplyMode: "auto", Instructions: "Turn research into a clear release plan."}
		if err := testStore(a).SaveRoom(room); err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		for _, m := range []store.Message{
			{ID: "room-user", Role: "user", Text: "What should we ship first?", At: now},
			{ID: "room-a", Role: "assistant", BotID: ada.ID, Text: "Start with **reliable conversations**.\n\n- Keep project threads separate\n- Share decisions in this channel\n- Save useful context as memory", At: now},
			{ID: "room-b", Role: "assistant", BotID: bo.ID, Text: "I’ll review the release checklist and report back here.", At: now},
		} {
			if err := testStore(a).AppendRoom(room.ID, m); err != nil {
				t.Fatal(err)
			}
		}
		if err := testStore(a).AppendThread(named.ID, store.Message{ID: "research", Role: "assistant", BotID: ada.ID, Text: "The release checklist is ready for review.", At: now}); err != nil {
			t.Fatal(err)
		}
		a.rooms = []store.Room{room}
		a.botThreads = map[string][]store.Thread{ada.ID: {{ID: ada.ID, BotID: ada.ID}, named}, bo.ID: {{ID: bo.ID, BotID: bo.ID}}}
		roomMessages, err := testStore(a).RoomTranscript(room.ID)
		if err != nil {
			t.Fatal(err)
		}
		threadMessages, err := testStore(a).ThreadTranscript(named.ID)
		if err != nil {
			t.Fatal(err)
		}
		a.threads[roomKey(room.ID)] = &thread{id: roomKey(room.ID), roomID: room.ID, loaded: true, msgs: roomMessages, list: ui.ListState{FollowEnd: true}}
		a.threads[named.ID] = &thread{id: named.ID, botID: ada.ID, loaded: true, msgs: threadMessages, list: ui.ListState{FollowEnd: true}}
		a.selectRoom(room.ID)
		tt := ui.NewTester(a.view, 1120, 820)
		tt.SetTitleBar(ui.TitleBar{Height: 52, Left: 78})
		tt.SetDark(dark)
		suffix := "light"
		if dark {
			suffix = "dark"
		}
		for _, screen := range []string{"channel", "thread", "channel-settings", "memory", "search", "decision"} {
			a.roomFormOpen, a.memoriesOpen, a.searchOpen, a.decisionOpen = false, false, false, false
			switch screen {
			case "thread":
				a.selectThread(named)
			case "channel-settings":
				a.openRoomForm(room)
			case "memory":
				a.selectRoom(room.ID)
				a.memoryScope, a.memoriesOpen = room.ID, true
				a.memories = []store.Memory{{ID: "frame-memory", Text: "Release decisions belong in the channel."}}
				a.memoryEdit.Text = "Prefer a small, well-tested release."
			case "search":
				a.searchQuery = "release"
				a.searchResults, err = testStore(a).Search(a.searchQuery)
				if err != nil {
					t.Fatal(err)
				}
				a.searchRan = true
				a.searchOpen = true
			case "decision":
				a.decision = store.DecisionSettings{BaseURL: "https://openrouter.ai/api/v1", APIKeyEnv: "OPENROUTER_API_KEY", Model: "provider/decision-model"}
				a.decisionOpen = true
			}
			tt.Frame()
			tt.Advance(time.Second)
			tt.Frame()
			f, err := os.Create(filepath.Join(dir, "phase3-"+screen+"-"+suffix+".png"))
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
