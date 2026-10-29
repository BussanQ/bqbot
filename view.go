package main

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"bqbot/internal/driver"
	"bqbot/internal/harness"
	"bqbot/internal/store"
)

// thread is what the window shows of one bot's conversation.
type thread struct {
	loaded bool
	msgs   []store.Message
	live   *store.Message // the reply streaming in
	busy   bool
	// answering is the bot whose question the busy bot answers, in its own
	// session with that bot.
	answering string
	draft     string
	list      ui.ListState
	unread    int // replies that came while another bot was shown
}

// session is a handoff session to look at: one bot's questions to another
// and its answers.
type session struct {
	label  string
	answer string // the answering bot's name
	msgs   []store.Message
}

// app is the window's state. The view and every method run on the main
// thread; other goroutines change it through post.
type app struct {
	st *store.Store
	h  *harness.Harness
	// post runs fn on the main thread and redraws: Window.Update.
	post func(fn func())

	engines  []string // engines found on this machine
	bots     []store.Bot
	selected string
	threads  map[string]*thread
	md       markdown.Options
	pal      palette // this frame's colors
	split    float32
	status   string

	creating   bool
	newName    string
	newEngine  string
	newModel   string
	newPersona string
	formErr    string

	deleting bool

	query string // filters the sidebar

	editing bool   // the form edits editID rather than creating a bot
	editID  string // the bot being edited

	viewingSessions bool
	sessions        []session
	sessionPick     string

	toolsOpen map[string]*bool // by message ID, whether its tool lines show
}

func newApp(st *store.Store, h *harness.Harness, post func(func())) (*app, error) {
	bots, err := st.Bots()
	if err != nil {
		return nil, err
	}
	a := &app{st: st, h: h, post: post, bots: bots, threads: map[string]*thread{}, split: 260, toolsOpen: map[string]*bool{}, engines: []string{"echo"}}
	if len(bots) > 0 {
		a.selected = bots[0].ID
	}
	h.OnUpdate = func(u harness.Update) { a.post(func() { a.fold(u) }) }
	return a, nil
}

// probeEngines looks for the agent CLIs in the background.
func (a *app) probeEngines() {
	for name, d := range a.h.Drivers {
		if d.Binary() == "" {
			continue
		}
		go func() {
			if _, err := driver.Probe(context.Background(), d.Binary()); err == nil {
				a.post(func() {
					if !slices.Contains(a.engines, name) {
						a.engines = append(a.engines, name)
						slices.Sort(a.engines)
					}
				})
			}
		}()
	}
}

func (a *app) thread(id string) *thread {
	t := a.threads[id]
	if t == nil {
		t = &thread{}
		t.list.FollowEnd = true
		t.list.FollowAnimation = &ui.Smooth
		a.threads[id] = t
	}
	if !t.loaded {
		t.loaded = true
		msgs, err := a.st.Transcript(id)
		if err != nil {
			a.status = err.Error()
		}
		t.msgs = msgs
		t.busy = a.h.Busy(id)
	}
	return t
}

func (a *app) bot(id string) (store.Bot, bool) {
	for _, b := range a.bots {
		if b.ID == id {
			return b, true
		}
	}
	return store.Bot{}, false
}

// fold applies a harness update.
func (a *app) fold(u harness.Update) {
	t := a.thread(u.BotID)
	if u.Done {
		t.busy, t.live, t.answering = false, nil, ""
		if u.Reply != nil && (u.Reply.Text != "" || len(u.Reply.Tools) > 0) {
			t.add(*u.Reply)
			if u.BotID != a.selected {
				t.unread++
			}
		}
		if u.Err != nil {
			a.status = u.Err.Error()
		}
		// The harness saved the session cursor; pick it up.
		if bots, err := a.st.Bots(); err == nil {
			a.bots = bots
		}
		return
	}
	t.busy = u.Busy
	t.live = u.Reply
	t.answering = u.Answering
}

// add appends m unless the thread has it: a thread loaded after the
// harness wrote m to the transcript already read it.
func (t *thread) add(m store.Message) {
	if !slices.ContainsFunc(t.msgs, func(o store.Message) bool { return o.ID == m.ID }) {
		t.msgs = append(t.msgs, m)
	}
}

func (a *app) send(b store.Bot, t *thread, text string) bool {
	if strings.TrimSpace(text) == "" || t.busy {
		return false
	}
	m, err := a.h.Send(b, text)
	if err != nil {
		a.status = err.Error()
		return false
	}
	a.status = ""
	t.msgs = append(t.msgs, m)
	t.busy = true
	return true
}

