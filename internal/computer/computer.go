// Package computer connects existing Boat desktops and Cua MCP desktops.
// It never provisions a billable machine or implicitly enables host access.
package computer

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"bqbot/internal/driver"
	"bqbot/internal/mcpgateway"
	"bqbot/internal/store"
)

const maxImage = 8 << 20

// Action is a deliberately small set of desktop interactions; no shell is exposed.
type Action struct {
	Kind      string `json:"kind"`
	X         int    `json:"x,omitempty"`
	Y         int    `json:"y,omitempty"`
	Text      string `json:"text,omitempty"`
	Key       string `json:"key,omitempty"`
	Direction string `json:"direction,omitempty"`
}

func (a Action) Validate() error {
	switch a.Kind {
	case "screenshot":
	case "click":
		if a.X < 0 || a.X > 32767 || a.Y < 0 || a.Y > 32767 {
			return errors.New("click coordinates are out of range")
		}
	case "type":
		if a.Text == "" || len(a.Text) > 8192 || strings.ContainsRune(a.Text, 0) {
			return errors.New("text must contain 1–8192 bytes without NUL")
		}
	case "key":
		switch a.Key {
		case "Return", "Tab", "Escape", "BackSpace", "Up", "Down", "Left", "Right", "Home", "End", "Page_Up", "Page_Down", "space":
		default:
			return errors.New("unsupported key")
		}
	case "scroll":
		if a.Direction != "up" && a.Direction != "down" {
			return errors.New("scroll must be up or down")
		}
	default:
		return errors.New("unsupported computer action")
	}
	return nil
}

type Result struct {
	Image  *driver.Attachment `json:"image,omitempty"`
	Width  int                `json:"width,omitempty"`
	Height int                `json:"height,omitempty"`
}

