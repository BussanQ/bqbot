package main

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"bqbot/internal/harness"
	"bqbot/internal/store"
	"github.com/egoist/mygo/ui"
)

// phase3State separates navigation from the legacy selected bot. Main chats
// retain their bot ID as the cache key; room keys cannot collide with threads.
type phase3State struct {
	selectedThread, selectedRoom        string
	botThreads                          map[string][]store.Thread
	rooms                               []store.Room
	showHidden, showClosed              bool
	newAvatar                           string
	threadFormOpen                      bool
	threadBot, threadTitle, threadError string
	roomFormOpen, deletingRoom          bool
	roomForm                            store.Room
	roomError                           string
	decisionOpen                        bool
	decision                            store.DecisionSettings
	decisionError                       string
	memoriesOpen                        bool
	memoryScope                         string
	memories                            []store.Memory
	memoryEdit                          store.Memory
	memoryError                         string
	searchOpen                          bool
	searchQuery, searchError            string
	searchResults                       []store.SearchResult
	searchRan                           bool
	highlightID                         string
	compactOpen                         bool
	compactScope, compactError          string
}

func roomKey(id string) string { return "room:" + id }

func (a *app) refreshNavigation() {
	if a.botThreads == nil {
		a.botThreads = map[string][]store.Thread{}
	}
	if rooms, err := a.api.Rooms(); err == nil {
		a.rooms = rooms
	} else {
		a.status = err.Error()
	}
	for _, b := range a.bots {
		a.refreshThreads(b.ID)
	}
}

func (a *app) refreshThreads(id string) {
	if a.botThreads == nil {
		a.botThreads = map[string][]store.Thread{}
	}
	rows, err := a.api.Threads(id)
	if err != nil {
		a.status = err.Error()
		return
	}
	a.botThreads[id] = rows
}

func (a *app) threadMeta(id string) (store.Thread, bool) {
	for _, rows := range a.botThreads {
		for _, row := range rows {
			if row.ID == id {
				return row, true
			}
		}
	}
	return store.Thread{}, false
}
func (a *app) room(id string) (store.Room, bool) {
	for _, r := range a.rooms {
		if r.ID == id {
			return r, true
		}
	}
	return store.Room{}, false
}
func (a *app) conversationID() string {
	if a.selectedRoom != "" {
		return roomKey(a.selectedRoom)
	}
	if meta, ok := a.threadMeta(a.selectedThread); ok && meta.BotID == a.selected {
		return meta.ID
	}
	return a.selected
}
func (a *app) conversationTitle(b store.Bot) string {
	if meta, ok := a.threadMeta(a.conversationID()); ok && meta.ID != b.ID {
		return b.Name + " · " + a.threadTitleFor(meta)
	}
	return b.Name
}
func (a *app) threadTitleFor(t store.Thread) string {
	if t.Title != "" {
		return t.Title
	}
	if t.AskerID != "" {
		if b, ok := a.bot(t.AskerID); ok {
			return "From " + b.Name
		}
		return "Handoff"
	}
	if t.ID == t.BotID {
		return "Main conversation"
	}
	return "Conversation"
}
func (a *app) selectFirstVisible() {
	a.selected, a.selectedThread, a.selectedRoom = "", "", ""
	for _, b := range a.bots {
		if !b.Hidden {
			a.selected = b.ID
			return
		}
	}
}
func (a *app) selectThread(t store.Thread) {
	a.selected, a.selectedThread, a.selectedRoom = t.BotID, t.ID, ""
	a.highlightID = ""
	a.thread(t.ID).unread = 0
}
func (a *app) selectRoom(id string) {
	a.selectedRoom, a.selectedThread = id, ""
	a.highlightID = ""
	a.roomConversation(id).unread = 0
}
func (a *app) persistBot(b store.Bot) bool {
	if err := a.api.SaveBot(b); err != nil {
		a.status = err.Error()
		return false
	}
	if i := slices.IndexFunc(a.bots, func(v store.Bot) bool { return v.ID == b.ID }); i >= 0 {
		a.bots[i] = b
	}
	return true
}

func (a *app) avatar(c *ui.Context, b store.Bot, size float32) {
	if b.Avatar == "" {
		ui.Avatar(c, b.Name, nil).FontSize(size)
		return
	}
	ui.Box(c).Size(size*2.2, size*2.2).Radius(999).Center().Background(a.pal.accent.Alpha(0.12)).Children(func() {
		ui.Text(c, b.Avatar).FontSize(size).TextColor(a.pal.text).SingleLine()
	})
}

