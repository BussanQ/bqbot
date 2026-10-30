package webgo

import (
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"

	"bqbot/internal/driver"
	"bqbot/internal/service"
	"bqbot/internal/store"
	"bqbot/internal/voice"
	"github.com/egoist/mygo"
)

// Desktop exposes only application operations to the bundled WebView. Service
// credentials stay in Go. The mutex serializes bridge calls with SSE callbacks;
// the existing app reducer remains responsible for ordering and reconciliation.
type Desktop struct {
	voice  *voice.Player
	mu     sync.Mutex
	app    *app
	notify func()
	closed bool
}

func newDesktop(api *service.Client) (*Desktop, error) {
	d := &Desktop{}
	a, err := newApp(api, func(fn func()) {
		d.mu.Lock()
		if d.closed {
			d.mu.Unlock()
			return
		}
		fn()
		notify := d.notify
		d.mu.Unlock()
		if notify != nil {
			notify()
		}
	})
	d.app = a
	return d, err
}

func (d *Desktop) start(notify func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.notify = notify
	d.app.connect()
	d.app.probeEngines()
}

func (d *Desktop) close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	if d.voice != nil {
		d.voice.Close()
	}
	if d.app != nil && d.app.cancel != nil {
		d.app.cancel()
	}
}

type webApproval struct {
	BotID   string                 `json:"bot_id"`
	Request driver.ApprovalRequest `json:"request"`
}

type webConversation struct {
	ID        string          `json:"id"`
	BotID     string          `json:"bot_id"`
	RoomID    string          `json:"room_id"`
	Messages  []store.Message `json:"messages"`
	Pending   []store.Message `json:"pending"`
	Approvals []webApproval   `json:"approvals"`
	Busy      bool            `json:"busy"`
	Queued    int             `json:"queued"`
	Answering string          `json:"answering"`
	Closed    bool            `json:"closed"`
}

type webState struct {
	Bots         []store.Bot               `json:"bots"`
	Rooms        []store.Room              `json:"rooms"`
	Threads      map[string][]store.Thread `json:"threads"`
	Engines      []service.Engine          `json:"engines"`
	Selected     string                    `json:"selected"`
	Conversation *webConversation          `json:"conversation"`
	Unread       map[string]int            `json:"unread"`
	Status       string                    `json:"status"`
}

// State marshals while holding the lock, so serialization cannot race a stream.
func (d *Desktop) State() (json.RawMessage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	a := d.app
	s := webState{Bots: a.bots, Rooms: a.rooms, Threads: a.botThreads, Engines: a.catalog, Selected: a.selected, Unread: map[string]int{}, Status: a.status}
	var t *thread
	if a.selectedRoom != "" {
		t = a.roomConversation(a.selectedRoom)
	} else if a.selected != "" {
		t = a.thread(a.conversationID())
	}
	if t != nil {
		c := &webConversation{ID: t.id, BotID: t.botID, RoomID: t.roomID, Messages: append([]store.Message{}, t.msgs...), Pending: t.pendingMessages, Busy: t.busy, Queued: t.queued, Answering: t.answering}
		merge := func(m *store.Message) {
			if m == nil {
				return
			}
			if i := slices.IndexFunc(c.Messages, func(v store.Message) bool { return v.ID == m.ID }); i >= 0 {
				c.Messages[i] = *m
			} else {
				c.Messages = append(c.Messages, *m)
			}
		}
		merge(t.live)
		for _, m := range t.lives {
			merge(m)
		}
		sort.SliceStable(c.Messages, func(i, j int) bool { return c.Messages[i].At.Before(c.Messages[j].At) })
		for _, card := range t.approvals {
			c.Approvals = append(c.Approvals, webApproval{BotID: card.botID, Request: card.request})
		}
		sort.Slice(c.Approvals, func(i, j int) bool { return c.Approvals[i].Request.ID < c.Approvals[j].Request.ID })
		if meta, ok := a.threadMeta(t.id); ok {
			c.Closed = meta.Closed || meta.AskerID != "" || meta.RoomID != ""
		}
		s.Conversation = c
	}
	for id, t := range a.threads {
		s.Unread[id] = t.unread
	}
	s.Status = a.status
	return json.Marshal(s)
}

// WebAction is an explicit command vocabulary, never an arbitrary HTTP proxy.
type WebAction struct {
	Kind        string                           `json:"kind"`
	ID          string                           `json:"id"`
	BotID       string                           `json:"bot_id"`
	Scope       string                           `json:"scope"`
	Text        string                           `json:"text"`
	Mode        string                           `json:"mode"`
	ShowHidden  bool                             `json:"show_hidden"`
	Bot         store.Bot                        `json:"bot"`
	Room        store.Room                       `json:"room"`
	Memory      store.Memory                     `json:"memory"`
	Decision    driver.ApprovalDecision          `json:"decision"`
	Settings    map[string]driver.EngineSettings `json:"settings"`
	Routing     store.DecisionSettings           `json:"routing"`
	Attachments []driver.Attachment              `json:"attachments"`
}

