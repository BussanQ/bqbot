package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"bqbot/internal/driver"
	"bqbot/internal/harness"
	"bqbot/internal/store"
)

// Client speaks the local service protocol. It is safe for concurrent use.
type Client struct {
	url     string
	token   string
	http    *http.Client
	initErr error
}

// NewClient creates a client that never uses proxies or follows redirects.
// Invalid/non-loopback URLs are reported when a method is called.
func NewClient(baseURL, token string) *Client {
	c := &Client{url: strings.TrimRight(baseURL, "/"), token: token}
	u, err := url.Parse(c.url)
	if err != nil || u == nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		c.initErr = errors.New("invalid loopback service URL")
	} else if ip := net.ParseIP(u.Hostname()); ip == nil || !ip.IsLoopback() {
		c.initErr = errors.New("service URL must use a literal loopback address")
	}
	if token == "" || strings.ContainsAny(token, "\r\n") {
		c.initErr = errors.New("invalid service token")
	}
	c.http = &http.Client{
		Transport:     &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ResponseHeaderTimeout: 15 * time.Second, IdleConnTimeout: 60 * time.Second, MaxIdleConnsPerHost: 8},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return c
}

// HTTPError preserves the HTTP status for callers that need to distinguish
// authentication, missing bots, validation errors, and conflicting approvals.
type HTTPError struct {
	StatusCode int
	Message    string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("service HTTP %d: %s", e.StatusCode, e.Message)
}

func (c *Client) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	if c.initErr != nil {
		return nil, c.initErr
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		if len(data) > maxRequestBytes {
			return nil, errors.New("request is too large")
		}
		reader = bytes.NewReader(data)
	}
	r, err := http.NewRequestWithContext(ctx, method, c.url+path, reader)
	if err != nil {
		return nil, errors.New("invalid service request")
	}
	r.Header.Set("Authorization", "Bearer "+c.token)
	r.Header.Set("Accept", "application/json")
	if path == "/v1/events" {
		r.Header.Set("Accept", "text/event-stream")
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(r)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		var payload struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&payload)
		if payload.Error == "" {
			payload.Error = http.StatusText(resp.StatusCode)
		}
		return nil, &HTTPError{resp.StatusCode, payload.Error}
	}
	return resp, nil
}

const maxResponseBytes = 64 << 20

func (c *Client) do(method, path string, body, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	resp, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxResponseBytes {
		return errors.New("service response is too large")
	}
	if out == nil {
		out = &struct{}{}
	}
	return json.Unmarshal(data, out)
}

func botPath(id string) string { return "/v1/bots/" + url.PathEscape(id) }

// Bots lists the stored bots in creation order.
func (c *Client) Bots() (v []store.Bot, err error) { err = c.do("GET", "/v1/bots", nil, &v); return }

// SaveBot creates or updates a bot after validating its engine configuration.
func (c *Client) SaveBot(v store.Bot) error { return c.do("PUT", "/v1/bots", v, nil) }

// DeleteBot delegates removal and turn cancellation to the harness.
func (c *Client) DeleteBot(id string) error { return c.do("DELETE", botPath(id), nil, nil) }

// Transcript retrieves the persisted main conversation, oldest first.
func (c *Client) Transcript(id string) (v []store.Message, err error) {
	err = c.do("GET", botPath(id)+"/transcript", nil, &v)
	return
}

// HandoffTranscript retrieves the separate target/asker conversation.
func (c *Client) HandoffTranscript(botID, askerID string) (v []store.Message, err error) {
	err = c.do("GET", botPath(botID)+"/handoffs/"+url.PathEscape(askerID), nil, &v)
	return
}

// WorkDir returns the bot's local working directory, creating it if necessary.
func (c *Client) WorkDir(id string) (v string, err error) {
	err = c.do("GET", botPath(id)+"/workdir", nil, &v)
	return
}

// Send submits a message and returns its recorded user entry. Replies arrive
// through Watch. Mode is empty/default, queue, or steer.
func (c *Client) Send(botID, text string, attachments []driver.Attachment, mode string) (v store.Message, err error) {
	err = c.do("POST", botPath(botID)+"/send", sendRequest{text, attachments, mode}, &v)
	return
}

// Stop asks the harness to cancel the bot's work.
func (c *Client) Stop(id string) error { return c.do("POST", botPath(id)+"/stop", struct{}{}, nil) }

// Approve resolves a pending approval once; stale or duplicate decisions conflict.
func (c *Client) Approve(botID, id string, d driver.ApprovalDecision) error {
	return c.do("POST", botPath(botID)+"/approvals/"+url.PathEscape(id), d, nil)
}