func (a *app) botMenu(c *ui.Context, m *ui.Menu, b store.Bot) {
	pin := "Pin"
	if b.Pinned {
		pin = "Unpin"
	}
	if m.Item(pin).Chosen() {
		b.Pinned = !b.Pinned
		a.persistBot(b)
	}
	unread := "Mark unread"
	if b.Unread {
		unread = "Mark read"
	}
	if m.Item(unread).Chosen() {
		b.Unread = !b.Unread
		a.persistBot(b)
	}
	if m.Item("Edit…").Chosen() {
		a.openEdit(b)
	}
	if m.Item("Duplicate").Chosen() {
		copy, err := a.api.CloneBot(b.ID)
		if err != nil {
			a.status = err.Error()
		} else {
			a.bots = append(a.bots, copy)
			a.selectBot(copy.ID)
		}
	}
	if m.Item("Copy native session ID").Disabled(b.Cursor == "").Chosen() {
		c.WriteClipboard(b.Cursor)
		c.Toast("Session ID copied")
	}
	m.Separator()
	if m.Item("New thread…").Chosen() {
		a.openThreadForm(b.ID)
	}
	if m.Item("Handoff sessions…").Chosen() {
		a.openSessions(b)
	}
	if m.Item("Open working folder").Chosen() {
		a.openFolder(c, b)
	}
	m.Separator()
	hidden := "Hide"
	if b.Hidden {
		hidden = "Restore"
	}
	if m.Item(hidden).Chosen() {
		b.Hidden = !b.Hidden
		if a.persistBot(b) && b.Hidden && a.selected == b.ID && a.selectedRoom == "" {
			a.selectFirstVisible()
		}
	}
	if m.Item("Delete…").Chosen() {
		a.selectBot(b.ID)
		a.deleting = true
	}
}

func (a *app) navigation(c *ui.Context) {
	p := a.pal
	ui.Row(c).Gap(6).Children(func() {
		if ui.Button(c, "Search messages").FontSize(11).Grow(1).Clicked() {
			a.searchOpen = true
		}
		ui.Button(c, "•••").Label("Navigation options").Menu(func(m *ui.Menu) {
			if m.Item("Show hidden bots").Checked(a.showHidden).Chosen() {
				a.showHidden = !a.showHidden
			}
			if m.Item("Show closed threads").Checked(a.showClosed).Chosen() {
				a.showClosed = !a.showClosed
			}
			if m.Item("Refresh conversations").Chosen() {
				if bots, err := a.api.Bots(); err == nil {
					a.bots = bots
				} else {
					a.status = err.Error()
				}
				a.refreshNavigation()
			}
			if m.Item("Decision model…").Chosen() {
				a.openDecision()
			}
		})
	})
	ui.SearchField(c, &a.query).Label("Search bots").Placeholder("Filter bots and channels")
	ui.Scroll(c.Key("navigation")).Grow(1).Gap(3).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Padding(6, 8).Children(func() {
			ui.Text(c, "Bots").FontSize(11.5).FontWeight(600).TextColor(p.faint).Grow(1)
			if a.showHidden {
				ui.Text(c, "Including hidden").FontSize(10).TextColor(p.faint)
			}
		})
		bots := slices.Clone(a.bots)
		slices.SortStableFunc(bots, func(x, y store.Bot) int {
			if x.Pinned == y.Pinned {
				return 0
			}
			if x.Pinned {
				return -1
			}
			return 1
		})
		q := strings.ToLower(strings.TrimSpace(a.query))
		for _, b := range bots {
			if b.Hidden && !a.showHidden {
				continue
			}
			if q != "" && !strings.Contains(strings.ToLower(b.Name), q) {
				continue
			}
			if _, ok := a.botThreads[b.ID]; !ok {
				a.refreshThreads(b.ID)
			}
			a.sidebarItem(c, b)
			if b.Pinned || b.Hidden {
				label := "Pinned"
				if b.Hidden {
					label = "Hidden · right-click to restore"
				}
				ui.Text(c, label).FontSize(10).TextColor(p.faint).PaddingStart(48)
			}
			for _, meta := range a.botThreads[b.ID] {
				if meta.ID == b.ID || meta.RoomID != "" || (meta.Closed && !a.showClosed) {
					continue
				}
				title := a.threadTitleFor(meta)
				if meta.Closed {
					title += " · closed"
				}
				row := ui.ButtonBase(c.Key("nav-thread:"+meta.ID)).Label("Thread: "+a.threadTitleFor(meta)).Padding(5, 8, 5, 28).Radius(7).Gap(6).AlignItems(ui.Center).Selected(a.conversationID() == meta.ID)
				if a.conversationID() == meta.ID {
					row.Background(p.press)
				}
				row.Children(func() {
					ui.Text(c, "↳").FontSize(12).TextColor(p.faint)
					ui.Text(c, title).FontSize(12).SingleLine().Grow(1)
					if t := a.threads[meta.ID]; t != nil {
						if t.busy {
							ui.Spinner(c)
						} else if t.unread > 0 {
							ui.Badge(c, fmt.Sprint(t.unread))
						}
					}
				})
				if row.Clicked() {
					a.selectThread(meta)
				}
				row.ContextMenu(func(m *ui.Menu) {
					if m.Item("Copy native session ID").Disabled(meta.Cursor == "").Chosen() {
						c.WriteClipboard(meta.Cursor)
					}
					if m.Item("Close thread").Disabled(meta.Closed).Chosen() {
						a.closeThread(meta)
					}
				})
			}
		}
		if len(a.bots) == 0 {
			ui.Text(c, "No bots yet. Press ⌘N.").FontSize(12).Padding(8).TextColor(p.muted)
		}
		ui.Row(c).AlignItems(ui.Center).Padding(12, 8, 4, 8).Children(func() {
			ui.Text(c, "Channels").FontSize(11.5).FontWeight(600).TextColor(p.faint).Grow(1)
			if a.iconButton(c, plusIcon, "New channel", "Create a channel").Clicked() {
				a.openRoomForm(store.Room{})
			}
		})
		for _, room := range a.rooms {
			if q != "" && !strings.Contains(strings.ToLower(room.Name), q) {
				continue
			}
			row := ui.ButtonBase(c.Key("nav-room:"+room.ID)).Label("Channel: "+room.Name).Padding(9, 10).Radius(8).Gap(8).AlignItems(ui.Center)
			if a.selectedRoom == room.ID {
				row.Background(p.press)
			}
			row.Children(func() {
				ui.Text(c, "#").FontSize(16).TextColor(p.accent)
				ui.Text(c, room.Name).FontSize(13).SingleLine().Grow(1)
				if t := a.threads[roomKey(room.ID)]; t != nil {
					if t.busy {
						ui.Spinner(c)
					} else if t.unread > 0 {
						ui.Badge(c, fmt.Sprint(t.unread))
					}
				}
			})
			if row.Clicked() {
				a.selectRoom(room.ID)
			}
			row.ContextMenu(func(m *ui.Menu) {
				if m.Item("Edit channel…").Chosen() {
					a.openRoomForm(room)
				}
				if m.Item("Delete channel…").Chosen() {
					a.selectRoom(room.ID)
					a.deletingRoom = true
				}
			})
		}
	})
}

