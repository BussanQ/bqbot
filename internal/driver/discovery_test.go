package driver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestClaudeModelPickerUsesProviderSettings(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("ANTHROPIC_MODEL", "")
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"model":"custom/other[1m]","env":{"ANTHROPIC_BASE_URL":"https://gateway.invalid","ANTHROPIC_AUTH_TOKEN":"fixture-secret","UNRELATED":"excluded"},"hooks":{"SessionStart":[]},"permissions":{"allow":["Bash"]},"modelSettings":{"custom/other[1m]":{"effortLevel":"high"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	path, defaults, clean, err := claudeModelSettings()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	clean()
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("settings file survived cleanup")
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0077 != 0 {
		t.Fatal("settings permissions")
	}
	if !json.Valid(data) || strings.Contains(string(data), "permissions") || strings.Contains(string(data), "hooks") || strings.Contains(string(data), "UNRELATED") || strings.Contains(string(data), "fixture-secret") || !strings.Contains(strings.Join(defaults.childEnv, "\n"), "ANTHROPIC_BASE_URL=https://gateway.invalid") || defaults.effort(defaults.Model) != "high" {
		t.Fatal("incorrect model/connection settings projection")
	}
	if runtime.GOOS == "windows" {
		return
	}
	bin := filepath.Join(dir, "claude")
	script := `#!/bin/sh
read -r req || exit 1
case "$req" in *list_models*) ;; *) exit 1;; esac
echo '{"type":"control_response","response":{"request_id":"catalog","subtype":"success","response":{"models":[{"value":"default","resolvedModel":"custom/other","supportedEffortLevels":["high","max"]},{"value":"opus","resolvedModel":"custom/other","displayName":"Other","supportedEffortLevels":["high","max"]},{"value":"plain","supportsEffort":false}]}}}'
`
	if err = os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	v, err := DiscoverModels(context.Background(), &Claude{Bin: bin}, EngineSettings{})
	if err != nil || len(v.Models) != 3 || v.Default != defaults.Model || v.DefaultEffort != "high" || v.Aliases["opus"] != "custom/other" || len(v.Models[1].Efforts) != 0 || len(v.Models[2].Efforts) != 2 {
		t.Fatal(v, err)
	}
}