// formEngines is what the engine menu offers: the engines found, and the
// edited bot's own even when it is not.
func (a *app) formEngines() []string {
	if a.editing && a.newEngine != "" && !slices.Contains(a.engines, a.newEngine) {
		if b, ok := a.bot(a.editID); ok && b.Engine == a.newEngine {
			return append(slices.Clone(a.engines), a.newEngine)
		}
	}
	return a.engines
}

// saveForm creates the bot, or saves the one edited.
func (a *app) saveForm() bool {
	name := strings.TrimSpace(a.newName)
	if name == "" {
		a.formErr = "Give the bot a name."
		return false
	}
	if !slices.Contains(a.formEngines(), a.newEngine) {
		a.formErr = "Choose an engine."
		return false
	}
	b := store.Bot{ID: store.NewID(), Created: time.Now()}
	if a.editing {
		old, ok := a.bot(a.editID)
		if !ok {
			a.formErr = "The bot is gone."
			return false
		}
		b = old
		// A session of one engine cannot resume on another.
		if b.Engine != a.newEngine || b.Model != strings.TrimSpace(a.newModel) {
			b.Cursor, b.Handoffs = "", nil
		}
	}
	b.Name, b.Engine, b.Model, b.Persona = name, a.newEngine, strings.TrimSpace(a.newModel), strings.TrimSpace(a.newPersona)
	if err := a.st.SaveBot(b); err != nil {
		a.formErr = err.Error()
		return false
	}
	if i := slices.IndexFunc(a.bots, func(o store.Bot) bool { return o.ID == b.ID }); i >= 0 {
		a.bots[i] = b
	} else {
		a.bots = append(a.bots, b)
	}
	a.selected = b.ID
	a.creating = false
	return true
}

func (a *app) openCreate() {
	a.creating, a.editing, a.editID = true, false, ""
	a.newName, a.newModel, a.newPersona, a.formErr = "", "", "", ""
	a.newEngine = a.engines[len(a.engines)-1]
	for _, e := range []string{"claude", "codex", "pi"} {
		if slices.Contains(a.engines, e) {
			a.newEngine = e
			break
		}
	}
}

func (a *app) openEdit(b store.Bot) {
	a.creating, a.editing, a.editID = true, true, b.ID
	a.newName, a.newEngine, a.newModel, a.newPersona, a.formErr = b.Name, b.Engine, b.Model, b.Persona, ""
}

// openSessions reads the bot's handoff sessions, both ways.
func (a *app) openSessions(b store.Bot) {
	a.sessions = nil
	for _, o := range a.bots {
		if o.ID == b.ID {
			continue
		}
		if msgs, err := a.st.HandoffTranscript(b.ID, o.ID); err == nil && len(msgs) > 0 {
			a.sessions = append(a.sessions, session{label: o.Name + " → " + b.Name, answer: b.Name, msgs: msgs})
		}
		if msgs, err := a.st.HandoffTranscript(o.ID, b.ID); err == nil && len(msgs) > 0 {
			a.sessions = append(a.sessions, session{label: b.Name + " → " + o.Name, answer: o.Name, msgs: msgs})
		}
	}
	a.sessionPick = ""
	if len(a.sessions) > 0 {
		a.sessionPick = a.sessions[0].label
	}
	a.viewingSessions = true
}

func (a *app) openFolder(c *ui.Context, b store.Bot) {
	dir, err := a.st.WorkDir(b.ID)
	if err != nil {
		a.status = err.Error()
		return
	}
	c.OpenURL((&url.URL{Scheme: "file", Path: dir + "/"}).String())
}

func (a *app) deleteBot(id string) {
	a.h.Stop(id)
	if err := a.st.DeleteBot(id); err != nil {
		a.status = err.Error()
		return
	}
	a.bots = slices.DeleteFunc(a.bots, func(b store.Bot) bool { return b.ID == id })
	delete(a.threads, id)
	a.selected = ""
	if len(a.bots) > 0 {
		a.selected = a.bots[0].ID
	}
}

// selectBot shows the bot's chat, reading its replies.
func (a *app) selectBot(id string) {
	a.selected = id
	if t := a.threads[id]; t != nil {
		t.unread = 0
	}
}