func (a *app) openThreadForm(botID string) {
	a.threadBot, a.threadTitle, a.threadError, a.threadFormOpen = botID, "", "", true
}
func (a *app) closeThread(meta store.Thread) {
	if err := a.api.CloseThread(meta.ID); err != nil {
		a.status = err.Error()
		return
	}
	a.refreshThreads(meta.BotID)
	if a.conversationID() == meta.ID {
		a.selectBot(meta.BotID)
	}
}
func (a *app) conversationActions(c *ui.Context, t *thread) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(7, 24).BorderWidth(0, 0, 1, 0).BorderColor(a.pal.hairline).Children(func() {
		if t.roomID == "" {
			if ui.Button(c, "New thread").FontSize(11).Clicked() {
				a.openThreadForm(a.selected)
			}
		}
		if ui.Button(c, "Memory").FontSize(11).Clicked() {
			a.openMemories(t)
		}
		if t.roomID == "" {
			if ui.Button(c, "Compact context").FontSize(11).Disabled(t.busy).Clicked() {
				a.compactScope, a.compactError, a.compactOpen = a.conversationID(), "", true
			}
			if meta, ok := a.threadMeta(a.conversationID()); ok && meta.ID != meta.BotID {
				if ui.Button(c, "Close thread").FontSize(11).Disabled(meta.Closed).Clicked() {
					a.closeThread(meta)
				}
			}
		}
		ui.Box(c).Grow(1)
		if a.highlightID != "" {
			if ui.Button(c, "Clear highlight").FontSize(11).Clicked() {
				a.highlightID = ""
			}
		}
	})
	if meta, ok := a.threadMeta(t.id); ok {
		if meta.Closed {
			ui.Text(c, "This thread is closed. Create a new thread to continue.").FontSize(12).TextColor(a.pal.muted).Padding(6, 24)
		}
		if meta.Summary != "" {
			ui.Text(c, "Context compacted · original messages preserved").FontSize(11).TextColor(a.pal.muted).Padding(4, 24)
		}
	}
}
func (a *app) chatMessage(c *ui.Context, b store.Bot, m *store.Message, done bool) {
	if speaker, ok := a.bot(m.BotID); ok && m.Role != "user" {
		b = speaker
	}
	box := ui.Column(c.Key("message:" + m.ID)).Gap(4)
	if a.highlightID == m.ID {
		box.Padding(8).Radius(12).Border(2, a.pal.accent).Background(a.pal.accent.Alpha(0.08))
	}
	box.Children(func() { a.message(c, b, m, done) })
}

func (a *app) roomConversation(id string) *thread {
	key := roomKey(id)
	t := a.threads[key]
	if t == nil {
		t = &thread{id: key, roomID: id, lives: map[string]*store.Message{}, active: map[string]bool{}, sequences: map[string]uint64{}}
		t.list.FollowEnd, t.list.FollowAnimation = true, &ui.Smooth
		a.threads[key] = t
	}
	if !t.loaded {
		msgs, err := a.api.RoomTranscript(id)
		if err != nil {
			a.status = err.Error()
		} else {
			t.msgs, t.loaded = msgs, true
		}
	}
	return t
}

