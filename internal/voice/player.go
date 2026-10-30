package voice

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"time"

	"github.com/egoist/mygo/audio"
)

// Status describes the current desktop playback without retaining speech text.
type Status struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

// Player owns one cancelable synthesis/playback operation. A new request replaces it.
type Player struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	serial uint64
	status Status
	closed bool
}

func (p *Player) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.status.State == "" {
		return Status{State: "idle"}
	}
	return p.status
}

// Start synthesizes and plays asynchronously; fetch must respect cancellation.
func (p *Player) Start(fetch func(context.Context) ([]byte, error)) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.New("speech player is closed")
	}
	if p.cancel != nil {
		p.cancel()
	}
	p.serial++
	serial := p.serial
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.status = Status{State: "synthesizing"}
	go func() {
		defer cancel()
		data, err := fetch(ctx)
		var player *audio.Player
		if err == nil && ctx.Err() == nil {
			if len(data) > MaxAudioBytes {
				err = errors.New("speech audio is too large")
			} else {
				var decoder audio.Decoder
				decoder, _, err = audio.NewDecoder(bytes.NewReader(data))
				if err == nil {
					if decoder.SampleRate() < 8000 || decoder.SampleRate() > 192000 || decoder.Channels() < 1 || decoder.Channels() > 2 || decoder.Len() > int64(decoder.SampleRate())*600 {
						err = errors.New("unsupported speech audio dimensions")
					} else {
						player = audio.NewPlayer(decoder)
					}
				}
			}
		}
		if player != nil {
			defer player.Close()
			// Check ownership under the same lock as Stop/Start; stale synthesis never starts playback.
			p.mu.Lock()
			if serial != p.serial || p.closed || ctx.Err() != nil {
				p.mu.Unlock()
				return
			}
			player.Play()
			p.status = Status{State: "playing"}
			p.mu.Unlock()
			ticker := time.NewTicker(50 * time.Millisecond)
			defer ticker.Stop()
		playing:
			for {
				select {
				case <-ctx.Done():
					break playing
				case <-ticker.C:
					if e := audio.Err(); e != nil {
						err = errors.New("audio output is unavailable")
						break playing
					}
					if !player.Playing() {
						break playing
					}
				}
			}
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if serial != p.serial {
			return
		}
		p.cancel = nil
		p.status = Status{State: "idle"}
		if err != nil && ctx.Err() == nil {
			p.status = Status{State: "error", Error: err.Error()}
		}
	}()
	return nil
}

func (p *Player) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.serial++
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.status = Status{State: "idle"}
}

func (p *Player) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	p.serial++
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.status = Status{State: "idle"}
}