// Execute assumes the caller has authorized this individual action. Host consent
// and connection enablement are checked again here, including for screenshots.
func Execute(ctx context.Context, c store.ComputerConfig, a Action) (Result, error) {
	if e := c.Validate(); e != nil {
		return Result{}, e
	}
	if !c.Enabled || c.Provider == "host" && !c.HostConsent {
		return Result{}, errors.New("computer access is disabled")
	}
	if e := a.Validate(); e != nil {
		return Result{}, e
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	token := ""
	if c.TokenEnv != "" {
		token = os.Getenv(c.TokenEnv)
		if token == "" || strings.ContainsAny(token, "\r\n") {
			return Result{}, errors.New("computer token environment variable is missing or invalid")
		}
	}
	if c.Provider == "boat" {
		return boat(ctx, c, token, a)
	}
	return cua(ctx, c, token, a)
}
func screenshot(data []byte) (Result, error) {
	if len(data) == 0 || len(data) > maxImage {
		return Result{}, errors.New("screenshot exceeds the 8 MiB limit or is empty")
	}
	config, format, e := image.DecodeConfig(bytes.NewReader(data))
	if e != nil || (format != "png" && format != "jpeg") || config.Width < 1 || config.Height < 1 || config.Width > 16384 || config.Height > 16384 || int64(config.Width)*int64(config.Height) > 64<<20 {
		return Result{}, errors.New("invalid screenshot image")
	}
	return Result{Image: &driver.Attachment{Name: "computer-" + time.Now().UTC().Format("20060102-150405") + "." + format, MIME: "image/" + format, Data: base64.StdEncoding.EncodeToString(data)}, Width: config.Width, Height: config.Height}, nil
}
func request(ctx context.Context, c store.ComputerConfig, token, method, path string, body any, max int64) ([]byte, error) {
	var input io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return nil, e
		}
		input = bytes.NewReader(b)
	}
	r, e := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+path, input)
	if e != nil {
		return nil, errors.New("invalid computer request")
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	resp, e := client.Do(r)
	if e != nil {
		return nil, errors.New("computer connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("computer service returned HTTP %d", resp.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if e != nil || int64(len(b)) > max {
		return nil, errors.New("computer response is invalid or too large")
	}
	return b, nil
}
func quote(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'" }
func boat(ctx context.Context, c store.ComputerConfig, token string, a Action) (Result, error) {
	prefix := "/sandboxes/" + url.PathEscape(c.SandboxID)
	command := "export DISPLAY=${DISPLAY:-:0}; "
	file := "/tmp/bqbot-" + store.NewID() + ".png"
	switch a.Kind {
	case "screenshot":
		command += "scrot -o " + quote(file)
	case "click":
		command += "xdotool mousemove --sync " + strconv.Itoa(a.X) + " " + strconv.Itoa(a.Y) + " click 1"
	case "type":
		command += "xdotool type --clearmodifiers -- " + quote(a.Text)
	case "key":
		command += "xdotool key --clearmodifiers " + quote(a.Key)
	case "scroll":
		button := "4"
		if a.Direction == "down" {
			button = "5"
		}
		command += "xdotool click --repeat 3 " + button
	}
	b, e := request(ctx, c, token, "POST", prefix+"/commands", map[string]any{"command": command, "timeoutSeconds": 25}, 64<<10)
	if e != nil {
		return Result{}, e
	}
	var reply struct {
		Success  bool `json:"success"`
		ExitCode *int `json:"exitCode"`
	}
	if json.Unmarshal(b, &reply) != nil || !reply.Success || reply.ExitCode == nil || *reply.ExitCode != 0 {
		return Result{}, errors.New("Boat desktop command failed; check desktop tools and sandbox state")
	}
	if a.Kind != "screenshot" {
		return Result{}, nil
	}
	// The temporary frame name is unique, so concurrent previews cannot mix frames.
	defer func() {
		clean, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_, _ = request(clean, c, token, "POST", prefix+"/commands", map[string]any{"command": "rm -f -- " + quote(file), "timeoutSeconds": 2}, 64<<10)
	}()
	b, e = request(ctx, c, token, "GET", prefix+"/artifacts?path="+url.QueryEscape(file), nil, maxImage)
	if e != nil {
		return Result{}, e
	}
	return screenshot(b)
}
func cua(ctx context.Context, c store.ComputerConfig, token string, a Action) (Result, error) {
	headers := map[string]string{}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	client, e := mcpgateway.Open(ctx, mcpgateway.Config{Transport: "http", URL: c.URL, Headers: headers})
	if e != nil {
		return Result{}, errors.New("cannot initialize Cua MCP connection")
	}
	defer client.Close()
	name := ""
	args := map[string]any{"scope": "desktop", "delivery_mode": "foreground"}
	switch a.Kind {
	case "screenshot":
		name = "get_desktop_state"
		args = map[string]any{}
	case "click":
		name = "click"
		args["x"] = a.X
		args["y"] = a.Y
		args["button"] = "left"
	case "type":
		name = "type_text"
		args["text"] = a.Text
	case "key":
		name = "press_key"
		args["key"] = a.Key
	case "scroll":
		name = "scroll"
		args["direction"] = a.Direction
		args["amount"] = 3
	}
	input, _ := json.Marshal(args)
	raw, e := client.Call(ctx, name, input)
	if e != nil {
		return Result{}, errors.New("Cua desktop action failed")
	}
	var result struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Data string `json:"data"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &result) != nil || result.IsError {
		return Result{}, errors.New("Cua desktop action was rejected")
	}
	if a.Kind != "screenshot" {
		return Result{}, nil
	}
	for _, v := range result.Content {
		if v.Type == "image" {
			if len(v.Data) > base64.StdEncoding.EncodedLen(maxImage) {
				return Result{}, errors.New("Cua screenshot is too large")
			}
			data, e := base64.StdEncoding.DecodeString(v.Data)
			if e != nil {
				return Result{}, errors.New("invalid Cua screenshot")
			}
			return screenshot(data)
		}
	}
	return Result{}, errors.New("Cua did not return an MCP image; enable desktop capture and check screen recording permissions")
}