// Room events can interleave several speakers. Sequence and live reply state
// belong to the source thread, while committed messages share the room list.
func (a *app) foldRoom(u harness.Update) {
	if _, ok := a.room(u.RoomID); !ok {
		if rooms, err := a.api.Rooms(); err == nil {
			a.rooms = rooms
		}
	}
	if u.Bot != nil {
		if i := slices.IndexFunc(a.bots, func(b store.Bot) bool { return b.ID == u.BotID }); i >= 0 {
			a.bots[i] = *u.Bot
		} else {
			a.bots = append(a.bots, *u.Bot)
		}
	}
	t := a.roomConversation(u.RoomID)
	if u.Sequence > 0 && u.Sequence < t.sequence {
		return
	}
	if u.Sequence > t.sequence {
		t.sequence = u.Sequence
	}
	if u.Sync && u.BotID == "" {
		t.msgs, t.pendingMessages = slices.Clone(u.Messages), nil
		t.lives, t.active, t.sequences = map[string]*store.Message{}, map[string]bool{}, map[string]uint64{}
		t.approvals = map[string]*approvalCard{}
		t.busy, t.queued = u.Busy || u.Queued > 0, u.Queued
		t.pendingMessages = u.QueuedMessages
		return
	}
	source := u.ThreadID
	if source == "" {
		source = u.BotID
	}
	if source == "" {
		source = u.RoomID
	}
	if u.Sequence > 0 && (u.Sequence < t.sequences[source] || (!u.Sync && u.Sequence == t.sequences[source])) {
		return
	}
	t.sequences[source] = u.Sequence
	if t.approvals == nil {
		t.approvals = map[string]*approvalCard{}
	}
	decorate := func(m store.Message) store.Message {
		if m.BotID == "" && m.Role != "user" {
			m.BotID = u.BotID
		}
		if m.ThreadID == "" {
			m.ThreadID = u.ThreadID
		}
		return m
	}
	if u.Sync {
		// The baseline room snapshot precedes its active speaker records.
		// Merge those records so one speaker cannot erase another live reply.
		for _, pending := range u.QueuedMessages {
			if !slices.ContainsFunc(t.pendingMessages, func(m store.Message) bool { return m.ID == pending.ID }) {
				t.pendingMessages = append(t.pendingMessages, pending)
			}
		}
		for _, m := range u.Messages {
			t.add(decorate(m))
		}
		for id, card := range t.approvals {
			if card.botID == u.BotID {
				delete(t.approvals, id)
			}
		}
		for _, req := range u.Approvals {
			t.approvals[req.ID] = &approvalCard{request: req, botID: u.BotID}
		}
		delete(t.lives, source)
	}
	if u.QueuedUser != nil && !slices.ContainsFunc(t.pendingMessages, func(m store.Message) bool { return m.ID == u.QueuedUser.ID }) {
		t.pendingMessages = append(t.pendingMessages, *u.QueuedUser)
	}
	if u.User != nil {
		t.add(decorate(*u.User))
		t.pendingMessages = slices.DeleteFunc(t.pendingMessages, func(m store.Message) bool { return m.ID == u.User.ID })
	}
	if u.Approval != nil {
		t.approvals[u.Approval.ID] = &approvalCard{request: *u.Approval, botID: u.BotID}
	}
	if u.ResolvedApproval != "" {
		delete(t.approvals, u.ResolvedApproval)
	}
	t.active[source] = u.Busy
	if u.Done {
		delete(t.lives, source)
		for id, card := range t.approvals {
			if card.botID == u.BotID {
				delete(t.approvals, id)
			}
		}
		if u.Reply != nil {
			t.add(decorate(*u.Reply))
			if a.selectedRoom != u.RoomID {
				t.unread++
			}
		}
		if msgs, err := a.api.RoomTranscript(u.RoomID); err == nil {
			for _, m := range msgs {
				t.add(m)
			}
		}
	} else if u.Reply != nil {
		reply := decorate(*u.Reply)
		t.lives[source] = &reply
	}
	// Busy and Queued describe the whole room, including other speakers.
	// A completed speaker must not leave its earlier busy state latched.
	t.busy = u.Busy || u.Queued > 0
	if u.Sync {
		// Snapshot projections may also carry partial replies in Messages.
		// Keep each partial reply only in the live list until its Done event.
		t.msgs = slices.DeleteFunc(t.msgs, func(m store.Message) bool {
			for _, live := range t.lives {
				if live.ID == m.ID {
					return true
				}
			}
			return false
		})
	}
	t.queued = u.Queued
	if !t.busy {
		t.pendingMessages = nil
	}
	if u.Error != "" {
		a.status = u.Error
	}
	if u.Err != nil {
		a.status = u.Err.Error()
	}
}