// Settings returns engine configuration with environment variable names only.
func (c *Client) Settings() (v map[string]driver.EngineSettings, err error) {
	err = c.do("GET", "/v1/settings", nil, &v)
	return
}

// SaveSettings replaces the stored engine settings after validation.
func (c *Client) SaveSettings(v map[string]driver.EngineSettings) error {
	return c.do("PUT", "/v1/settings", v, nil)
}

// Engines probes registered engines and returns their configured model catalogs.
func (c *Client) Engines() (v []Engine, err error) { err = c.do("GET", "/v1/engines", nil, &v); return }

// Snapshot returns the harness's current runtime state for reconciliation.
func (c *Client) Snapshot() ([]harness.Update, error) {
	var data json.RawMessage
	if err := c.do("GET", "/v1/snapshot", nil, &data); err != nil {
		return nil, err
	}
	return decodeUpdates(data)
}

func decodeUpdate(data []byte) (harness.Update, error) {
	var u harness.Update
	if err := json.Unmarshal(data, &u); err != nil {
		return u, err
	}
	var message struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(data, &message); err != nil {
		return u, err
	}
	if message.Error != "" {
		u.Err = errors.New(message.Error)
	}
	return u, nil
}
func decodeUpdates(data []byte) ([]harness.Update, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	updates := make([]harness.Update, 0, len(raw))
	for _, data := range raw {
		u, err := decodeUpdate(data)
		if err != nil {
			return nil, err
		}
		updates = append(updates, u)
	}
	return updates, nil
}

// Watch delivers updates until ctx is canceled. It reconnects after transport
// failures with bounded backoff; each connection receives a fresh snapshot.
// Callbacks are serialized and may see repeated complete updates, so consumers
// must replace messages by ID rather than append text. Snapshot reconciliation
// is state recovery, not a historical event log; Transcript retrieves history.
// Authentication and other non-retryable HTTP errors are returned immediately.
func (c *Client) Watch(ctx context.Context, onUpdate func(harness.Update)) error {
	if onUpdate == nil {
		return errors.New("update callback is required")
	}
	backoff := 250 * time.Millisecond
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		started := time.Now()
		err := c.watchOnce(ctx, onUpdate)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if c.initErr != nil {
			return c.initErr
		}
		var httpErr *HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode < 500 && httpErr.StatusCode != http.StatusTooManyRequests {
			return err
		}
		var protocolErr *streamError
		if errors.As(err, &protocolErr) {
			return err
		}
		if time.Since(started) > 5*time.Second {
			backoff = 250 * time.Millisecond
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if backoff < 5*time.Second {
			backoff *= 2
			if backoff > 5*time.Second {
				backoff = 5 * time.Second
			}
		}
	}
}

type streamError struct{ err error }

func (e *streamError) Error() string { return "invalid service event stream: " + e.err.Error() }
func (c *Client) watchOnce(ctx context.Context, onUpdate func(harness.Update)) error {
	resp, err := c.request(ctx, "GET", "/v1/events", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if strings.Split(resp.Header.Get("Content-Type"), ";")[0] != "text/event-stream" {
		return &streamError{errors.New("expected text/event-stream")}
	}
	// Heartbeats keep the stream live without retransmitting image transcripts.
	// Reconnect if the peer stops sending bytes.
	timer := time.AfterFunc(15*time.Second, func() { resp.Body.Close() })
	defer timer.Stop()
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), maxResponseBytes)
	kind := ""
	var data []byte
	for scanner.Scan() {
		timer.Reset(15 * time.Second)
		line := scanner.Text()
		if line == "" {
			if len(data) > 0 {
				switch kind {
				case "update":
					u, e := decodeUpdate(data)
					if e != nil {
						return &streamError{e}
					}
					if ctx.Err() != nil {
						return ctx.Err()
					}
					onUpdate(u)
				case "snapshot":
					updates, e := decodeUpdates(data)
					if e != nil {
						return &streamError{e}
					}
					for _, u := range updates {
						if ctx.Err() != nil {
							return ctx.Err()
						}
						onUpdate(u)
					}
				default:
					return &streamError{errors.New("unknown event type")}
				}
			}
			kind = ""
			data = nil
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			kind = value
		case "data":
			if len(data)+len(value)+1 > maxResponseBytes {
				return &streamError{errors.New("frame is too large")}
			}
			data = append(data, value...)
			data = append(data, '\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}
