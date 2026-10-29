package driver

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// Echo is an offline engine that repeats the message back, word by word:
// for trying the app without an agent CLI, and for tests.
type Echo struct {
	Delay time.Duration // between words
	n     atomic.Int64
}

func (d *Echo) Name() string   { return "echo" }
func (d *Echo) Binary() string { return "" }

func (d *Echo) Run(ctx context.Context, in TurnInput, emit func(Event)) error {
	cursor := in.Cursor
	if cursor == "" {
		cursor = fmt.Sprintf("echo-%d", d.n.Add(1))
	}
	emit(Event{Kind: EvSession, Cursor: cursor})
	words := strings.Fields("Echo: " + in.Text)
	for i, w := range words {
		if d.Delay > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d.Delay):
			}
		} else if err := ctx.Err(); err != nil {
			return err
		}
		if i > 0 {
			w = " " + w
		}
		emit(Event{Kind: EvText, Text: w})
	}
	return nil
}
