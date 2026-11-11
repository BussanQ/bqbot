package driver

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type claudeDefaults struct {
	childEnv      []string
	Model         string
	EffortLevel   string
	ModelSettings map[string]struct{ EffortLevel string }
}

func (c claudeDefaults) effort(id string) string {
	if lv := c.ModelSettings[id].EffortLevel; lv != "" {
		return lv
	}
	return c.EffortLevel
}

// Claude runs with isolated setting sources. Import only model selection and
// provider routing so discovery and turns agree without importing hooks or permissions.
// Provider credentials are passed only in the child environment.
func claudeModelSettings() (string, claudeDefaults, func(), error) {
	nop := func() {}
	defaults := claudeDefaults{Model: os.Getenv("ANTHROPIC_MODEL")}
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", defaults, nop, err
		}
		dir = filepath.Join(home, ".claude")
	}
	data, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		return "", defaults, nop, nil
	}
	if err != nil {
		return "", defaults, nop, err
	}
	var source map[string]json.RawMessage
	if json.Unmarshal(data, &source) != nil {
		return "", defaults, nop, errors.New("无法读取 Claude 用户模型设置")
	}
	settings := map[string]any{}
	for _, key := range []string{"model", "modelPicker", "modelSettings", "availableModels", "effortLevel"} {
		if v, ok := source[key]; ok {
			settings[key] = v
		}
	}
	var env map[string]string
	_ = json.Unmarshal(source["env"], &env)
	routing := map[string]string{}
	for k, v := range env {
		if strings.HasPrefix(k, "ANTHROPIC_DEFAULT_") && strings.Contains(k, "_MODEL") {
			routing[k] = v
			continue
		}
		switch k {
		case "ANTHROPIC_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_MODEL_CAPABILITIES", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY":
			routing[k] = v
		}
	}
	for k, v := range routing {
		defaults.childEnv = append(defaults.childEnv, k+"="+v)
	}
	_ = json.Unmarshal(data, &defaults)
	if v := routing["ANTHROPIC_MODEL"]; v != "" {
		defaults.Model = v
	} else if v := os.Getenv("ANTHROPIC_MODEL"); v != "" {
		defaults.Model = v
	}
	if len(settings) == 0 {
		return "", defaults, nop, nil
	}
	f, err := os.CreateTemp("", "bqbot-claude-models-*.json")
	if err != nil {
		return "", defaults, nop, err
	}
	clean := func() { os.Remove(f.Name()) }
	err = json.NewEncoder(f).Encode(settings)
	closeErr := f.Close()
	if err = errors.Join(err, closeErr); err != nil {
		clean()
		return "", defaults, nop, err
	}
	return f.Name(), defaults, clean, nil
}