func (a *app) roomChat(c *ui.Context) {
	r, ok := a.room(a.selectedRoom)
	if !ok {
		ui.Column(c).Fill().Center().Children(func() {
			ui.Text(c, "Channel unavailable")
			if ui.Button(c, "Back to bots").Clicked() {
				a.selectFirstVisible()
			}
		})
		return
	}
	t := a.roomConversation(r.ID)
	t.unread = 0
	ui.Column(c.Key(roomKey(r.ID))).Fill().Children(func() {
		ui.Row(c).Height(topBar(c)).Padding(0, 20).Gap(10).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(a.pal.hairline).DragWindow().Children(func() {
			ui.Column(c).Grow(1).Gap(2).Children(func() {
				ui.Text(c, "# "+r.Name).FontSize(15).FontWeight(700)
				mode := "Lead"
				if r.ReplyMode == "auto" {
					mode = "Auto"
				}
				ui.Text(c, fmt.Sprintf("%d members · %s", len(r.Members), mode)).FontSize(11.5).TextColor(a.pal.muted)
			})
			if a.iconButton(c, editIcon, "Edit channel", "Members, reply rules and instructions").Clicked() {
				a.openRoomForm(r)
			}
			if a.iconButton(c, trashIcon, "Delete channel", "Delete channel").Clicked() {
				a.deletingRoom = true
			}
		})
		a.conversationActions(c, t)
		liveKeys := make([]string, 0, len(t.lives))
		for k := range t.lives {
			liveKeys = append(liveKeys, k)
		}
		slices.Sort(liveKeys)
		rows := len(t.msgs) + len(liveKeys)
		t.list.Key = func(i int) any {
			if i < len(t.msgs) {
				return t.msgs[i].ID
			}
			return "live:" + liveKeys[i-len(t.msgs)]
		}
		if rows == 0 {
			ui.Column(c).Grow(1).Center().Gap(10).Children(func() {
				ui.Text(c, "Welcome to #"+r.Name).FontSize(19).FontWeight(700)
				ui.Text(c, "Mention a member, or let the reply rule choose.").FontSize(13).TextColor(a.pal.muted)
			})
		} else {
			ui.List(c, &t.list, rows, func(i int) {
				m, done := (*store.Message)(nil), true
				if i < len(t.msgs) {
					m = &t.msgs[i]
				} else {
					m, done = t.lives[liveKeys[i-len(t.msgs)]], false
				}
				speaker := store.Bot{Name: m.From}
				if speaker.Name == "" {
					speaker.Name = "Bot"
				}
				a.chatMessage(c, speaker, m, done)
			}).Grow(1).Padding(20, 28, 12, 28).Gap(20).Justify(ui.End)
		}
		if a.status != "" {
			ui.Text(c, a.status).FontSize(12).TextColor(c.Theme().Danger).Padding(6, 24)
		}
		if len(t.approvals) > 0 {
			a.approvalCards(c, store.Bot{}, t)
		}
		ui.Row(c).Gap(6).Padding(4, 24).Children(func() {
			ui.Text(c, "Mention").FontSize(11).TextColor(a.pal.muted)
			ui.Button(c, "@ member").Label("Mention member").Menu(func(m *ui.Menu) {
				for _, id := range r.Members {
					if b, ok := a.bot(id); ok && m.Item(b.Name).Chosen() {
						if t.draft != "" && !strings.HasSuffix(t.draft, " ") {
							t.draft += " "
						}
						t.draft += "@" + b.ID + " "
					}
				}
			})
			if t.busy {
				ui.Text(c, "Working…").FontSize(11).TextColor(a.pal.accent)
			}
			if len(t.pendingMessages) > 0 {
				ui.Text(c, fmt.Sprintf("%d pending", len(t.pendingMessages))).FontSize(11).TextColor(a.pal.muted)
			}
		})
		a.composer(c, store.Bot{Name: "#" + r.Name}, t)
	})
}
func (a *app) stopConversation(_ store.Bot) {
	var err error
	if a.selectedRoom != "" {
		err = a.api.StopRoom(a.selectedRoom)
	} else if id := a.conversationID(); id != "" {
		err = a.api.StopThread(id)
	}
	if err != nil {
		a.status = err.Error()
		return
	}
	if updates, err := a.api.Snapshot(); err == nil {
		for _, u := range updates {
			a.fold(u)
		}
	} else {
		a.status = err.Error()
	}
}

func (a *app) openRoomForm(r store.Room) {
	if r.ID == "" {
		r = store.Room{ID: store.NewID(), Created: time.Now(), ReplyMode: "lead"}
	}
	r.Members = slices.Clone(r.Members)
	a.roomForm, a.roomError, a.roomFormOpen = r, "", true
}
func formField(c *ui.Context, label string, build func()) {
	ui.Column(c).Gap(4).Children(func() { ui.Text(c, label).FontSize(12).FontWeight(600); build() })
}
func (a *app) roomFormView(c *ui.Context) {
	r := &a.roomForm
	ui.Column(c).Width(500).Gap(12).Children(func() {
		ui.Text(c, "Channel settings").FontSize(18).FontWeight(700)
		ui.Scroll(c).MaxHeight(470).Gap(12).Children(func() {
			formField(c, "Name", func() { ui.TextInput(c, &r.Name).Label("Channel name").AutoFocus() })
			formField(c, "Members", func() {
				ui.Column(c).Gap(5).Children(func() {
					if len(a.bots) == 0 {
						ui.Text(c, "Create a bot before adding channel members.").FontSize(12)
					}
					for _, b := range a.bots {
						selected := slices.Contains(r.Members, b.ID)
						before := selected
						ui.Checkbox(c.Key(b.ID), &selected, b.Name).Label("Member: " + b.Name)
						if before != selected {
							if selected {
								r.Members = append(r.Members, b.ID)
								if r.LeadID == "" {
									r.LeadID = b.ID
								}
							} else {
								r.Members = slices.DeleteFunc(r.Members, func(id string) bool { return id == b.ID })
								if r.LeadID == b.ID {
									r.LeadID = ""
									if len(r.Members) > 0 {
										r.LeadID = r.Members[0]
									}
								}
							}
						}
					}
				})
			})
			labels := []string{}
			ids := []string{}
			for _, id := range r.Members {
				if b, ok := a.bot(id); ok {
					labels = append(labels, b.Name+" · "+id[:min(6, len(id))])
					ids = append(ids, id)
				}
			}
			lead := ""
			if i := slices.Index(ids, r.LeadID); i >= 0 {
				lead = labels[i]
			}
			formField(c, "Lead", func() { ui.Select(c, &lead, labels).Label("Channel lead").Disabled(len(labels) == 0) })
			if i := slices.Index(labels, lead); i >= 0 {
				r.LeadID = ids[i]
			}
			mode := "Lead"
			if r.ReplyMode == "auto" {
				mode = "Auto"
			}
			formField(c, "Reply rule", func() { ui.Select(c, &mode, []string{"Lead", "Auto"}).Label("Reply rule") })
			r.ReplyMode = strings.ToLower(mode)
			if mode == "Auto" {
				ui.Text(c, "The decision model chooses a member; uncertain decisions fall back to the lead.").FontSize(12).TextColor(a.pal.muted)
			}
			formField(c, "Shared instructions", func() { ui.TextArea(c, &r.Instructions).Lines(3, 5).Label("Channel instructions") })
			formField(c, "Working directory", func() {
				ui.TextInput(c, &r.WorkDir).Label("Channel working directory").Placeholder("Empty for a channel-owned folder")
			})
		})
		if a.roomError != "" {
			ui.Text(c, a.roomError).FontSize(12).TextColor(c.Theme().Danger)
		}
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "Cancel").Clicked() {
				a.roomFormOpen = false
			}
			if ui.PrimaryButton(c, "Save channel").Clicked() {
				r.Name, r.WorkDir = strings.TrimSpace(r.Name), strings.TrimSpace(r.WorkDir)
				if r.Name == "" || len(r.Members) == 0 || r.LeadID == "" {
					a.roomError = "Enter a name, choose members and a lead."
					return
				}
				if err := a.api.SaveRoom(*r); err != nil {
					a.roomError = err.Error()
					return
				}
				a.refreshNavigation()
				a.selectRoom(r.ID)
				a.roomFormOpen = false
			}
		})
	})
}