func (a *app) view(c *ui.Context) {
	t := *c.Theme()
	a.pal = paletteFor(&t)
	t.Accent, t.AccentText = a.pal.accent, a.pal.onAccent
	t.Selection = a.pal.accent.Alpha(0.3)
	c.SetTheme(&t)
	if c.Vibrancy() {
		c.Root().Background(ui.Transparent)
	}

	modal := a.creating || a.deleting || a.viewingSessions
	if !modal && c.Shortcut(ui.Cmd, ui.KeyN) {
		a.openCreate()
	}
	if b, ok := a.bot(a.selected); ok && !modal && c.Shortcut(ui.Cmd, ui.KeyPeriod) {
		a.h.Stop(b.ID)
	}
	ui.Split(c, &a.split, func() {
		side := ui.Column(c).Fill()
		if !c.Vibrancy() {
			side.Background(a.pal.sidebar)
		}
		side.Children(func() { a.sidebar(c) })
	}, func() {
		ui.Box(c).Fill().Background(a.pal.bg).Children(func() { a.chat(c) })
	}).Fill()

	ui.Modal(c, &a.creating, func() { a.botForm(c) })
	ui.Modal(c, &a.viewingSessions, func() { a.sessionsView(c) })
	if b, ok := a.bot(a.selected); ok {
		if ui.AlertDialog(c, &a.deleting, fmt.Sprintf("Delete “%s”?", b.Name),
			"Its conversation and working folder are deleted too.", "Cancel", "Delete") == 1 {
			a.deleteBot(b.ID)
			c.Toast("Deleted " + b.Name)
		}
	}
}

// iconButton is a round button showing only an icon, named by label.
func (a *app) iconButton(c *ui.Context, svg *ui.SVG, label, tip string) ui.Element {
	b := roundButton(c, 16, a.pal.muted, label).Tooltip(tip)
	b.Children(func() { ui.Icon(c, svg).Size(16, 16) })
	return b
}

func (a *app) sidebar(c *ui.Context) {
	p := a.pal
	bar := c.TitleBar()
	ui.Column(c).Fill().Padding(0, 12, 12, 12).Gap(4).DragWindow().Children(func() {
		// The strip of the window buttons, which the new bot button shares.
		ui.Row(c).Height(topBar(c)).PaddingStart(max(bar.Left-12, 6)).Gap(8).AlignItems(ui.Center).Children(func() {
			if bar.Left == 0 {
				ui.Icon(c, botIcon).Size(18, 18).TextColor(p.accent)
				ui.Text(c, "bqbot").FontSize(15).FontWeight(700)
			}
			ui.Box(c).Grow(1)
			if roundButton(c, 16, p.muted, "New bot").Tooltip("New bot (⌘N)").Children(func() {
				ui.Icon(c, plusIcon).Size(16, 16)
			}).Clicked() {
				a.openCreate()
			}
		})
		if len(a.bots) > 5 {
			ui.SearchField(c, &a.query).Label("Search bots").Margin(0, 0, 8, 0)
		}
		if len(a.bots) == 0 {
			ui.Column(c).Grow(1).Padding(8, 10).Gap(4).Children(func() {
				ui.Text(c, "No bots yet.").FontSize(13).TextColor(p.muted)
				ui.Text(c, "Press ⌘N to create one.").FontSize(12).TextColor(p.faint)
			})
		} else {
			ui.Text(c, "Bots").FontSize(11.5).FontWeight(600).TextColor(p.faint).Padding(6, 10, 4, 10)
			q := strings.ToLower(strings.TrimSpace(a.query))
			ui.Scroll(c.Key("bots")).Grow(1).Margin(0, -6).Padding(0, 6).Gap(2).Children(func() {
				for _, b := range a.bots {
					if q != "" && !strings.Contains(strings.ToLower(b.Name), q) {
						continue
					}
					a.sidebarItem(c, b)
				}
			})
		}
		ui.Row(c).Gap(6).AlignItems(ui.Center).Padding(8, 10, 0, 10).Children(func() {
			ui.Box(c).Size(6, 6).Radius(3).Background(ui.Oklch(0.72, 0.17, 150))
			ui.Text(c, strings.Join(a.engines, " · ")).FontSize(11.5).TextColor(p.muted).SingleLine()
		})
	})
}

