package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"bqbot/internal/computer"
	"bqbot/internal/driver"
	"bqbot/internal/mcpgateway"
)

func (h *Harness) prepareComputer(t *turn, g *externalGroup) error {
	configs, e := h.Store.Computers()
	if e != nil {
		return e
	}
	for _, config := range configs {
		if config.BotID != t.bot.ID || !config.Enabled {
			continue
		}
		if e := config.Validate(); e != nil {
			return e
		}
		tool := mcpgateway.Tool{Name: "computer", Description: "Use this bot's configured desktop. Each action requires the normal tool approval. screenshot captures the screen; click uses pixel coordinates; type inserts text; key accepts Return, Tab, Escape, BackSpace, arrows, Home, End, Page_Up, Page_Down, space; scroll accepts up/down and screenshot x/y coordinates.", InputSchema: json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string","enum":["screenshot","click","type","key","scroll"]},"x":{"type":"integer","minimum":0,"maximum":32767},"y":{"type":"integer","minimum":0,"maximum":32767},"text":{"type":"string","maxLength":8192},"key":{"type":"string"},"direction":{"type":"string","enum":["up","down"]}},"required":["kind"],"additionalProperties":false}`)}
		bindHost := h.Host.BindSession()
		g.tools["external_computer"] = externalTool{tool: tool, invoke: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
			// Configuration is read after approval. Disabling or changing the desktop
			// while a tool is awaiting permission cannot grant access to a different one.
			current, e := h.Store.Computer(t.bot.ID)
			if e != nil {
				return nil, e
			}
			if current != config {
				return nil, errors.New("computer configuration changed; retry in a new turn")
			}
			var action computer.Action
			dec := json.NewDecoder(bytes.NewReader(args))
			dec.DisallowUnknownFields()
			if e := dec.Decode(&action); e != nil {
				return nil, errors.New("invalid computer action")
			}
			if dec.Decode(new(any)) != io.EOF {
				return nil, errors.New("invalid computer action")
			}
			out, e := computer.ExecuteWithHost(bindHost(ctx), current, action, h.Host)
			if e != nil {
				return nil, e
			}
			if out.Image == nil {
				return json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": "Desktop action completed."}}})
			}
			h.mu.Lock()
			if t.ctx.Err() != nil {
				h.mu.Unlock()
				return nil, errors.New("turn stopped")
			}
			// Bound persisted screenshot volume below the transcript line limit.
			// Every capture still returns to the requesting engine.
			images := append([]driver.Attachment{}, t.reply.Attachments...)
			images = append(images, *out.Image)
			bytes := 0
			for i := len(images) - 1; i >= 0; i-- {
				bytes += len(images[i].Data)
				if bytes > 12<<20 || len(images)-i > 4 {
					images = images[i+1:]
					break
				}
			}
			t.reply.Attachments = images
			t.output = true
			e = h.publishLocked(t, Update{BotID: t.bot.ID, Reply: clone(t.reply)})
			h.mu.Unlock()
			if e != nil {
				return nil, e
			}
			return json.Marshal(map[string]any{"content": []map[string]string{{"type": "image", "mimeType": out.Image.MIME, "data": out.Image.Data}}})
		}}
	}
	return nil
}
