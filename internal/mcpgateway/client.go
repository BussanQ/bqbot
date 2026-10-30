// Package mcpgateway implements turn-scoped MCP clients for custom servers.
package mcpgateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Config is resolved by the harness. Headers and Env are never serialized to UI.
type Config struct {
	Transport, Command, URL string
	Args, Env               []string
	Headers                 map[string]string
}
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations struct {
		ReadOnly bool `json:"readOnlyHint"`
	} `json:"annotations,omitempty"`
}
type packet struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code int `json:"code"`
	} `json:"error"`
	Method string `json:"method"`
}

// Client owns its subprocess/HTTP event stream for one turn.
type Client struct {
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	writeMu  sync.Mutex
	pending  map[string]chan packet
	seq      atomic.Uint64
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	http     *http.Client
	config   Config
	endpoint string
	session  string
}

func Open(parent context.Context, config Config) (*Client, error) {
	ctx, cancel := context.WithCancel(parent)
	c := &Client{ctx: ctx, cancel: cancel, pending: map[string]chan packet{}, config: config, endpoint: config.URL, http: &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	var err error
	switch config.Transport {
	case "stdio":
		err = c.startCommand()
	case "http":
	case "sse":
		err = c.startSSE()
	default:
		err = errors.New("unsupported MCP transport")
	}
	if err != nil {
		c.Close()
		return nil, err
	}
	initCtx, stop := context.WithTimeout(ctx, 20*time.Second)
	defer stop()
	var initialized struct {
		Protocol string `json:"protocolVersion"`
	}
	err = c.Request(initCtx, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "bqbot", "version": "1"}}, &initialized)
	if err == nil {
		err = c.notify(initCtx, "notifications/initialized", map[string]any{})
	}
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("MCP initialization: %w", err)
	}
	return c, nil
}

// Done is closed when the connection or its subprocess ends.
func (c *Client) Done() <-chan struct{} { return c.ctx.Done() }

