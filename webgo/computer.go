package webgo

import (
	"encoding/json"
	"errors"
	"runtime"

	"github.com/egoist/mygo"

	"bqbot/internal/computer"
	"bqbot/internal/driver"
	"bqbot/internal/service"
	"bqbot/internal/store"
)

// ComputerAction exposes desktop operations without exposing service credentials.
type ComputerAction struct {
	Kind       string               `json:"kind"`
	BotID      string               `json:"bot_id"`
	ThreadID   string               `json:"thread_id"`
	Config     store.ComputerConfig `json:"config"`
	Action     computer.Action      `json:"action"`
	Confirmed  bool                 `json:"confirmed"`
	Permission string               `json:"permission"`
}

func (d *Desktop) Computer(r ComputerAction) (json.RawMessage, error) {
	d.mu.Lock()
	api := d.app.api
	closed := d.closed
	d.mu.Unlock()
	if closed {
		return nil, errors.New("desktop is closed")
	}
	var v any
	var e error
	switch r.Kind {
	case "host-status":
		v, e = api.HostStatus()
	case "host-start":
		v, e = api.StartHost()
	case "host-stop":
		v, e = api.StopHost()
	case "host-permissions":
		mygo.RunOnMain(func() { v, e = computer.RequestHostPermissions() })
	case "host-settings":
		if runtime.GOOS != "darwin" {
			return nil, errors.New("内置本机控制目前仅支持 macOS")
		}
		var pane string
		switch r.Permission {
		case "accessibility":
			pane = "Privacy_Accessibility"
		case "screen_recording":
			pane = "Privacy_ScreenCapture"
		default:
			return nil, errors.New("unknown host permission")
		}
		e = mygo.Shell.OpenExternal("x-apple.systempreferences:com.apple.preference.security?" + pane)
	case "computers":
		v, e = api.Computers()
	case "save-computer":
		v, e = api.SaveComputer(r.Config)
	case "action":
		v, e = api.ComputerAction(r.BotID, service.ComputerRequest{Action: r.Action, Confirmed: r.Confirmed})
	case "capture-to-chat":
		// Capture again in Go; the page cannot smuggle a different image into this operation.
		var out computer.Result
		threads, err := api.Threads(r.BotID)
		if err != nil {
			return nil, err
		}
		found := false
		for _, t := range threads {
			if t.ID == r.ThreadID && t.BotID == r.BotID && !t.Closed && t.AskerID == "" && t.RoomID == "" {
				found = true
			}
		}
		if !found {
			return nil, errors.New("select an open private conversation for this computer")
		}
		out, e = api.ComputerAction(r.BotID, service.ComputerRequest{Action: computer.Action{Kind: "screenshot"}})
		if e == nil && out.Image != nil {
			v, e = api.SendThread(r.ThreadID, "查看这张电脑截图。", []driver.Attachment{*out.Image}, "queue")
		}
	default:
		return nil, errors.New("unknown computer action")
	}
	if e != nil {
		return nil, e
	}
	return json.Marshal(v)
}