func (a *app) openDecision() {
	settings, err := a.api.DecisionSettings()
	if err != nil {
		a.status = err.Error()
		return
	}
	a.decision, a.decisionError, a.decisionOpen = settings, "", true
}
func (a *app) decisionView(c *ui.Context) {
	ui.Column(c).Width(460).Gap(14).Children(func() {
		ui.Text(c, "Decision model").FontSize(18).FontWeight(700)
		ui.Text(c, "Used by Auto channels to select the replying member.").FontSize(12).TextColor(a.pal.muted)
		formField(c, "Base URL", func() {
			ui.TextInput(c, &a.decision.BaseURL).Label("Decision base URL").Placeholder("https://openrouter.ai/api/v1")
		})
		formField(c, "Model", func() { ui.TextInput(c, &a.decision.Model).Label("Decision model ID").Placeholder("Provider/model") })
		formField(c, "API key environment variable", func() {
			ui.TextInput(c, &a.decision.APIKeyEnv).Label("Decision API key environment variable").Placeholder("OPENROUTER_API_KEY")
		})
		ui.Text(c, "Enter the environment variable name. Its value stays on the service.").FontSize(12).TextColor(a.pal.muted)
		if a.decisionError != "" {
			ui.Text(c, a.decisionError).FontSize(12).TextColor(c.Theme().Danger)
		}
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "Cancel").Clicked() {
				a.decisionOpen = false
			}
			if ui.PrimaryButton(c, "Save decision model").Clicked() {
				a.decision.BaseURL, a.decision.Model, a.decision.APIKeyEnv = strings.TrimSpace(a.decision.BaseURL), strings.TrimSpace(a.decision.Model), strings.TrimSpace(a.decision.APIKeyEnv)
				if err := a.api.SaveDecisionSettings(a.decision); err != nil {
					a.decisionError = err.Error()
				} else {
					a.decisionOpen = false
				}
			}
		})
	})
}

func (a *app) openMemories(t *thread) {
	scope := t.id
	if scope == "" {
		scope = a.conversationID()
	}
	if t.roomID != "" {
		scope = t.roomID
	}
	a.memoryScope, a.memoryEdit, a.memoryError = scope, store.Memory{}, ""
	memories, err := a.api.Memories(scope)
	if err != nil {
		a.status = err.Error()
		return
	}
	a.memories, a.memoriesOpen = memories, true
}
func (a *app) reloadMemories() {
	memories, err := a.api.Memories(a.memoryScope)
	if err != nil {
		a.memoryError = err.Error()
	} else {
		a.memories = memories
	}
}
func (a *app) memoriesView(c *ui.Context) {
	ui.Column(c).Width(520).Gap(12).Children(func() {
		ui.Text(c, "Conversation memory").FontSize(18).FontWeight(700)
		ui.Text(c, "Saved facts are recalled for relevant messages in this conversation.").FontSize(12).TextColor(a.pal.muted)
		ui.Scroll(c).MaxHeight(260).Gap(8).Children(func() {
			if len(a.memories) == 0 {
				ui.Text(c, "No memories yet.").FontSize(13).TextColor(a.pal.muted)
			}
			for _, memory := range a.memories {
				ui.Column(c.Key(memory.ID)).Padding(10).Radius(10).Background(a.pal.bubble).Gap(6).Children(func() {
					ui.Text(c, memory.Text).FontSize(13).Selectable()
					ui.Row(c).Gap(8).Children(func() {
						if ui.Button(c, "Edit").Label("Edit memory " + memory.ID).FontSize(11).Clicked() {
							a.memoryEdit = memory
						}
						if ui.Button(c, "Delete").Label("Delete memory " + memory.ID).FontSize(11).Clicked() {
							if err := a.api.DeleteMemory(a.memoryScope, memory.ID); err != nil {
								a.memoryError = err.Error()
							} else {
								if a.memoryEdit.ID == memory.ID {
									a.memoryEdit = store.Memory{}
								}
								a.reloadMemories()
							}
						}
					})
				})
			}
		})
		formField(c, "Memory", func() {
			ui.TextArea(c, &a.memoryEdit.Text).Lines(3, 5).Label("Memory text").Placeholder("A useful fact or preference")
		})
		if a.memoryError != "" {
			ui.Text(c, a.memoryError).FontSize(12).TextColor(c.Theme().Danger)
		}
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "Close").Clicked() {
				a.memoriesOpen = false
			}
			if a.memoryEdit.ID != "" {
				if ui.Button(c, "New memory").Clicked() {
					a.memoryEdit = store.Memory{}
				}
			}
			if ui.PrimaryButton(c, "Save memory").Disabled(strings.TrimSpace(a.memoryEdit.Text) == "").Clicked() {
				memory := a.memoryEdit
				memory.Text = strings.TrimSpace(memory.Text)
				if memory.ID == "" {
					memory.ID, memory.Created = store.NewID(), time.Now()
				}
				if err := a.api.SaveMemory(a.memoryScope, memory); err != nil {
					a.memoryError = err.Error()
				} else {
					a.memoryEdit, a.memoryError = store.Memory{}, ""
					a.reloadMemories()
				}
			}
		})
	})
}