func (a *app) sidebarItem(c *ui.Context, b store.Bot) {
	p := a.pal
	th := a.threads[b.ID]
	selected := b.ID == a.selected
	row := ui.ButtonBase(c.Key(b.ID)).Height(46).PaddingX(8).Gap(10).Radius(10).AlignItems(ui.Center).Justify(ui.Start).
		FocusRing(false).Label(b.Name).Selected(selected)
	bg := ui.Transparent
	switch {
	case selected:
		bg = p.press
	case row.Hovered():
		bg = p.hover
	}
	row.Background(row.AnimateColor("bg", bg, hoverFade))
	row.Children(func() {
		ui.Avatar(c, b.Name, nil).FontSize(15)
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(1).Children(func() {
			ui.Text(c, b.Name).FontSize(13.5).FontWeight(600).TextColor(p.text).SingleLine()
			sub := b.Engine
			if b.Model != "" {
				sub += " · " + b.Model
			}
			ui.Text(c, sub).FontSize(11.5).TextColor(p.muted).SingleLine()
		})
		switch {
		case th != nil && th.busy:
			ui.Spinner(c)
		case th != nil && th.unread > 0:
			ui.Badge(c, fmt.Sprint(th.unread)).Background(p.accent).TextColor(p.onAccent)
		}
	})
	if row.Clicked() {
		a.selectBot(b.ID)
	}
	row.ContextMenu(func(m *ui.Menu) {
		if m.Item("Edit…").Chosen() {
			a.openEdit(b)
		}
		if m.Item("Handoff sessions…").Chosen() {
			a.openSessions(b)
		}
		if m.Item("Open working folder").Chosen() {
			a.openFolder(c, b)
		}
		m.Separator()
		if m.Item("Delete…").Chosen() {
			a.selectBot(b.ID)
			a.deleting = true
		}
	})
}

func (a *app) chat(c *ui.Context) {
	p := a.pal
	b, ok := a.bot(a.selected)
	if !ok {
		ui.Column(c).Fill().Center().Gap(12).DragWindow().Children(func() {
			ui.Box(c).Size(72, 72).Radius(22).Background(p.accent.Alpha(0.12)).Center().Children(func() {
				ui.Icon(c, botIcon).Size(34, 34).TextColor(p.accent)
			})
			ui.Text(c, "Create a bot to start chatting.").FontSize(17).FontWeight(600)
			ui.Text(c, "Each bot runs on an agent CLI of this machine: claude, codex or pi.").FontSize(13).TextColor(p.muted)
			if pillButton(c, plusIcon, "Create a bot", p.accent, p.onAccent).Margin(8, 0, 0, 0).Clicked() {
				a.openCreate()
			}
		})
		return
	}
	t := a.thread(b.ID)
	t.unread = 0
	t.list.Key = func(i int) any {
		if i < len(t.msgs) {
			return t.msgs[i].ID
		}
		return "live"
	}
	rows := len(t.msgs)
	if t.live != nil {
		rows++
	}
	ui.Column(c.Key(b.ID)).Fill().Children(func() {
		a.header(c, b, t)
		if rows == 0 {
			// The list keeps its rows at the bottom; the greeting goes in
			// the middle.
			ui.Column(c).Grow(1).Center().Gap(10).Padding(20, 24).Children(func() {
				ui.Avatar(c, b.Name, nil).FontSize(28)
				ui.Text(c, "Say hello to "+b.Name+".").FontSize(18).FontWeight(700).Margin(6, 0, 0, 0)
				if b.Persona != "" {
					ui.Text(c, b.Persona).FontSize(13).TextColor(p.muted).TextAlign(ui.Center).MaxLines(3).MaxWidth(420)
				}
			})
		} else {
			ui.List(c, &t.list, rows, func(i int) {
				if i < len(t.msgs) {
					a.message(c, b, &t.msgs[i], true)
				} else {
					a.message(c, b, t.live, false)
				}
			}).Grow(1).Padding(24, 28, 12, 28).Gap(22).Justify(ui.End)
		}
		if t.busy && t.answering != "" {
			ui.Row(c).AlignSelf(ui.Center).Gap(6).AlignItems(ui.Center).Padding(4, 12).Margin(0, 0, 6, 0).Radius(999).
				Background(p.hover).Children(func() {
				ui.Icon(c, handoffIcon).Size(12, 12).TextColor(p.muted)
				ui.Text(c, "正在回答 "+t.answering+" 的提问（单独的会话）").FontSize(12).TextColor(p.muted)
			})
		}
		if a.status != "" {
			danger := c.Theme().Danger
			ui.Row(c).Gap(8).AlignItems(ui.Center).Margin(0, 24, 8, 24).Padding(8, 8, 8, 12).Radius(10).
				Background(danger.Alpha(0.1)).TextColor(danger).Children(func() {
				ui.Icon(c, alertIcon).Size(16, 16)
				ui.Text(c, a.status).FontSize(13).Selectable().Grow(1)
				if roundButton(c, 12, danger, "Dismiss").Children(func() { ui.Icon(c, closeIcon).Size(12, 12) }).Clicked() {
					a.status = ""
				}
			})
		}
		a.composer(c, b, t)
	})
}

