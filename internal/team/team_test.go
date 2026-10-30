package team

import (
	"strings"
	"testing"
	"time"

	"bqbot/internal/store"
)

func TestMarkdownRoundTripAndStrictPrivileges(t *testing.T) {
	d := Document{Version: 1, Name: "Team", Bots: []Bot{{Name: "Echo", Engine: "echo", Persona: "line one\nline two"}}, Rooms: []Room{{Name: "Room", Members: []string{"Echo"}, Lead: "Echo"}}, Routines: []Routine{{Name: "Timer", Bot: "Echo", Prompt: "Hi", Kind: "interval", IntervalSeconds: 60}}, Notes: "Team notes"}
	text, err := Format(d)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	if got.Notes != d.Notes || got.Bots[0].Persona != d.Bots[0].Persona || len(got.Routines) != 1 {
		t.Fatalf("round trip: %#v", got)
	}
	for _, field := range []string{"approval_mode: full-auto", "cursor: secret", "api_key: secret", "work_dir: /tmp"} {
		bad := strings.Replace(text, "    engine: echo", "    engine: echo\n    "+field, 1)
		if bad == text {
			t.Fatal("test replacement failed", text)
		}
		if _, err := Parse(bad); err == nil {
			t.Fatalf("accepted %s", field)
		}
	}
}
func TestExportNeverIncludesLocalState(t *testing.T) {
	b := store.Bot{ID: "local", Name: "Echo", Engine: "echo", Cursor: "SECRET-CURSOR", ApprovalMode: "full-auto", Handoffs: map[string]string{"peer": "SECRET-HANDOFF"}}
	r := store.Routine{ID: "routine", BotID: "local", Name: "job", Prompt: "run", Enabled: true, NextAt: time.Now(), Kind: "interval", IntervalSeconds: 5}
	d, err := Export("Team", []store.Bot{b}, nil, []store.Routine{r})
	if err != nil {
		t.Fatal(err)
	}
	text, err := Format(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"SECRET", "full-auto", "next_at", "enabled", "local", "routine" + "_id"} {
		if strings.Contains(text, word) {
			t.Errorf("export leaked %q: %s", word, text)
		}
	}
	if d.Routines[0].Local("new", "new-bot").Enabled {
		t.Fatal("imported routine enabled")
	}
}
func TestInvalidTeamReferences(t *testing.T) {
	for _, d := range []Document{
		{Version: 1, Name: "T", Bots: []Bot{{Name: "a", Engine: "echo"}, {Name: "a", Engine: "echo"}}},
		{Version: 1, Name: "T", Bots: []Bot{{Name: "a", Engine: "echo"}}, Rooms: []Room{{Name: "r", Members: []string{"a"}, Lead: "missing"}}},
		{Version: 1, Name: "T", Bots: []Bot{{Name: "a", Engine: "echo"}}, Routines: []Routine{{Name: "r", Bot: "missing", Kind: "webhook", Prompt: "x"}}},
	} {
		if err := d.Validate(); err == nil {
			t.Fatal("invalid team accepted", d)
		}
	}
}
