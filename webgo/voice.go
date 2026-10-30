package webgo

import (
	"context"
	"encoding/json"
	"errors"

	"bqbot/internal/voice"
)

// VoiceAction is a closed vocabulary for user-initiated speech operations.
type VoiceAction struct {
	Kind   string       `json:"kind"`
	Text   string       `json:"text"`
	Config voice.Config `json:"config"`
}

func (d *Desktop) Voice(r VoiceAction) (json.RawMessage, error) {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil, errors.New("desktop is closed")
	}
	if d.voice == nil {
		d.voice = &voice.Player{}
	}
	player, api := d.voice, d.app.api
	d.mu.Unlock()
	var value any
	var err error
	switch r.Kind {
	case "settings":
		value, err = api.Voice()
	case "save":
		player.Stop()
		err = api.SaveVoice(r.Config)
	case "speak":
		err = player.Start(func(ctx context.Context) ([]byte, error) { return api.SynthesizeVoice(ctx, r.Text) })
		value = player.Status()
	case "stop":
		player.Stop()
		value = player.Status()
	case "status":
		value = player.Status()
	default:
		return nil, errors.New("unknown voice action")
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