func (c *Client) Close() {
	c.cancel()
	if c.stdin != nil {
		c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	if c.http != nil {
		c.http.CloseIdleConnections()
	}
}
func (c *Client) startCommand() error {
	cmd := exec.CommandContext(c.ctx, c.config.Command, c.config.Args...)
	cmd.Env = c.config.Env
	cmd.WaitDelay = time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return errors.New("cannot open MCP stdout")
	}
	c.stdin, err = cmd.StdinPipe()
	if err != nil {
		return errors.New("cannot open MCP stdin")
	}
	// A server may print credentials to stderr. Do not copy it into transcripts.
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		return errors.New("cannot start MCP command")
	}
	c.cmd = cmd
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 65536), 16<<20)
		for sc.Scan() {
			c.deliver(sc.Bytes())
		}
		_ = cmd.Wait()
		c.cancel()
	}()
	return nil
}
func (c *Client) deliver(data []byte) {
	var p packet
	if json.Unmarshal(data, &p) != nil {
		return
	}
	if p.Method != "" {
		// Sampling and elicitation are intentionally unavailable to external servers.
		if len(p.ID) > 0 {
			go func() {
				_ = c.send(c.ctx, map[string]any{"jsonrpc": "2.0", "id": p.ID, "error": map[string]any{"code": -32601, "message": "client requests are unavailable"}})
			}()
		}
		return
	}
	c.mu.Lock()
	ch := c.pending[string(p.ID)]
	c.mu.Unlock()
	if ch != nil {
		select {
		case ch <- p:
		default:
		}
	}
}
func (c *Client) Request(ctx context.Context, method string, params, out any) error {
	id := fmt.Sprint(c.seq.Add(1))
	ch := make(chan packet, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	if err := c.send(ctx, map[string]any{"jsonrpc": "2.0", "id": json.Number(id), "method": method, "params": params}); err != nil {
		return err
	}
	select {
	case p := <-ch:
		if p.Error != nil {
			return fmt.Errorf("MCP request failed (code %d)", p.Error.Code)
		}
		if out != nil {
			if err := json.Unmarshal(p.Result, out); err != nil {
				return errors.New("invalid MCP result")
			}
		}
		return nil
	case <-ctx.Done():
		// Use the independent session context to deliver cancellation after the call context ends.
		go func() {
			cancelCtx, stop := context.WithTimeout(c.ctx, time.Second)
			defer stop()
			_ = c.notify(cancelCtx, "notifications/cancelled", map[string]any{"requestId": json.Number(id)})
		}()
		return ctx.Err()
	case <-c.ctx.Done():
		return errors.New("MCP connection closed")
	}
}
func (c *Client) notify(ctx context.Context, method string, params any) error {
	return c.send(ctx, map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}
func (c *Client) send(ctx context.Context, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if c.config.Transport == "stdio" {
		c.writeMu.Lock()
		defer c.writeMu.Unlock()
		if c.ctx.Err() != nil {
			return errors.New("MCP connection closed")
		}
		_, err = c.stdin.Write(append(data, '\n'))
		if err != nil {
			return errors.New("MCP write failed")
		}
		return nil
	}
	c.mu.Lock()
	endpoint := c.endpoint
	c.mu.Unlock()
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(data))
	if err != nil {
		return errors.New("invalid MCP endpoint")
	}
	c.headers(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	c.mu.Lock()
	session := c.session
	c.mu.Unlock()
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return errors.New("MCP HTTP request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("MCP returned HTTP %d", resp.StatusCode)
	}
	if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
		c.mu.Lock()
		c.session = s
		c.mu.Unlock()
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		var request packet
		_ = json.Unmarshal(data, &request)
		answered := errors.New("MCP response received")
		err := readSSE(io.LimitReader(resp.Body, 16<<20), func(event, data string) error {
			c.deliver([]byte(data))
			var response packet
			if json.Unmarshal([]byte(data), &response) == nil && len(request.ID) > 0 && string(request.ID) == string(response.ID) && response.Method == "" {
				return answered
			}
			return nil
		})
		if errors.Is(err, answered) {
			return nil
		}
		return err
	}
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return errors.New("cannot read MCP response")
	}
	if len(bytes.TrimSpace(data)) > 0 {
		c.deliver(data)
	}
	return nil
}
func (c *Client) headers(r *http.Request) {
	for k, v := range c.config.Headers {
		r.Header.Set(k, v)
	}
	r.Header.Set("MCP-Protocol-Version", "2025-06-18")
}
func (c *Client) startSSE() error {
	req, err := http.NewRequestWithContext(c.ctx, "GET", c.config.URL, nil)
	if err != nil {
		return errors.New("invalid MCP SSE URL")
	}
	c.headers(req)
	req.Header.Set("Accept", "text/event-stream")
	startCtx, stop := context.WithTimeout(c.ctx, 20*time.Second)
	endpoint := make(chan error, 1)
	go func() {
		resp, e := c.http.Do(req)
		if e != nil {
			endpoint <- errors.New("MCP SSE connection failed")
			c.cancel()
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			endpoint <- fmt.Errorf("MCP SSE returned HTTP %d", resp.StatusCode)
			c.cancel()
			return
		}
		sent := false
		e = readSSE(resp.Body, func(event, data string) error {
			if event == "endpoint" {
				base, _ := url.Parse(c.config.URL)
				target, err := base.Parse(data)
				if err != nil || target.Scheme != base.Scheme || target.Host != base.Host || target.User != nil {
					return errors.New("MCP SSE endpoint must share its origin")
				}
				if sent {
					return nil
				}
				c.mu.Lock()
				c.endpoint = target.String()
				c.mu.Unlock()
				sent = true
				endpoint <- nil
			} else if event == "message" || event == "" {
				c.deliver([]byte(data))
			}
			return nil
		})
		if !sent {
			if e == nil {
				e = errors.New("MCP SSE ended before endpoint")
			}
			endpoint <- e
		}
		c.cancel()
	}()
	defer stop()
	select {
	case err := <-endpoint:
		return err
	case <-startCtx.Done():
		return errors.New("MCP SSE endpoint timed out")
	}
}
func readSSE(r io.Reader, fn func(string, string) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 65536), 16<<20)
	event := ""
	var data []string
	dispatch := func() error {
		if len(data) == 0 {
			return nil
		}
		err := fn(event, strings.Join(data, "\n"))
		event = ""
		data = nil
		return err
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return dispatch()
}
func (c *Client) Tools(ctx context.Context) ([]Tool, error) {
	var tools []Tool
	cursor := ""
	for page := 0; page < 20; page++ {
		var result struct {
			Tools []Tool `json:"tools"`
			Next  string `json:"nextCursor"`
		}
		params := map[string]string{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if err := c.Request(ctx, "tools/list", params, &result); err != nil {
			return nil, err
		}
		tools = append(tools, result.Tools...)
		if len(tools) > 1000 {
			return nil, errors.New("too many MCP tools")
		}
		if result.Next == "" {
			return tools, nil
		}
		if result.Next == cursor {
			return nil, errors.New("MCP pagination cycle")
		}
		cursor = result.Next
	}
	return nil, errors.New("too many MCP tool pages")
}
func (c *Client) Call(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	var result json.RawMessage
	err := c.Request(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, &result)
	return result, err
}
