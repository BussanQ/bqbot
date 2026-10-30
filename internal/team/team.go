// Package team reads portable team Markdown without importing local privileges.
package team

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"bqbot/internal/store"
	"gopkg.in/yaml.v3"
)

const MaxBytes = 2 << 20

// Document contains only shareable settings. References use names, never local IDs.
type Document struct {
	Version  int       `yaml:"version" json:"version"`
	Name     string    `yaml:"name" json:"name"`
	Bots     []Bot     `yaml:"bots" json:"bots"`
	Rooms    []Room    `yaml:"rooms,omitempty" json:"rooms,omitempty"`
	Routines []Routine `yaml:"routines,omitempty" json:"routines,omitempty"`
	Notes    string    `yaml:"-" json:"notes,omitempty"`
}
type Bot struct {
	Name    string `yaml:"name" json:"name"`
	Engine  string `yaml:"engine" json:"engine"`
	Model   string `yaml:"model,omitempty" json:"model,omitempty"`
	Persona string `yaml:"persona,omitempty" json:"persona,omitempty"`
	Avatar  string `yaml:"avatar,omitempty" json:"avatar,omitempty"`
}
type Room struct {
	Name         string   `yaml:"name" json:"name"`
	Instructions string   `yaml:"instructions,omitempty" json:"instructions,omitempty"`
	Members      []string `yaml:"members" json:"members"`
	Lead         string   `yaml:"lead" json:"lead"`
	ReplyMode    string   `yaml:"reply_mode,omitempty" json:"reply_mode,omitempty"`
}
type Routine struct {
	Name            string    `yaml:"name" json:"name"`
	Bot             string    `yaml:"bot" json:"bot"`
	Prompt          string    `yaml:"prompt" json:"prompt"`
	Kind            string    `yaml:"kind" json:"kind"`
	At              time.Time `yaml:"at,omitempty" json:"at,omitempty"`
	Time            string    `yaml:"time,omitempty" json:"time,omitempty"`
	Timezone        string    `yaml:"timezone,omitempty" json:"timezone,omitempty"`
	IntervalSeconds int       `yaml:"interval_seconds,omitempty" json:"interval_seconds,omitempty"`
}

