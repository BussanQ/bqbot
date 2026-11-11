package driver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"

	"strings"
	"time"
)

type ModelInventory struct {
	index         map[string]int
	Aliases       map[string]string `json:"aliases,omitempty"`
	DefaultEffort string            `json:"default_effort,omitempty"`
	Models        []Model           `json:"models"`
	Default       string            `json:"default,omitempty"`
	Source        string            `json:"source"`
}

func (v *ModelInventory) add(engine, id, name string, efforts []string) *Model {
	if strings.TrimSpace(id) == "" || len(id) > 256 || strings.ContainsAny(id, "\x00\r\n") {
		return nil
	}
	if v.index == nil {
		v.index = make(map[string]int)
	}
	if i, ok := v.index[id]; ok {
		return &v.Models[i]
	}
	if name == "" {
		name = id
	}
	v.index[id] = len(v.Models)
	v.Models = append(v.Models, Model{ID: id, Name: name, Engine: engine, Efforts: efforts})
	return &v.Models[len(v.Models)-1]
}

// DiscoverModels queries metadata without sending a prompt. Configured IDs
// also remain selectable when a provider restricts or omits its inventory.
func DiscoverModels(ctx context.Context, d Driver, cfg EngineSettings) (ModelInventory, error) {
	name := d.Name()
	base := Catalog(name, cfg)
	fallback := ModelInventory{Models: base, Source: "fallback"}
	if name == "echo" {
		fallback.Default = "echo"
		fallback.Source = "builtin"
		return fallback, nil
	}
	if name == "openai" || name == "bqbot" {
		fallback.Default = "gpt-4.1"
		if len(base) > 0 {
			fallback.Default = base[0].ID
		}
		if len(cfg.Models) > 0 {
			fallback.Source = "configured"
		}
		if cfg.BaseURL == "" && os.Getenv("OPENAI_API_KEY") == "" && cfg.APIKeyEnv == "" {
			return fallback, nil
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	v := ModelInventory{Source: "agent"}
	var err error
	switch name {
	case "claude":
		err = claudeInventory(ctx, d, cfg, &v)
	case "codex":
		err = codexInventory(ctx, d, cfg, &v)
	case "pi":
		err = piInventory(ctx, d, cfg, &v)
	case "grok":
		err = grokInventory(ctx, d, cfg, &v)
	case "openai", "bqbot":
		v.Default = fallback.Default
		err = apiInventory(ctx, cfg, name, &v)
	default:
		return fallback, nil
	}
	if err != nil || len(v.Models) == 0 {
		return fallback, errors.New("未能读取 Agent 的模型列表")
	}
	if len(cfg.Models) > 0 {
		for _, m := range base {
			v.add(name, m.ID, m.Name, m.Efforts)
		}
	}
	return v, nil
}

type modelPipe struct {
	scan *bufio.Scanner
	send *json.Encoder
}

func queryModels(ctx context.Context, bin string, args, env []string, fn func(*modelPipe, string) error) error {
	dir, err := os.MkdirTemp("", "bqbot-catalog-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { stdout.Close(); stdin.Close() })
	defer func() { stop(); stdin.Close(); stdout.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	scan := bufio.NewScanner(stdout)
	scan.Buffer(make([]byte, 65536), 4<<20)
	return fn(&modelPipe{scan, json.NewEncoder(stdin)}, dir)
}
func (p *modelPipe) rpc(method string, id int, params any, dst any) error {
	if err := p.send.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return err
	}
	for p.scan.Scan() {
		var m struct {
			ID     int
			Result json.RawMessage
		}
		if json.Unmarshal(p.scan.Bytes(), &m) == nil && m.ID == id {
			return json.Unmarshal(m.Result, dst)
		}
	}
	return io.ErrUnexpectedEOF
}
func binary(d Driver, cfg EngineSettings) string {
	if cfg.Path != "" {
		return cfg.Path
	}
	return d.Binary()
}

func claudeInventory(ctx context.Context, d Driver, cfg EngineSettings, v *ModelInventory) error {
	file, defaults, clean, err := claudeModelSettings()
	selected := defaults.Model
	if err != nil {
		return err
	}
	defer clean()
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--no-session-persistence", "--setting-sources", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`}
	if file != "" {
		args = append(args, "--settings", file)
	}
	return queryModels(ctx, binary(d, cfg), args, defaults.childEnv, func(p *modelPipe, _ string) error {
		for _, op := range []string{"list_models", "initialize"} {
			if err := p.send.Encode(map[string]any{"type": "control_request", "request_id": "catalog", "request": map[string]string{"subtype": op}}); err != nil {
				return err
			}
			for p.scan.Scan() {
				var m struct {
					Type     string
					Response struct {
						ID       string `json:"request_id"`
						Subtype  string
						Response struct {
							Models []struct {
								Value, ResolvedModel, DisplayName string
								SupportedEffortLevels             []string
								SupportsEffort                    *bool
							}
						}
					}
				}
				if json.Unmarshal(p.scan.Bytes(), &m) != nil || m.Type != "control_response" || m.Response.ID != "catalog" {
					continue
				}
				if m.Response.Subtype != "success" || len(m.Response.Response.Models) == 0 {
					break
				}
				var defaultLevels []string
				v.Aliases = map[string]string{}
				for _, row := range m.Response.Response.Models {
					id := row.ResolvedModel
					if id == "" {
						id = row.Value
					}
					if row.Value == "default" {
						if row.SupportsEffort == nil || *row.SupportsEffort {
							defaultLevels = row.SupportedEffortLevels
						}
						if id != "default" {
							v.Default = id
						}
						continue
					}
					if row.Value != "" && row.Value != id {
						v.Aliases[row.Value] = id
					}
					if row.Value == selected {
						selected = id
					}
					levels := row.SupportedEffortLevels
					if row.SupportsEffort != nil && !*row.SupportsEffort {
						levels = nil
					}
					v.add("claude", id, row.DisplayName, levels)
				}
				if selected != "" && selected != "default" {
					v.Default = selected
				}
				if v.Default != "" {
					var levels []string
					for _, row := range m.Response.Response.Models {
						if row.Value == "default" && strings.TrimSuffix(v.Default, "[1m]") == strings.TrimSuffix(row.ResolvedModel, "[1m]") {
							levels = defaultLevels
						}
					}
					v.add("claude", v.Default, v.Default, levels)
					v.DefaultEffort = defaults.effort(v.Default)
				}
				for i := range v.Models {
					v.Models[i].DefaultEffort = defaults.effort(v.Models[i].ID)
				}
				return nil
			}
		}
		return io.ErrUnexpectedEOF
	})
}

func codexInventory(ctx context.Context, d Driver, cfg EngineSettings, v *ModelInventory) error {
	return queryModels(ctx, binary(d, cfg), (&Codex{}).Args(TurnInput{}), nil, func(p *modelPipe, _ string) error {
		var init json.RawMessage
		if err := p.rpc("initialize", 1, map[string]any{"clientInfo": map[string]string{"name": "bqbot", "version": "1"}}, &init); err != nil {
			return err
		}
		if err := p.send.Encode(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
			return err
		}
		params := map[string]any{"limit": 100, "includeHidden": false}
		for page := 0; page < 20; page++ {
			var result struct {
				Data []struct {
					ID, Model, DisplayName, DefaultReasoningEffort string
					Hidden, IsDefault                              bool
					SupportedReasoningEfforts                      []struct{ ReasoningEffort string }
				}
				NextCursor string
			}
			if err := p.rpc("model/list", 2+page, params, &result); err != nil {
				return err
			}
			for _, m := range result.Data {
				if m.Hidden {
					continue
				}
				id := m.Model
				if id == "" {
					id = m.ID
				}
				var levels []string
				for _, r := range m.SupportedReasoningEfforts {
					levels = append(levels, r.ReasoningEffort)
				}
				if row := v.add("codex", id, m.DisplayName, levels); row != nil {
					row.DefaultEffort = m.DefaultReasoningEffort
				}
				if m.IsDefault {
					v.Default = id
				}
			}
			if result.NextCursor == "" {
				break
			}
			params["cursor"] = result.NextCursor
		}
		var config struct {
			Config struct {
				Model  string
				Effort string `json:"model_reasoning_effort"`
			}
		}
		if p.rpc("config/read", 99, map[string]bool{"includeLayers": false}, &config) == nil {
			if config.Config.Model != "" {
				v.Default = config.Config.Model
			}
			v.DefaultEffort = config.Config.Effort
		}

		return nil
	})
}

func piInventory(ctx context.Context, d Driver, cfg EngineSettings, v *ModelInventory) error {
	return queryModels(ctx, binary(d, cfg), []string{"--mode", "rpc", "--no-session", "--no-extensions", "--no-mcp"}, nil, func(p *modelPipe, _ string) error {
		for _, cmd := range []string{"get_available_models", "get_state"} {
			if err := p.send.Encode(map[string]string{"type": cmd, "id": cmd}); err != nil {
				return err
			}
			for p.scan.Scan() {
				var r struct {
					Type, ID string
					Success  bool
					Data     struct {
						Models []struct {
							ID, Name, Provider string
							Reasoning          bool
							ThinkingLevelMap   map[string]*string
						}
						ThinkingLevel string
						Model         struct{ ID, Provider string }
					}
				}
				if json.Unmarshal(p.scan.Bytes(), &r) != nil || r.Type != "response" || r.ID != cmd {
					continue
				}
				if cmd == "get_available_models" && !r.Success {
					return errors.New("models unavailable")
				}
				for _, m := range r.Data.Models {
					if m.ID == "" || m.Provider == "" {
						continue
					}
					var levels []string
					if m.Reasoning {
						for _, lv := range []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"} {
							mapped, ok := m.ThinkingLevelMap[lv]
							if mapped != nil || (!ok && lv != "xhigh" && lv != "max") {
								levels = append(levels, lv)
							}
						}
					}
					v.add("pi", m.Provider+"/"+m.ID, m.Name, levels)
				}
				if m := r.Data.Model; m.ID != "" {
					v.Default = m.Provider + "/" + m.ID
					v.DefaultEffort = r.Data.ThinkingLevel
				}
				break
			}
		}

		return nil
	})
}

type acpChoice struct {
	Value, Name string
	Options     []acpChoice
}
type acpOption struct {
	ID, Category string
	CurrentValue json.RawMessage
	Options      []acpChoice
}

func (o acpOption) model() bool { return o.Category == "model" || o.ID == "model" }
func (o acpOption) effort() bool {
	return o.Category == "thought_level" || o.ID == "reasoning_effort" || o.ID == "effort"
}
func (o acpOption) current() string {
	var current string
	_ = json.Unmarshal(o.CurrentValue, &current)
	return current
}
func (o acpOption) choices() []acpChoice {
	var out []acpChoice
	for _, v := range o.Options {
		if v.Value != "" {
			out = append(out, v)
		} else {
			out = append(out, v.Options...)
		}
	}
	return out
}

type acpModelSession struct {
	ID     string `json:"sessionId"`
	Models struct {
		CurrentModelID  string
		AvailableModels []struct{ ModelID, Name string }
	}
	ConfigOptions []acpOption
}

func grokInventory(ctx context.Context, d Driver, cfg EngineSettings, v *ModelInventory) error {
	return queryModels(ctx, binary(d, cfg), []string{"--acp"}, nil, func(p *modelPipe, dir string) error {
		var init json.RawMessage
		if err := p.rpc("initialize", 1, map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}}, &init); err != nil {
			return err
		}
		var s acpModelSession
		if err := p.rpc("session/new", 2, map[string]any{"cwd": dir, "mcpServers": []any{}}, &s); err != nil {
			return err
		}
		v.Default = s.Models.CurrentModelID
		var levels []string
		for _, o := range s.ConfigOptions {
			if o.effort() {
				v.DefaultEffort = o.current()
				for _, lv := range o.choices() {
					levels = append(levels, lv.Value)
				}
			}
		}
		for _, m := range s.Models.AvailableModels {
			v.add("grok", m.ModelID, m.Name, levels)
		}
		for _, o := range s.ConfigOptions {
			if o.model() {
				if o.current() != "" {
					v.Default = o.current()
				}
				for _, m := range o.choices() {
					v.add("grok", m.Value, m.Name, levels)
				}
			}
		}
		return nil
	})
}

func apiInventory(ctx context.Context, cfg EngineSettings, name string, v *ModelInventory) error {
	base := strings.TrimSuffix(strings.TrimRight(cfg.BaseURL, "/"), "/chat/completions")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	u, err := url.Parse(base + "/models")
	if err != nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("invalid API address")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return err
	}
	key := cfg.APIKeyEnv
	if key == "" && u.Scheme == "https" && u.Host == "api.openai.com" {
		key = "OPENAI_API_KEY"
	}
	if key != "" {
		val := os.Getenv(key)
		if val == "" {
			return errors.New("missing model service key")
		}
		req.Header.Set("Authorization", "Bearer "+val)
	}
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New("model service unavailable")
	}
	var data struct {
		Data []struct {
			ID      string
			Name    string
			Efforts []string `json:"supported_reasoning_efforts"`
		}
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&data); err != nil {
		return err
	}
	known := Catalog(name, EngineSettings{})
	for _, m := range data.Data {
		if m.Efforts == nil {
			for _, row := range known {
				if row.ID == m.ID {
					m.Efforts = row.Efforts
					break
				}
			}
		}
		v.add(name, m.ID, m.Name, m.Efforts)
	}
	return nil
}