func (a *app) header(c *ui.Context, b store.Bot, t *thread) {
	p := a.pal
	ui.Row(c).Height(topBar(c)).Gap(10).AlignItems(ui.Center).Padding(0, 14, 0, 20).
		BorderWidth(0, 0, 1, 0).BorderColor(p.hairline).DragWindow().Children(func() {
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(1).Children(func() {
			ui.Text(c, b.Name).FontSize(14).FontWeight(700).SingleLine()
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				model := b.Model
				if model == "" {
					model = "default model"
				}
				ui.Text(c, b.Engine+" · "+model).FontSize(11.5).TextColor(p.muted).SingleLine()
				if !slices.Contains(a.engines, b.Engine) {
					ui.Text(c, "· engine not found").FontSize(11.5).TextColor(c.Theme().Warning)
				}
				if t.busy {
					ui.Text(c, "· working…").FontSize(11.5).TextColor(p.accent)
				}
			})
		})
		if a.iconButton(c, editIcon, "Edit bot", "Edit name, engine and persona").Clicked() {
			a.openEdit(b)
		}
		if a.iconButton(c, historyIcon, "Handoff sessions", "Sessions with other bots").Clicked() {
			a.openSessions(b)
		}
		if a.iconButton(c, folderIcon, "Open working folder", "Open working folder").Clicked() {
			a.openFolder(c, b)
		}
		if a.iconButton(c, trashIcon, "Delete bot", "Delete bot").Clicked() {
			a.deleting = true
		}
	})
}

// composer is a card along the foot of the chat: the draft over a row of
// the hint and the send button.
func (a *app) composer(c *ui.Context, b store.Bot, t *thread) {
	p := a.pal
	card := ui.Column(c).Gap(6).Margin(4, 24, 18, 24).Padding(12, 10, 10, 16).Radius(16).Background(p.field)
	card.Children(func() {
		in := ui.TextAreaBase(c, &t.draft).Lines(1, 8).Placeholder("Message " + b.Name).Label("Message").FontSize(14).AutoFocus().
			OnTextKey(func(k ui.TextKey) bool {
				if k.Key == ui.KeyEnter && k.Mods == 0 {
					if a.send(b, t, k.Text()) {
						k.SetText("")
					}
					return true
				}
				return false // Shift+Enter breaks the line
			})
		if in.Focused() {
			card.Border(1, p.accent.Alpha(0.55)).Shadow(0, 0, 0, 3, p.accent.Alpha(0.15))
		} else {
			card.Border(1, p.hairline).Shadow(0, 4, 16, -4, p.shade)
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Enter to send · Shift+Enter for a new line").FontSize(11).TextColor(p.faint).Grow(1)
			if t.busy {
				if a.sendButton(c, stopIcon, "Stop", "Stop (⌘.)", p.text, p.bg, false).Clicked() {
					a.h.Stop(b.ID)
				}
			} else if empty := strings.TrimSpace(t.draft) == ""; a.sendButton(c, sendIcon, "Send", "Send (Enter)", p.accent, p.onAccent, empty).Clicked() && !empty {
				if a.send(b, t, t.draft) {
					t.draft = ""
				}
			}
		})
	})
}

// sendButton is the composer's round button, filled with bg, faded while
// off.
func (a *app) sendButton(c *ui.Context, svg *ui.SVG, label, tip string, bg, fg ui.Color, off bool) ui.Element {
	b := ui.ButtonBase(c.Key(label)).Size(30, 30).Center().Radius(999).Label(label).Tooltip(tip).TextColor(fg).Disabled(off)
	fill := bg
	switch {
	case off:
		fill = a.pal.press
		b.TextColor(a.pal.faint)
	case b.Hovered():
		fill = bg.Mix(ui.RGB(255, 255, 255), 0.12)
	}
	b.Background(b.AnimateColor("bg", fill, hoverFade))
	pose := ui.Pose{}
	if b.Pressed() {
		pose.Scale = 0.9
	}
	b.AnimatePose(pose, ui.Snappy)
	b.Children(func() { ui.Icon(c, svg).Size(15, 15) })
	return b
}

