package webgo

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/egoist/mygo"

	"bqbot/internal/team"
)

// TeamAction carries portable Markdown, never arbitrary filesystem paths.
type TeamAction struct {
	Kind     string `json:"kind"`
	Markdown string `json:"markdown"`
	Name     string `json:"name"`
	RoomID   string `json:"room_id"`
}

func (d *Desktop) Team(r TeamAction) (json.RawMessage, error) {
	d.mu.Lock()
	api := d.app.api
	closed := d.closed
	d.mu.Unlock()
	if closed {
		return nil, errors.New("desktop is closed")
	}
	var v any
	var err error
	switch r.Kind {
	case "preview":
		v, err = api.PreviewTeam(r.Markdown)
	case "apply":
		v, err = api.ApplyTeam(r.Markdown)
	case "export":
		var markdown string
		markdown, err = api.ExportTeam(r.Name, r.RoomID)
		v = map[string]string{"markdown": markdown}
	case "save-file":
		if _, err = team.Parse(r.Markdown); err != nil {
			break
		}
		var path string
		path, err = mygo.Dialog.Save(mygo.SaveDialogOptions{Title: "保存团队 Markdown", DefaultPath: "bqbot-team.md", Filters: []mygo.FileFilter{{Name: "Markdown", Extensions: []string{"md"}}}})
		if err != nil || path == "" {
			v = false
			break
		}
		// The native dialog confirms replacement. Rename a private file so an
		// existing symlink is replaced, never followed to another target.
		err = saveTeamFile(path, r.Markdown)
		v = err == nil
	case "presets":
		presets := team.Presets()
		items := []map[string]string{}
		for _, p := range presets {
			markdown, e := team.Format(p)
			if e != nil {
				return nil, e
			}
			items = append(items, map[string]string{"name": p.Name, "markdown": markdown})
		}
		v = items
	default:
		return nil, errors.New("unknown team action")
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

func saveTeamFile(path, markdown string) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".bqbot-team-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.WriteString(markdown); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
