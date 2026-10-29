package driver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// OpenAI is a stateless OpenAI-compatible chat completions streaming driver.
// Each turn replays History; API keys are read from the configured environment variable.
type OpenAI struct{ Client *http.Client }

// API is the OpenAI-compatible driver.
type API = OpenAI

func (*OpenAI) Name() string   { return "openai" }
func (*OpenAI) Binary() string { return "" }

func imageDataURL(a Attachment) (string, error) {
	switch a.MIME {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return "", fmt.Errorf("unsupported image MIME type %q", a.MIME)
	}
	if a.Data == "" {
		return "", fmt.Errorf("empty image attachment")
	}
	if _, err := base64.StdEncoding.DecodeString(a.Data); err != nil {
		return "", fmt.Errorf("invalid base64 image: %w", err)
	}
	return "data:" + a.MIME + ";base64," + a.Data, nil
}

func apiMessage(role, text string, images []Attachment) (map[string]any, error) {
	if len(images) == 0 {
		return map[string]any{"role": role, "content": text}, nil
	}
	content := []any{map[string]any{"type": "text", "text": text}}
	for _, a := range images {
		data, err := imageDataURL(a)
		if err != nil {
			return nil, err
		}
		content = append(content, map[string]any{"type": "image_url", "image_url": map[string]string{"url": data}})
	}
	return map[string]any{"role": role, "content": content}, nil
}

func (d *OpenAI) Run(ctx context.Context, in TurnInput, emit func(Event)) error {
	base := strings.TrimRight(in.Settings.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return fmt.Errorf("invalid API base URL")
	}
	if !strings.HasSuffix(u.Path, "/chat/completions") {
		u.Path = strings.TrimRight(u.Path, "/") + "/chat/completions"
	}
	keyEnv := in.Settings.APIKeyEnv
	if keyEnv == "" {
		keyEnv = "OPENAI_API_KEY"
	}
	key := os.Getenv(keyEnv)
	if in.Settings.APIKeyEnv != "" && key == "" {
		return fmt.Errorf("API key environment variable %s is empty", keyEnv)
	}
	messages := make([]any, 0, len(in.History)+2)
	if in.System != "" {
		messages = append(messages, map[string]string{"role": "system", "content": in.System})
	}
	for _, h := range in.History {
		if h.Role != "user" && h.Role != "assistant" {
			continue
		}
		msg, err := apiMessage(h.Role, h.Text, h.Attachments)
		if err != nil {
			return err
		}
		messages = append(messages, msg)
	}
	msg, err := apiMessage("user", in.Text, in.Attachments)
	if err != nil {
		return err
	}
	messages = append(messages, msg)
	model := in.Model
	if model == "" {
		model = "gpt-4.1"
		if models := Catalog("api", in.Settings); len(models) != 0 {
			model = models[0].ID
		}
	}
	body := map[string]any{"model": model, "messages": messages, "stream": true}
	if in.Effort != "" {
		body["reasoning_effort"] = in.Effort
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	client := d.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("API request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Do not echo provider bodies, which can contain credentials or request contents.
		return fmt.Errorf("%w: API HTTP %d", ErrTurnFailed, resp.StatusCode)
	}
	reader := bufio.NewReader(resp.Body)
	var data []string
	finished := false
	consume := func() error {
		if len(data) == 0 {
			return nil
		}
		payload := strings.Join(data, "\n")
		data = nil
		if payload == "[DONE]" {
			finished = true
			return nil
		}
		var chunk struct {
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				Delta struct {
					Content   string          `json:"content"`
					ToolCalls json.RawMessage `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return fmt.Errorf("%w: invalid API stream JSON", ErrTurnFailed)
		}
		if len(chunk.Error) != 0 && string(chunk.Error) != "null" {
			return fmt.Errorf("%w: API stream error", ErrTurnFailed)
		}
		if len(chunk.Choices) == 0 {
			return nil
		}
		c := chunk.Choices[0]
		if len(c.Delta.ToolCalls) != 0 && string(c.Delta.ToolCalls) != "null" && string(c.Delta.ToolCalls) != "[]" {
			return fmt.Errorf("%w: API tool calls are unsupported", ErrTurnFailed)
		}
		if c.Delta.Content != "" {
			emit(Event{Kind: EvText, Text: c.Delta.Content})
		}
		if c.FinishReason != nil && *c.FinishReason != "" {
			if *c.FinishReason != "stop" {
				return fmt.Errorf("%w: API finish reason %s", ErrTurnFailed, *c.FinishReason)
			}
			finished = true
		}
		return nil
	}
	for {
		line, readErr := reader.ReadString('\n')
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			if err := consume(); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if finished {
				return nil
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
		if readErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := consume(); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if readErr != io.EOF {
				return fmt.Errorf("API stream: %w", readErr)
			}
			if !finished {
				return fmt.Errorf("%w: API stream ended before completion", ErrTurnFailed)
			}
			return nil
		}
	}
}