// handoffNote reports whether a tool line is the harness's note of a
// handoff, which shows apart from the engine's tools.
func handoffNote(line string) bool {
	return strings.HasPrefix(line, "已转交给 ") || strings.HasPrefix(line, "转交给 ")
}

func stamp(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	if now := time.Now(); now.Year() == at.Year() && now.YearDay() == at.YearDay() {
		return at.Format("15:04")
	}
	return at.Format("01-02 15:04")
}

// message builds one message: the person's in a bubble on the right, the
// bot's beside its avatar, as Markdown under the tools it ran.
func (a *app) message(c *ui.Context, b store.Bot, m *store.Message, done bool) {
	p := a.pal
	switch m.Role {
	case "user":
		ui.Column(c).AlignItems(ui.End).Gap(5).PaddingStart(80).Children(func() {
			ui.Text(c, m.Text).FontSize(14).Selectable().Padding(10, 15).Radius(18, 18, 6, 18).
				Background(p.accent).TextColor(p.onAccent).MaxWidth(560).Shadow(0, 2, 8, -2, p.accent.Alpha(0.35))
			ui.Text(c, stamp(m.At)).FontSize(11).TextColor(p.faint).PaddingEnd(4)
		})
	case "error":
		danger := c.Theme().Danger
		ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(10, 14).Radius(12).Background(danger.Alpha(0.08)).
			Border(1, danger.Alpha(0.2)).TextColor(danger).Children(func() {
			ui.Icon(c, alertIcon).Size(16, 16)
			ui.Text(c, m.Text).FontSize(13).Selectable().Grow(1)
		})
	default:
		ui.Row(c).Gap(12).AlignItems(ui.Start).PaddingEnd(40).Children(func() {
			ui.Avatar(c, b.Name, nil).FontSize(14)
			ui.Column(c).Grow(1).Shrink(1).MinWidth(0).MaxWidth(760).Gap(8).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Height(28).Children(func() {
					ui.Text(c, b.Name).FontSize(13).FontWeight(600)
					if done {
						ui.Text(c, stamp(m.At)).FontSize(11).TextColor(p.faint)
					}
				})
				a.tools(c, m, done)
				switch {
				case done:
					markdown.View(c, m.Text, &a.md)
				case m.Text == "":
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Spinner(c)
						ui.Text(c, "Thinking…").FontSize(13).TextColor(p.muted)
					})
				default:
					markdown.Stream(c, m.Text, &a.md)
				}
				if done && m.Text != "" {
					copied := roundButton(c, 13, p.faint, "Copy").Tooltip("Copy").Margin(-4, 0, 0, -8).Children(func() {
						ui.Icon(c, copyIcon).Size(13, 13)
					})
					if copied.Clicked() {
						c.WriteClipboard(m.Text)
						c.Toast("Copied")
					}
				}
			})
		})
	}
}

// tools shows the handoff notes, and the engine's tool lines folded up
// once the reply is done.
func (a *app) tools(c *ui.Context, m *store.Message, done bool) {
	p := a.pal
	var other []string
	for _, line := range m.Tools {
		if !handoffNote(line) {
			other = append(other, line)
			continue
		}
		ui.Row(c).AlignSelf(ui.Start).Gap(6).AlignItems(ui.Center).Padding(4, 11).Radius(999).
			Background(p.accent.Alpha(0.1)).TextColor(p.accent).Children(func() {
			ui.Icon(c, handoffIcon).Size(12, 12)
			ui.Text(c, line).FontSize(12).FontWeight(500)
		})
	}
	if len(other) == 0 {
		return
	}
	lines := func() {
		ui.Column(c).Gap(4).Padding(6, 0, 2, 4).Children(func() {
			for _, line := range other {
				ui.Row(c).Gap(7).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, toolIcon).Size(11, 11).TextColor(p.faint)
					ui.Text(c, line).FontSize(12).TextColor(p.muted).SingleLine()
				})
			}
		})
	}
	box := ui.Column(c).AlignSelf(ui.Start).MaxWidth(560).Padding(6, 12).Radius(10).Background(p.bubble).Border(1, p.hairline)
	if !done {
		box.Children(lines)
		return
	}
	open := a.toolsOpen[m.ID]
	if open == nil {
		open = new(bool)
		a.toolsOpen[m.ID] = open
	}
	label := fmt.Sprintf("Ran %d tools", len(other))
	if len(other) == 1 {
		label = "Ran 1 tool"
	}
	box.Children(func() {
		ui.Collapsible(c, label, open, lines).FontSize(12).TextColor(p.muted)
	})
}