// Action reuses the service's validation and existing conversation reducer.
func (d *Desktop) Action(r WebAction) (json.RawMessage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	a := d.app
	var result any
	var err error
	refresh := false
	switch r.Kind {
	case "visibility":
		a.showHidden = r.ShowHidden
		if b, ok := a.bot(a.selected); ok && b.Hidden && !a.showHidden {
			a.selectFirstVisible()
		}
	case "select":
		if r.Scope == "room" {
			if _, ok := a.room(r.ID); !ok {
				return nil, errors.New("room not found")
			}
			a.selectRoom(r.ID)
		} else {
			meta, ok := a.threadMeta(r.ID)
			if !ok {
				return nil, errors.New("conversation not found")
			}
			a.selectThread(meta)
			if b, ok := a.bot(meta.BotID); ok && b.Unread {
				b.Unread = false
				if !a.persistBot(b) {
					return nil, errors.New(a.status)
				}
			}
		}
	case "send":
		// Address the captured conversation explicitly: switching tabs while a
		// request is in flight must never send the draft to a different bot.
		var t *thread
		if r.Scope == "room" {
			if _, ok := a.room(r.ID); !ok {
				return nil, errors.New("room not found")
			}
			t = a.roomConversation(r.ID)
		} else {
			if _, ok := a.threadMeta(r.ID); !ok {
				return nil, errors.New("conversation not found")
			}
			t = a.thread(r.ID)
		}
		b, _ := a.bot(t.botID)
		t.attachments, t.sendMode = r.Attachments, r.Mode
		if !a.send(b, t, r.Text) {
			return nil, errors.New(a.statusOr("Enter a message or attach an image."))
		}
	case "stop":
		if r.Scope == "room" {
			err = a.api.StopRoom(r.ID)
		} else {
			err = a.api.StopThread(r.ID)
		}
	case "save-bot":
		_, exists := a.bot(r.Bot.ID)
		if r.Bot.ID == "" {
			r.Bot.ID = store.NewID()
		}
		err = a.api.SaveBot(r.Bot)
		if err == nil && !exists {
			a.selected, a.selectedThread, a.selectedRoom = r.Bot.ID, r.Bot.ID, ""
		}
		refresh = true
	case "delete-bot":
		err = a.api.DeleteBot(r.ID)
		refresh = true
		if err == nil {
			delete(a.threads, r.ID)
			a.selected = ""
		}
	case "clone-bot":
		result, err = a.api.CloneBot(r.ID)
		refresh = true
	case "create-thread":
		var meta store.Thread
		meta, err = a.api.CreateThread(r.BotID, r.Text)
		if err == nil {
			a.refreshThreads(r.BotID)
			a.selectThread(meta)
		}
	case "close-thread":
		err = a.api.CloseThread(r.ID)
		refresh = true
	case "compact":
		result, err = a.api.CompactThread(r.ID)
		refresh = true
	case "save-room":
		if r.Room.ID == "" {
			r.Room.ID = store.NewID()
		}
		err = a.api.SaveRoom(r.Room)
		refresh = true
		if err == nil {
			a.selectedRoom = r.Room.ID
		}
	case "delete-room":
		err = a.api.DeleteRoom(r.ID)
		refresh = true
		if err == nil {
			delete(a.threads, roomKey(r.ID))
			a.selectedRoom = ""
		}
	case "approve":
		err = a.api.Approve(r.BotID, r.ID, r.Decision)
		if err == nil {
			for _, t := range a.threads {
				delete(t.approvals, r.ID)
			}
		}
	case "settings":
		result, err = a.api.Settings()
	case "save-settings":
		err = a.api.SaveSettings(r.Settings)
		if err == nil {
			a.probeEngines()
		}
	case "routing":
		result, err = a.api.DecisionSettings()
	case "save-routing":
		err = a.api.SaveDecisionSettings(r.Routing)
	case "memories":
		result, err = a.api.Memories(r.Scope)
	case "save-memory":
		if r.Memory.ID == "" {
			r.Memory.ID = store.NewID()
		}
		err = a.api.SaveMemory(r.Scope, r.Memory)
	case "delete-memory":
		err = a.api.DeleteMemory(r.Scope, r.ID)
	case "search":
		result, err = a.api.Search(r.Text)
	case "dismiss-status":
		a.status = ""
	default:
		return nil, errors.New("unknown desktop action")
	}
	if err != nil {
		return nil, err
	}
	if refresh {
		a.bots, err = a.api.Bots()
		if err != nil {
			return nil, err
		}
		a.refreshNavigation()
		if a.selectedRoom == "" {
			if b, ok := a.bot(a.selected); !ok || b.Hidden && !a.showHidden {
				a.selectFirstVisible()
			}
		}
	}
	return json.Marshal(result)
}

func (a *app) statusOr(fallback string) string {
	if a.status != "" {
		return a.status
	}
	return fallback
}

// PickImages reads only files explicitly chosen in the native picker.
func (d *Desktop) PickImages() ([]driver.Attachment, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: "添加图片", Multiple: true, Filters: []mygo.FileFilter{{Name: "Images", Extensions: []string{"png", "jpg", "jpeg", "gif", "webp"}}}})
	if err != nil {
		return nil, err
	}
	if len(paths) > 4 {
		return nil, errors.New("每条消息最多添加 4 张图片")
	}
	images := []driver.Attachment{}
	for _, path := range paths {
		attachment, err := readAttachment(path)
		if err != nil {
			return nil, err
		}
		images = append(images, attachment)
	}
	return images, nil
}

func (d *Desktop) Copy(text string) { mygo.Clipboard.WriteText(text) }

// OpenLink only opens web URLs outside the privileged application page.
func (d *Desktop) OpenLink(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || strings.ContainsAny(raw, "\r\n") {
		return errors.New("unsupported link")
	}
	return mygo.Shell.OpenExternal(raw)
}

func (d *Desktop) OpenWorkDir(id string) error {
	path, err := d.app.api.WorkDir(id)
	if err != nil {
		return err
	}
	return mygo.Shell.OpenPath(path)
}