func (a *app) runSearch() {
	a.searchError, a.searchResults = "", nil
	a.searchQuery = strings.TrimSpace(a.searchQuery)
	if a.searchQuery == "" {
		a.searchRan = false
		return
	}
	results, err := a.api.Search(a.searchQuery)
	if err != nil {
		a.searchError = err.Error()
		a.searchRan = false
		return
	}
	a.searchResults, a.searchRan = results, true
}
func (a *app) resultLocation(r store.SearchResult) string {
	if r.RoomID != "" {
		if room, ok := a.room(r.RoomID); ok {
			return "#" + room.Name
		}
		return "Channel"
	}
	name := "Bot"
	if b, ok := a.bot(r.BotID); ok {
		name = b.Name
	}
	if meta, ok := a.threadMeta(r.ThreadID); ok && meta.ID != meta.BotID {
		name += " · " + a.threadTitleFor(meta)
	}
	return name
}
func (a *app) openSearchResult(r store.SearchResult) {
	var t *thread
	if r.RoomID != "" {
		a.selectRoom(r.RoomID)
		t = a.roomConversation(r.RoomID)
	} else {
		id := r.ThreadID
		if id == "" {
			id = r.BotID
		}
		a.refreshThreads(r.BotID)
		if meta, ok := a.threadMeta(id); ok {
			a.selectThread(meta)
		} else if id == r.BotID {
			a.selectBot(r.BotID)
		} else {
			a.searchError = "This thread is no longer available."
			return
		}
		t = a.thread(id)
	}
	// Reload persisted history so a search made after a reconnect can navigate
	// to messages that were never loaded into this window's list.
	var msgs []store.Message
	var err error
	if r.RoomID != "" {
		msgs, err = a.api.RoomTranscript(r.RoomID)
	} else {
		id := r.ThreadID
		if id == "" {
			id = r.BotID
		}
		msgs, err = a.api.ThreadTranscript(id)
	}
	if err != nil {
		a.searchError = err.Error()
		return
	}
	t.msgs = msgs
	index := slices.IndexFunc(t.msgs, func(m store.Message) bool { return m.ID == r.Message.ID })
	if index < 0 {
		a.searchError = "This message is no longer available."
		return
	}
	a.highlightID = r.Message.ID
	t.list.FollowEnd = false
	t.list.ScrollTo(index, ui.Center)
	a.searchOpen = false
}
func searchExcerpt(text, query string) string {
	// Clip by runes, never split UTF-8; the selected transcript row gets a
	// visible accent outline after navigation.
	runes := []rune(text)
	if len(runes) <= 180 {
		return text
	}
	start := 0
	if i := strings.Index(strings.ToLower(text), strings.ToLower(query)); i > 0 {
		start = max(0, utf8.RuneCountInString(text[:i])-45)
	}
	end := min(len(runes), start+180)
	out := string(runes[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(runes) {
		out += "…"
	}
	return out
}
func (a *app) searchView(c *ui.Context) {
	ui.Column(c).Width(640).Gap(12).Children(func() {
		ui.Text(c, "Search transcripts").FontSize(18).FontWeight(700)
		ui.Row(c).Gap(8).Children(func() {
			ui.TextInput(c, &a.searchQuery).Label("Search transcript text").Placeholder("Search all conversations").Grow(1).AutoFocus().OnTextKey(func(k ui.TextKey) bool {
				if k.Key == ui.KeyEnter {
					a.searchQuery = k.Text()
					a.runSearch()
					return true
				}
				return false
			})
			if ui.PrimaryButton(c, "Search").Clicked() {
				a.runSearch()
			}
		})
		if a.searchError != "" {
			ui.Text(c, a.searchError).FontSize(12).TextColor(c.Theme().Danger)
		}
		if a.searchRan {
			ui.Text(c, fmt.Sprintf("%d results · up to 100 matches", len(a.searchResults))).FontSize(12).TextColor(a.pal.muted)
		}
		ui.Scroll(c).Height(340).Gap(6).Children(func() {
			if a.searchRan && len(a.searchResults) == 0 {
				ui.Text(c, "No matching messages.").FontSize(13).TextColor(a.pal.muted)
			}
			for i, result := range a.searchResults {
				row := ui.ButtonBase(c.Key(fmt.Sprint(i))).Label(fmt.Sprintf("Search result %d", i+1)).Padding(12).Radius(10).Background(a.pal.bubble)
				row.Children(func() {
					ui.Column(c).Gap(5).Children(func() {
						ui.Text(c, a.resultLocation(result)+" · "+stamp(result.Message.At)).FontSize(11).TextColor(a.pal.muted)
						ui.Text(c, searchExcerpt(result.Message.Text, a.searchQuery)).FontSize(13).MaxLines(3)
					})
				})
				if row.Clicked() {
					a.openSearchResult(result)
				}
			}
		})
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "Clear results").Clicked() {
				a.searchQuery, a.searchError, a.highlightID = "", "", ""
				a.searchResults, a.searchRan = nil, false
			}
			if ui.Button(c, "Close").Clicked() {
				a.searchOpen = false
			}
		})
	})
}

