// Direct input is scoped to a confirmed viewer and one shared Xvnc connection.
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

type inputGate struct{ sync.RWMutex }

// RFB uses an eight-byte KeyEvent and a six-byte PointerEvent. Keeping one
// connection for the entire gesture preserves held buttons and modifier keys.
type rfbInput struct {
	net.Conn
	width, height int
	x, y          uint16
	heldPointer   bool
	keys          map[uint32]bool
}

func openInput(ctx context.Context) (*rfbInput, error) {
	conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", "127.0.0.1:5901")
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	read := func(n int) ([]byte, error) { b := make([]byte, n); _, e := io.ReadFull(conn, b); return b, e }
	b, e := read(12)
	if e != nil || string(b) != "RFB 003.008\n" {
		return nil, errors.New("unexpected RFB greeting")
	}
	if _, e = conn.Write(b); e != nil {
		return nil, e
	}
	n, e := read(1)
	if e != nil {
		return nil, e
	}
	b, e = read(int(n[0]))
	if e != nil {
		return nil, e
	}
	none := false
	for _, v := range b {
		if v == 1 {
			none = true
		}
	}
	if !none {
		return nil, errors.New("unexpected Xvnc authentication")
	}
	if _, e = conn.Write([]byte{1}); e != nil {
		return nil, e
	}
	b, e = read(4)
	if e != nil || binary.BigEndian.Uint32(b) != 0 {
		return nil, errors.New("Xvnc refused input connection")
	}
	if _, e = conn.Write([]byte{1}); e != nil {
		return nil, e
	}
	b, e = read(24)
	if e != nil {
		return nil, e
	}
	w, h := int(binary.BigEndian.Uint16(b)), int(binary.BigEndian.Uint16(b[2:]))
	name := binary.BigEndian.Uint32(b[20:])
	if w < 1 || h < 1 || name > 65536 {
		return nil, errors.New("invalid Xvnc desktop")
	}
	if _, e = read(int(name)); e != nil {
		return nil, e
	}
	conn.SetDeadline(time.Time{})
	ok = true
	return &rfbInput{Conn: conn, width: w, height: h, keys: make(map[uint32]bool)}, nil
}

func (r *rfbInput) pointer(mask byte) error {
	b := []byte{5, mask, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(b[2:], r.x)
	binary.BigEndian.PutUint16(b[4:], r.y)
	_, err := r.Write(b)
	return err
}
func (r *rfbInput) key(k uint32, down bool) error {
	b := []byte{4, 0, 0, 0, 0, 0, 0, 0}
	if down {
		b[1] = 1
		r.keys[k] = true
	} else {
		delete(r.keys, k)
	}
	binary.BigEndian.PutUint32(b[4:], k)
	_, err := r.Write(b)
	return err
}

type inputEvent struct {
	Kind string
	X, Y int
	Mask uint8
	Key  uint32
	Down bool
}

func (r *rfbInput) apply(e inputEvent) error {
	r.SetWriteDeadline(time.Now().Add(time.Second))
	switch e.Kind {
	case "pointer":
		if e.X < 0 || e.Y < 0 || e.X >= r.width || e.Y >= r.height || e.Mask > 127 {
			return errors.New("invalid pointer")
		}
		r.x, r.y = uint16(e.X), uint16(e.Y)
		r.heldPointer = e.Mask != 0
		return r.pointer(e.Mask)
	case "key":
		if e.Key == 0 || e.Key > 0x0110ffff || (e.Down && !r.keys[e.Key] && len(r.keys) >= 64) {
			return errors.New("invalid key")
		}
		return r.key(e.Key, e.Down)
	case "reset":
		for k := range r.keys {
			if err := r.key(k, false); err != nil {
				return err
			}
		}
		if r.heldPointer {
			r.heldPointer = false
			return r.pointer(0)
		}
		return nil
	}
	return errors.New("unknown input")
}

func (s *videoServer) attachInput(ctx context.Context, dc *webrtc.DataChannel) {
	if dc.Label() != "desktop-input" || !dc.Ordered() {
		dc.Close()
		return
	}
	var mu sync.Mutex
	var input *rfbInput
	closeInput := func() {
		mu.Lock()
		defer mu.Unlock()
		if input != nil {
			_ = input.apply(inputEvent{Kind: "reset"})
			input.Close()
			input = nil
			s.gate.Unlock()
		}
	}
	dc.OnOpen(func() {
		mu.Lock()
		defer mu.Unlock()
		if ctx.Err() != nil || !s.gate.TryLock() {
			_ = dc.SendText("busy")
			return
		}
		var err error
		input, err = openInput(ctx)
		if err != nil {
			s.gate.Unlock()
			_ = dc.SendText("failed")
			return
		}
		_ = dc.SendText("ready")
	})
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		mu.Lock()
		if input == nil {
			mu.Unlock()
			return
		}
		var e inputEvent
		bad := ctx.Err() != nil || !msg.IsString || len(msg.Data) > 256 || json.Unmarshal(msg.Data, &e) != nil || input.apply(e) != nil
		mu.Unlock()
		if bad {
			closeInput()
			_ = dc.SendText("failed")
			_ = dc.Close()
		}
	})
	dc.OnClose(closeInput)
	go func() { <-ctx.Done(); closeInput() }()
}
