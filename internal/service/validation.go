package service

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"bqbot/internal/driver"
	"bqbot/internal/store"
)

var idPattern = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
var envPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validID(id string) bool { return idPattern.MatchString(id) }
func safeText(s string, max int) bool {
	if len(s) > max {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func sortedEngines(drivers map[string]driver.Driver) []string {
	names := make([]string, 0, len(drivers))
	for name := range drivers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
func (s *Server) validateBot(b store.Bot, settings map[string]driver.EngineSettings) error {
	if !validID(b.ID) {
		return invalid("invalid bot ID")
	}
	if strings.TrimSpace(b.Name) == "" || !safeText(b.Name, 256) {
		return invalid("invalid bot name")
	}
	if !safeText(b.Avatar, 64) || strings.ContainsAny(b.Avatar, "/\\") {
		return invalid("invalid avatar")
	}
	if len(b.Persona) > 1<<20 {
		return invalid("persona is too large")
	}
	if _, ok := s.h.Drivers[b.Engine]; !ok {
		return invalid("unknown engine")
	}
	if b.ApprovalMode != "" && b.ApprovalMode != "ask" && b.ApprovalMode != "auto-edit" && b.ApprovalMode != "full-auto" {
		return invalid("invalid approval mode")
	}
	if !safeText(b.Cursor, 4096) || len(b.Handoffs) > 1024 {
		return invalid("invalid session data")
	}
	for id, cursor := range b.Handoffs {
		if !validID(id) || !safeText(cursor, 4096) {
			return invalid("invalid handoff session")
		}
	}
	models := driver.Catalog(b.Engine, settings[b.Engine])
	if b.Model != "" {
		if !safeText(b.Model, 256) || strings.TrimSpace(b.Model) != b.Model {
			return invalid("invalid model ID")
		}
		for _, m := range models {
			if m.ID == b.Model {
				return validateEffort(b.Effort, m.Efforts)
			}
		}
		// Providers add models independently of bqbot releases. A custom ID
		// is valid without requiring a catalog update.
		return validateEffort(b.Effort, []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"})
	}
	if b.Effort == "" {
		return nil
	}
	// The engine chooses its default model; accept only an effort advertised
	// by its catalog, without pretending that the first entry is the default.
	for _, m := range models {
		if validateEffort(b.Effort, m.Efforts) == nil {
			return nil
		}
	}
	return invalid("effort is not supported by the engine")
}
func validateEffort(effort string, allowed []string) error {
	if effort == "" {
		return nil
	}
	for _, v := range allowed {
		if v == effort {
			return nil
		}
	}
	return invalid("effort is not supported by the model")
}
func (s *Server) validateSettings(settings map[string]driver.EngineSettings) error {
	for name, cfg := range settings {
		if _, ok := s.h.Drivers[name]; !ok {
			return invalid("unknown engine in settings")
		}
		if !safeText(cfg.Path, 4096) {
			return invalid("invalid CLI path")
		}
		if cfg.APIKeyEnv != "" && (len(cfg.APIKeyEnv) > 128 || !envPattern.MatchString(cfg.APIKeyEnv)) {
			return invalid("APIKeyEnv must name an environment variable")
		}
		if cfg.BaseURL != "" {
			u, err := url.Parse(cfg.BaseURL)
			if err != nil || len(cfg.BaseURL) > 4096 || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				return invalid("invalid API base URL; credentials and queries are not allowed")
			}
		}
		if len(cfg.Models) > 256 {
			return invalid("too many configured models")
		}
		seen := map[string]bool{}
		for _, model := range cfg.Models {
			if model == "" || !safeText(model, 256) || strings.TrimSpace(model) != model || seen[model] {
				return invalid("invalid or duplicate model ID")
			}
			seen[model] = true
		}
	}
	return nil
}
func validateSend(req sendRequest) error {
	if req.Mode != "" && req.Mode != "queue" && req.Mode != "steer" {
		return invalid("mode must be queue or steer")
	}
	if len(req.Text) > 1<<20 {
		return invalid("message is too large")
	}
	if strings.TrimSpace(req.Text) == "" && len(req.Attachments) == 0 {
		return invalid("empty message")
	}
	if len(req.Attachments) > 4 {
		return invalid("at most four attachments are allowed")
	}
	total := 0
	for _, a := range req.Attachments {
		if !safeText(a.Name, 256) || strings.ContainsAny(a.Name, "/\\") {
			return invalid("invalid attachment name")
		}
		switch a.MIME {
		case "image/png", "image/jpeg", "image/gif", "image/webp":
		default:
			return invalid("unsupported image MIME type")
		}
		if len(a.Data) > base64.StdEncoding.EncodedLen(5<<20) {
			return invalid("attachment exceeds 5 MiB")
		}
		data, err := base64.StdEncoding.Strict().DecodeString(a.Data)
		if err != nil || len(data) == 0 || len(data) > 5<<20 {
			return invalid("invalid or oversized base64 attachment")
		}
		total += len(data)
		if total > 12<<20 {
			return invalid("attachments exceed 12 MiB")
		}
		if http.DetectContentType(data) != a.MIME {
			return invalid("attachment content does not match MIME type")
		}
	}
	return nil
}
