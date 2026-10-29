package driver

import "strings"

// Catalog returns a static model catalog. Configured model IDs replace the defaults.
// Callers may still send a model ID not present in this list.
func Catalog(engine string, settings EngineSettings) []Model {
	engine = strings.ToLower(engine)
	var ids, efforts []string
	switch engine {
	case "claude":
		ids, efforts = []string{"sonnet", "opus", "haiku"}, []string{"low", "medium", "high"}
	case "codex":
		ids, efforts = []string{"gpt-5-codex", "gpt-5"}, []string{"low", "medium", "high"}
	case "pi":
		ids, efforts = []string{"anthropic/claude-sonnet-4-5", "openai/gpt-5"}, []string{"off", "minimal", "low", "medium", "high"}
	case "grok":
		ids = []string{"grok-code-fast-1", "grok-4"}
	case "api", "openai":
		ids, efforts = []string{"gpt-4.1", "gpt-4.1-mini", "gpt-5"}, []string{"low", "medium", "high"}
	case "echo":
		ids = []string{"echo"}
	}
	if len(settings.Models) != 0 {
		ids = settings.Models
	}
	models := make([]Model, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		models = append(models, Model{ID: id, Name: id, Engine: engine, Efforts: append([]string(nil), efforts...)})
	}
	return models
}
