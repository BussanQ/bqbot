// Package mcp is the bqbot stdio MCP server (`bqbot mcp <grant file>`) that
// an engine's CLI starts for a bot's turn. Its tools, list_bots and ask_bot,
// call back into the running app over the loopback endpoint the grant file
// names, with the turn's token from that file. The file keeps the token off
// argv and out of the engines' configs.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Grant is the content of the grant file the app writes (mode 0600) for a
// turn and removes when the turn ends.
type Grant struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// Server is the server's name in the engines' configs; tools reach the model
// as mcp__bqbot__<tool>.
const Server = "bqbot"

// Tool names.
const (
	ToolList = "list_bots"
	ToolAsk  = "ask_bot"
)

// Tools are the server's tool names.
var Tools = []string{ToolAsk, ToolList}

// Backend answers the tools.
type Backend interface {
	ListBots(ctx context.Context) (string, error)
	AskBot(ctx context.Context, name, question string) (string, error)
}

// Main serves stdin and stdout for the turn whose grant file is args[0].
func Main(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: bqbot mcp <grant file>; bqbot starts this server for a bot's turn")
	}
	b, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	var g Grant
	if err := json.Unmarshal(b, &g); err != nil || g.URL == "" || g.Token == "" {
		return fmt.Errorf("%s is not a bqbot grant file", args[0])
	}
	return Serve(context.Background(), os.Stdin, os.Stdout, &HTTPBackend{URL: g.URL, Token: g.Token})
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

const protocolVersion = "2025-06-18"

var toolList = map[string]any{"tools": []any{
	map[string]any{
		"name": ToolList,
		"description": "List the person's other bots in bqbot, with what each is for and whether it is busy. " +
			"Use it to pick whom to ask with ask_bot.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		"annotations": map[string]any{"readOnlyHint": true},
	},
	map[string]any{
		"name": ToolAsk,
		"description": "Hand a question to another of the person's bots and wait for its answer, which comes back to you " +
			"(not to the person). The bot answers in its own conversation; it cannot ask other bots in turn. " +
			"Fails if that bot is busy.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":     map[string]any{"type": "string", "description": "The bot's name, as list_bots shows it"},
				"question": map[string]any{"type": "string", "description": "The question, with the context the bot needs: it does not see your conversation"},
			},
			"required": []string{"name", "question"},
		},
	},
}}

// Serve speaks MCP over newline-delimited JSON-RPC on r and w until r ends.
// Tool calls run concurrently and stop on notifications/cancelled.
func Serve(ctx context.Context, r io.Reader, w io.Writer, b Backend) error {
	ctx, cancelAll := context.WithCancel(ctx)
	defer cancelAll()
	var (
		wmu     sync.Mutex
		cmu     sync.Mutex
		calls   = map[string]context.CancelFunc{}
		pending sync.WaitGroup
	)
	reply := func(resp response) {
		resp.JSONRPC = "2.0"
		line, err := json.Marshal(resp)
		if err != nil {
			return
		}
		wmu.Lock()
		w.Write(append(line, '\n'))
		wmu.Unlock()
	}
	defer pending.Wait()

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			reply(response{ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
			continue
		}
		if len(req.ID) == 0 { // a notification
			if req.Method == "notifications/cancelled" {
				var p struct {
					RequestID json.RawMessage `json:"requestId"`
				}
				json.Unmarshal(req.Params, &p)
				cmu.Lock()
				if cancel := calls[string(p.RequestID)]; cancel != nil {
					cancel()
				}
				cmu.Unlock()
			}
			continue
		}
		switch req.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			json.Unmarshal(req.Params, &p)
			v := p.ProtocolVersion
			if v == "" {
				v = protocolVersion
			}
			reply(response{ID: req.ID, Result: map[string]any{
				"protocolVersion": v,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": Server, "version": "1"},
			}})
		case "ping":
			reply(response{ID: req.ID, Result: map[string]any{}})
		case "tools/list":
			reply(response{ID: req.ID, Result: toolList})
		case "tools/call":
			callCtx, cancel := context.WithCancel(ctx)
			key := string(req.ID)
			cmu.Lock()
			calls[key] = cancel
			cmu.Unlock()
			pending.Add(1)
			go func() {
				defer pending.Done()
				res := call(callCtx, b, req.Params)
				cmu.Lock()
				delete(calls, key)
				cmu.Unlock()
				cancel()
				reply(response{ID: req.ID, Result: res})
			}()
		default:
			reply(response{ID: req.ID, Error: &rpcError{-32601, "method not found: " + req.Method}})
		}
	}
	return sc.Err()
}

func call(ctx context.Context, b Backend, params json.RawMessage) map[string]any {
	var p struct {
		Name      string `json:"name"`
		Arguments struct {
			Name     string `json:"name"`
			Question string `json:"question"`
		} `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return toolResult("", fmt.Errorf("bad arguments: %v", err))
	}
	switch p.Name {
	case ToolList:
		return toolResult(b.ListBots(ctx))
	case ToolAsk:
		if strings.TrimSpace(p.Arguments.Name) == "" || strings.TrimSpace(p.Arguments.Question) == "" {
			return toolResult("", errors.New("name and question are required"))
		}
		return toolResult(b.AskBot(ctx, p.Arguments.Name, p.Arguments.Question))
	}
	return toolResult("", fmt.Errorf("unknown tool %q", p.Name))
}

func toolResult(text string, err error) map[string]any {
	if err != nil {
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": err.Error()}}, "isError": true}
	}
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}
}

// HTTPBackend calls the app's handoff endpoint.
type HTTPBackend struct {
	URL   string // http://127.0.0.1:port
	Token string
	// Client is a client without a proxy or timeout when nil: an ask waits
	// for a whole turn of the other bot.
	Client *http.Client
}

// Reply is the endpoint's response body.
type Reply struct {
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
}

// AskRequest is the body of POST /ask_bot.
type AskRequest struct {
	Name     string `json:"name"`
	Question string `json:"question"`
}

func (c *HTTPBackend) ListBots(ctx context.Context) (string, error) {
	return c.post(ctx, "/list_bots", struct{}{})
}

func (c *HTTPBackend) AskBot(ctx context.Context, name, question string) (string, error) {
	return c.post(ctx, "/ask_bot", AskRequest{Name: name, Question: question})
}

func (c *HTTPBackend) post(ctx context.Context, path string, body any) (string, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.URL, "/")+path, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	client := c.Client
	if client == nil {
		client = &http.Client{Transport: &http.Transport{Proxy: nil}}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("bqbot is not reachable: %w", err)
	}
	defer resp.Body.Close()
	var r Reply
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&r); err != nil {
		return "", fmt.Errorf("bqbot answered %s", resp.Status)
	}
	if r.Error != "" {
		return "", errors.New(r.Error)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("bqbot answered %s", resp.Status)
	}
	return r.Text, nil
}

// ToolTimeout is how long an engine should wait for one tool call: ask_bot
// lasts a whole turn of the other bot.
const ToolTimeout = 15 * time.Minute

// ToolPrefix is how the engines name the server's tools: mcp__bqbot__ask_bot.
const ToolPrefix = "mcp__" + Server + "__"