func Parse(markdown string) (Document, error) {
	var d Document
	if len(markdown) > MaxBytes {
		return d, errors.New("team Markdown exceeds 2 MiB")
	}
	markdown = strings.ReplaceAll(markdown, "\r\n", "\n")
	if !strings.HasPrefix(markdown, "---\n") {
		return d, errors.New("team requires YAML frontmatter")
	}
	rest := markdown[4:]
	end := strings.Index("\n"+rest, "\n---\n")
	if end < 0 {
		return d, errors.New("unterminated YAML frontmatter")
	}
	front := rest[:end]
	decoder := yaml.NewDecoder(strings.NewReader(front))
	decoder.KnownFields(true)
	if err := decoder.Decode(&d); err != nil {
		return d, fmt.Errorf("invalid team frontmatter: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return d, errors.New("only one YAML document is allowed")
	}
	d.Notes = strings.TrimSpace(rest[end+4:])
	return d, d.Validate()
}
func safeName(s string, max int) bool {
	return s != "" && strings.TrimSpace(s) == s && len(s) <= max && strings.IndexFunc(s, unicode.IsControl) < 0
}
func (d Document) Validate() error {
	if d.Version != 1 || !safeName(d.Name, 120) || len(d.Bots) == 0 || len(d.Bots) > 32 || len(d.Rooms) > 32 || len(d.Routines) > 128 {
		return errors.New("team version 1, name and 1–32 bots are required")
	}
	names := map[string]bool{}
	for _, b := range d.Bots {
		if !safeName(b.Name, 120) || names[b.Name] || !safeName(b.Engine, 64) || len(b.Persona) > 128<<10 || len(b.Model) > 256 || len(b.Avatar) > 64 || strings.ContainsAny(b.Avatar, "/\\") {
			return errors.New("invalid or duplicate bot")
		}
		names[b.Name] = true
	}
	rooms := map[string]bool{}
	for _, r := range d.Rooms {
		if !safeName(r.Name, 120) || rooms[r.Name] || len(r.Instructions) > 128<<10 || len(r.Members) == 0 || len(r.Members) > 32 || (r.ReplyMode != "" && r.ReplyMode != "lead" && r.ReplyMode != "auto") {
			return errors.New("invalid or duplicate room")
		}
		rooms[r.Name] = true
		seen := map[string]bool{}
		for _, n := range r.Members {
			if !names[n] || seen[n] {
				return errors.New("room has unknown or duplicate member")
			}
			seen[n] = true
		}
		if !seen[r.Lead] {
			return errors.New("room lead must be a member")
		}
	}
	routines := map[string]bool{}
	for _, r := range d.Routines {
		if !names[r.Bot] || routines[r.Name] {
			return errors.New("routine has unknown bot or duplicate name")
		}
		routines[r.Name] = true
		v := r.Local("check", "check")
		if err := v.Validate(); err != nil {
			return err
		}
	}
	return nil
}
func (r Routine) Local(id, botID string) store.Routine {
	return store.Routine{ID: id, Name: r.Name, BotID: botID, Prompt: r.Prompt, Kind: r.Kind, At: r.At, Time: r.Time, Timezone: r.Timezone, IntervalSeconds: r.IntervalSeconds, Enabled: false}
}
func Format(d Document) (string, error) {
	if err := d.Validate(); err != nil {
		return "", err
	}
	var b bytes.Buffer
	b.WriteString("---\n")
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(d); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	b.WriteString("---\n")
	if d.Notes != "" {
		b.WriteString("\n" + d.Notes + "\n")
	}
	return b.String(), nil
}

// Presets are inert templates; applying one always uses Ask permissions.
func Presets() []Document {
	return []Document{
		{Version: 1, Name: "离线演示", Bots: []Bot{{Name: "Echo", Engine: "echo", Persona: "用于验证本地聊天与自动化。"}}},
		{Version: 1, Name: "研发小组", Bots: []Bot{{Name: "开发", Engine: "codex", Persona: "实现任务并运行相关测试，说明尚未验证的部分。"}, {Name: "审阅", Engine: "codex", Persona: "审阅代码，给出可复现的缺陷及修复建议。"}}, Rooms: []Room{{Name: "研发", Members: []string{"开发", "审阅"}, Lead: "开发", ReplyMode: "lead"}}},
	}
}

// Export builds a fresh allowlisted document; credentials, sessions, grants,
// working directories, memory and transcripts are never part of this format.
func Export(name string, bots []store.Bot, rooms []store.Room, routines []store.Routine) (Document, error) {
	d := Document{Version: 1, Name: name}
	names := map[string]string{}
	for _, b := range bots {
		names[b.ID] = b.Name
		d.Bots = append(d.Bots, Bot{Name: b.Name, Engine: b.Engine, Model: b.Model, Persona: b.Persona, Avatar: b.Avatar})
	}
	for _, r := range rooms {
		v := Room{Name: r.Name, Instructions: r.Instructions, Lead: names[r.LeadID], ReplyMode: r.ReplyMode}
		valid := true
		for _, id := range r.Members {
			if names[id] == "" {
				valid = false
				break
			}
			v.Members = append(v.Members, names[id])
		}
		if valid {
			d.Rooms = append(d.Rooms, v)
		}
	}
	for _, r := range routines {
		if names[r.BotID] != "" {
			d.Routines = append(d.Routines, Routine{Name: r.Name, Bot: names[r.BotID], Prompt: r.Prompt, Kind: r.Kind, At: r.At, Time: r.Time, Timezone: r.Timezone, IntervalSeconds: r.IntervalSeconds})
		}
	}
	return d, d.Validate()
}