func (a *app) phase3ModalOpen() bool {
	return a.threadFormOpen || a.roomFormOpen || a.deletingRoom || a.decisionOpen || a.memoriesOpen || a.searchOpen || a.compactOpen
}
func (a *app) phase3Modals(c *ui.Context) {
	ui.Modal(c, &a.threadFormOpen, func() {
		ui.Column(c).Width(400).Gap(14).Children(func() {
			ui.Text(c, "New thread").FontSize(18).FontWeight(700)
			ui.TextInput(c, &a.threadTitle).Label("Thread title").Placeholder("Conversation title").AutoFocus()
			if a.threadError != "" {
				ui.Text(c, a.threadError).FontSize(12).TextColor(c.Theme().Danger)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Cancel").Clicked() {
					a.threadFormOpen = false
				}
				if ui.PrimaryButton(c, "Create thread").Clicked() {
					if strings.TrimSpace(a.threadTitle) == "" {
						a.threadError = "Give the thread a title."
						return
					}
					meta, err := a.api.CreateThread(a.threadBot, strings.TrimSpace(a.threadTitle))
					if err != nil {
						a.threadError = err.Error()
						return
					}
					a.refreshThreads(a.threadBot)
					a.selectThread(meta)
					a.threadFormOpen = false
				}
			})
		})
	})
	ui.Modal(c, &a.roomFormOpen, func() { a.roomFormView(c) })
	ui.Modal(c, &a.decisionOpen, func() { a.decisionView(c) })
	ui.Modal(c, &a.memoriesOpen, func() { a.memoriesView(c) })
	ui.Modal(c, &a.searchOpen, func() { a.searchView(c) })
	ui.Modal(c, &a.compactOpen, func() {
		ui.Column(c).Width(440).Gap(14).Children(func() {
			ui.Text(c, "Compact context?").FontSize(18).FontWeight(700)
			ui.Text(c, "Older messages become a summary for future turns. The last 12 messages and the full original transcript remain available.").FontSize(13)
			if a.compactError != "" {
				ui.Text(c, a.compactError).FontSize(12).TextColor(c.Theme().Danger)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Cancel").Clicked() {
					a.compactOpen = false
				}
				if ui.PrimaryButton(c, "Compact").Clicked() {
					meta, err := a.api.CompactThread(a.compactScope)
					if err != nil {
						a.compactError = err.Error()
						return
					}
					a.refreshThreads(meta.BotID)
					a.compactOpen = false
					c.Toast("Context compacted; transcript preserved")
				}
			})
		})
	})
	if room, ok := a.room(a.selectedRoom); ok {
		if ui.AlertDialog(c, &a.deletingRoom, "Delete #"+room.Name+"?", "The channel and its conversations will be deleted.", "Cancel", "Delete channel permanently") == 1 {
			if err := a.api.DeleteRoom(room.ID); err != nil {
				a.status = err.Error()
			} else {
				delete(a.threads, roomKey(room.ID))
				a.refreshNavigation()
				a.selectFirstVisible()
			}
		}
	}
}

// foldMetadata applies profile snapshots before loading a conversation. Metadata
// notifications are list invalidations, never idle runtime events.
func (a *app) foldMetadata(u harness.Update) bool {
	if u.Metadata {
		if bots, err := a.api.Bots(); err == nil {
			a.bots = bots
		} else {
			a.status = err.Error()
		}
		a.refreshNavigation()
		if a.selectedRoom != "" {
			if _, ok := a.room(a.selectedRoom); !ok {
				delete(a.threads, roomKey(a.selectedRoom))
				a.selectFirstVisible()
			}
		} else if b, ok := a.bot(a.selected); !ok || b.Hidden && !a.showHidden {
			a.selectFirstVisible()
		}
		if a.memoriesOpen {
			a.reloadMemories()
		}
		return true
	}
	if u.Thread != nil {
		if a.botThreads == nil {
			a.botThreads = map[string][]store.Thread{}
		}
		rows := a.botThreads[u.Thread.BotID]
		if i := slices.IndexFunc(rows, func(t store.Thread) bool { return t.ID == u.Thread.ID }); i >= 0 {
			rows[i] = *u.Thread
		} else {
			rows = append(rows, *u.Thread)
		}
		a.botThreads[u.Thread.BotID] = rows
	}
	if u.Room != nil {
		if i := slices.IndexFunc(a.rooms, func(r store.Room) bool { return r.ID == u.Room.ID }); i >= 0 {
			a.rooms[i] = *u.Room
		} else {
			a.rooms = append(a.rooms, *u.Room)
		}
	}
	return false
}