func (a *app) sessionsView(c *ui.Context) {
	p := a.pal
	ui.Column(c).Width(640).Gap(14).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Handoff sessions").FontSize(17).FontWeight(700).Grow(1)
			if roundButton(c, 14, p.muted, "Close").Children(func() { ui.Icon(c, closeIcon).Size(14, 14) }).Clicked() {
				a.viewingSessions = false
			}
		})
		if len(a.sessions) == 0 {
			ui.Text(c, "No handoff sessions yet. When a bot asks another, their conversation shows here.").FontSize(13).TextColor(p.muted)
			return
		}
		labels := make([]string, len(a.sessions))
		for i, s := range a.sessions {
			labels[i] = s.label
		}
		ui.Select(c, &a.sessionPick, labels).Label("Session")
		i := slices.Index(labels, a.sessionPick)
		if i < 0 {
			return
		}
		s := a.sessions[i]
		ui.Scroll(c).Height(420).Radius(12).Background(p.bubble).Border(1, p.hairline).Children(func() {
			ui.Column(c).Gap(16).Padding(14, 16).Children(func() {
				for _, m := range s.msgs {
					ui.Column(c.Key(m.ID)).Gap(6).Children(func() {
						who := s.answer
						if m.Role == "user" {
							who = m.From
						}
						ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
							ui.Avatar(c, who, nil).FontSize(10)
							ui.Text(c, who).FontSize(12.5).FontWeight(600)
							ui.Text(c, stamp(m.At)).FontSize(11).TextColor(p.faint)
						})
						if m.Role == "user" {
							ui.Text(c, m.Text).FontSize(13.5).Selectable().Padding(8, 12).Radius(10).Background(p.press)
						} else {
							markdown.View(c, m.Text, &a.md)
						}
					})
				}
			})
		})
	})
}

func (a *app) botForm(c *ui.Context) {
	p := a.pal
	title, action := "New bot", "Create"
	if a.editing {
		title, action = "Edit bot", "Save"
	}
	ui.Column(c).Width(440).Gap(14).Children(func() {
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			name := strings.TrimSpace(a.newName)
			if name == "" {
				ui.Box(c).Size(40, 40).Radius(20).Background(p.accent.Alpha(0.12)).Center().Children(func() {
					ui.Icon(c, botIcon).Size(20, 20).TextColor(p.accent)
				})
			} else {
				ui.Avatar(c, name, nil).FontSize(20)
			}
			ui.Text(c, title).FontSize(17).FontWeight(700)
		})
		field := func(label string, fn func()) {
			ui.Column(c).Gap(5).Children(func() {
				ui.Text(c, label).FontSize(12).FontWeight(600).TextColor(p.muted)
				fn()
			})
		}
		field("Name", func() { ui.TextInput(c, &a.newName).Placeholder("Name").Label("Name").AutoFocus() })
		field("Engine", func() { ui.Select(c, &a.newEngine, a.formEngines()).Label("Engine") })
		field("Model", func() {
			ui.TextInput(c, &a.newModel).Placeholder("Empty for the engine's default").Label("Model")
		})
		field("Persona", func() {
			ui.TextArea(c, &a.newPersona).Lines(3, 8).Placeholder("Personality and instructions").Label("Persona")
		})
		if a.editing {
			ui.Text(c, "Changing the engine or model starts new sessions.").FontSize(12).TextColor(p.muted)
		}
		if a.formErr != "" {
			ui.Text(c, a.formErr).FontSize(13).TextColor(c.Theme().Danger)
		}
		ui.Row(c).Gap(8).Justify(ui.End).Margin(4, 0, 0, 0).Children(func() {
			if ui.Button(c, "Cancel").Clicked() {
				a.creating = false
			}
			if ui.PrimaryButton(c, action).Clicked() && a.saveForm() {
				c.Toast(action + "d " + strings.TrimSpace(a.newName))
			}
		})
	})
}
