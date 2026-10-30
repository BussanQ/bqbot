package webgo

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"bqbot/internal/driver"
	"bqbot/internal/store"
)

type approvalCard struct {
	botID   string
	request driver.ApprovalRequest
	answer  string
}

func (a *app) stop(id string) {
	if err := a.api.Stop(id); err != nil {
		a.status = err.Error()
		return
	}
	if updates, err := a.api.Snapshot(); err == nil {
		for _, u := range updates {
			a.fold(u)
		}
	}
}

func (a *app) approvalCards(c *ui.Context, b store.Bot, t *thread) {
	ids := make([]string, 0, len(t.approvals))
	for id := range t.approvals {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	ui.Scroll(c.Key("approvals")).MaxHeight(230).Margin(4, 24).Children(func() {
		for _, id := range ids {
			card := t.approvals[id]
			r := card.request
			owner := card.botID
			if owner == "" {
				owner = b.ID
			}
			ui.Column(c.Key(id)).Gap(8).Padding(12).Radius(12).Background(a.pal.bubble).Border(1, a.pal.accent.Alpha(0.4)).Children(func() {
				title := "Permission requested · " + r.Tool
				if r.Kind == "question" {
					title = "Answer requested"
				}
				ui.Text(c, title).FontSize(13).FontWeight(600)
				if t.answering != "" {
					ui.Text(c, "While answering "+t.answering).FontSize(12).TextColor(a.pal.muted)
				}
				if r.Question != "" {
					ui.Text(c, r.Question).FontSize(13).Selectable()
				}
				if len(r.Input) > 0 {
					ui.Text(c, string(r.Input)).FontSize(12).Selectable()
				}
				if r.Kind == "question" {
					if len(r.Options) > 0 {
						ui.Select(c, &card.answer, r.Options).Label("Answer choice")
					}
					ui.TextInput(c, &card.answer).Label("Your answer").Placeholder("Your answer")
				}
				ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
					if ui.Button(c, "Deny").Clicked() {
						a.resolve(owner, id, driver.ApprovalDecision{Outcome: "deny"})
					}
					if r.Kind == "question" {
						if ui.PrimaryButton(c, "Answer").Disabled(strings.TrimSpace(card.answer) == "").Clicked() {
							a.resolve(owner, id, driver.ApprovalDecision{Outcome: "answer", Answer: card.answer})
						}
					} else if ui.PrimaryButton(c, "Allow").Clicked() {
						a.resolve(owner, id, driver.ApprovalDecision{Outcome: "allow"})
					}
				})
			})
		}
	})
}

func (a *app) resolve(botID, id string, d driver.ApprovalDecision) {
	if err := a.api.Approve(botID, id, d); err != nil {
		a.status = err.Error()
		return
	}
	for _, t := range a.threads {
		if card := t.approvals[id]; card != nil && (card.botID == "" || card.botID == botID) {
			delete(t.approvals, id)
		}
	}
}

func (a *app) openOptions(b store.Bot) {
	a.optionBot = b
	a.optionsError = ""
	if a.optionBot.ApprovalMode == "" {
		a.optionBot.ApprovalMode = "ask"
	}
	a.modelPick = "Default model"
	if b.Model != "" {
		a.modelPick = b.Engine + " / " + b.Model
	}
	a.optionsOpen = true
}

func (a *app) optionsView(c *ui.Context) {
	b := &a.optionBot
	ui.Column(c).Width(440).Gap(12).Children(func() {
		ui.Text(c, "Model & permissions").FontSize(17).FontWeight(700)
		ui.Text(c, "Changes apply to the next turn. Switching models starts a session with the conversation history.").FontSize(12).TextColor(a.pal.muted)
		labels := []string{"Default model"}
		choices := map[string]driver.Model{}
		for _, e := range a.catalog {
			if !e.Available {
				continue
			}
			for _, m := range e.Models {
				label := e.Name + " / " + m.ID
				labels = append(labels, label)
				choices[label] = m
			}
		}
		if !slices.Contains(labels, a.modelPick) {
			labels = append(labels, a.modelPick)
			choices[a.modelPick] = driver.Model{ID: b.Model, Engine: b.Engine}
		}
		ui.Text(c, "Model").FontSize(12).FontWeight(600)
		previous := a.modelPick
		ui.Select(c, &a.modelPick, labels).Label("Model catalog")
		if a.modelPick != previous {
			if m, ok := choices[a.modelPick]; ok {
				b.Engine, b.Model = m.Engine, m.ID
				b.Effort = ""
			} else {
				b.Model = ""
				b.Effort = ""
			}
		}
		ui.Text(c, "Engine: "+b.Engine).FontSize(12).TextColor(a.pal.muted)
		ui.TextInput(c, &b.Model).Label("Model ID").Placeholder("Default model, or custom model ID")
		efforts := []string{"default", "low", "medium", "high"}
		for _, e := range a.catalog {
			for _, m := range e.Models {
				if m.Engine == b.Engine && m.ID == b.Model && len(m.Efforts) > 0 {
					efforts = append([]string{"default"}, m.Efforts...)
				}
			}
		}
		effort := b.Effort
		if effort == "" {
			effort = "default"
		}
		ui.Text(c, "Reasoning effort").FontSize(12).FontWeight(600)
		ui.Select(c, &effort, efforts).Label("Effort")
		b.Effort = effort
		if b.Effort == "default" {
			b.Effort = ""
		}
		mode := approvalLabel(b.ApprovalMode)
		ui.Text(c, "Approval mode").FontSize(12).FontWeight(600)
		ui.Select(c, &mode, []string{"Ask", "Auto edit", "Fully automatic"}).Label("Approval mode")
		switch mode {
		case "Auto edit":
			b.ApprovalMode = "auto-edit"
		case "Fully automatic":
			b.ApprovalMode = "full-auto"
		default:
			b.ApprovalMode = "ask"
		}
		ui.Text(c, "Ask confirms tools individually. Auto edit permits file edits. Fully automatic permits tool actions. Questions still require an answer.").FontSize(12).TextColor(a.pal.muted)
		if a.optionsError != "" {
			ui.Text(c, a.optionsError).FontSize(12).TextColor(c.Theme().Danger)
		}
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "Cancel").Clicked() {
				a.optionsOpen = false
			}
			if ui.PrimaryButton(c, "Apply").Clicked() {
				if old, ok := a.bot(b.ID); ok && (old.Engine != b.Engine || old.Model != b.Model) {
					b.Cursor = ""
					b.Handoffs = nil
				}
				if err := a.api.SaveBot(*b); err != nil {
					a.optionsError = err.Error()
					return
				}
				for i := range a.bots {
					if a.bots[i].ID == b.ID {
						a.bots[i] = *b
					}
				}
				a.optionsOpen = false
			}
		})
	})
}

func approvalLabel(mode string) string {
	switch mode {
	case "auto-edit":
		return "Auto edit"
	case "full-auto":
		return "Fully automatic"
	default:
		return "Ask"
	}
}

func (a *app) openSettings() {
	settings, err := a.api.Settings()
	if err != nil {
		a.status = err.Error()
		return
	}
	a.settings = settings
	a.settingsError = ""
	if a.settings == nil {
		a.settings = map[string]driver.EngineSettings{}
	}
	a.settingsEngine = "claude"
	a.loadEngineSettings()
	a.settingsOpen = true
}

func (a *app) loadEngineSettings() {
	s := a.settings[a.settingsEngine]
	a.settingsPath, a.settingsURL, a.settingsKeyEnv = s.Path, s.BaseURL, s.APIKeyEnv
	a.settingsModels = strings.Join(s.Models, "\n")
}

func (a *app) keepEngineSettings() {
	s := driver.EngineSettings{Path: strings.TrimSpace(a.settingsPath), BaseURL: strings.TrimSpace(a.settingsURL), APIKeyEnv: strings.TrimSpace(a.settingsKeyEnv)}
	for _, m := range strings.FieldsFunc(a.settingsModels, func(r rune) bool { return r == '\n' || r == ',' }) {
		if m = strings.TrimSpace(m); m != "" {
			s.Models = append(s.Models, m)
		}
	}
	a.settings[a.settingsEngine] = s
}

func (a *app) settingsView(c *ui.Context) {
	ui.Column(c).Width(460).Gap(12).Children(func() {
		ui.Text(c, "Engine settings").FontSize(17).FontWeight(700)
		previous := a.settingsEngine
		ui.Select(c, &a.settingsEngine, []string{"claude", "codex", "pi", "grok", "openai"}).Label("Settings engine")
		if previous != a.settingsEngine {
			next := a.settingsEngine
			a.settingsEngine = previous
			a.keepEngineSettings()
			a.settingsEngine = next
			a.loadEngineSettings()
		}
		if a.settingsEngine == "openai" {
			ui.Text(c, "OpenAI compatible endpoint").FontSize(12)
			ui.TextInput(c, &a.settingsURL).Label("API base URL").Placeholder("https://api.openai.com/v1")
			ui.Text(c, "API key environment variable").FontSize(12)
			ui.TextInput(c, &a.settingsKeyEnv).Label("API key environment variable").Placeholder("OPENAI_API_KEY")
		} else {
			ui.Text(c, "CLI executable").FontSize(12)
			ui.TextInput(c, &a.settingsPath).Label("CLI path").Placeholder("Default: " + a.settingsEngine + " on PATH")
		}
		ui.Text(c, "Model catalog (optional, one ID per line)").FontSize(12)
		ui.TextArea(c, &a.settingsModels).Lines(3, 5).Label("Model catalog IDs")
		if a.settingsError != "" {
			ui.Text(c, a.settingsError).FontSize(12).TextColor(c.Theme().Danger)
		}
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "Cancel").Clicked() {
				a.settingsOpen = false
			}
			if ui.PrimaryButton(c, "Save settings").Clicked() {
				a.keepEngineSettings()
				if err := a.api.SaveSettings(a.settings); err != nil {
					a.settingsError = err.Error()
					return
				}
				a.settingsOpen = false
				a.probeEngines()
			}
		})
	})
}

func (a *app) pickImages(scopeID string) {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: "Attach images", Multiple: true, Filters: []mygo.FileFilter{{Name: "Images", Extensions: []string{"png", "jpg", "jpeg", "gif", "webp"}}}})
		var images []driver.Attachment
		if err == nil {
			for _, path := range paths {
				var image driver.Attachment
				image, err = readAttachment(path)
				if err != nil {
					break
				}
				images = append(images, image)
			}
		}
		a.post(func() {
			if err != nil {
				a.status = err.Error()
				return
			}
			t := a.threads[scopeID]
			if t == nil {
				return
			}
			if len(t.attachments)+len(images) > 4 {
				a.status = "Attach up to 4 images per message."
				return
			}
			t.attachments = append(t.attachments, images...)
		})
	}()
}

func readAttachment(path string) (driver.Attachment, error) {
	f, err := os.Open(path)
	if err != nil {
		return driver.Attachment{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 5*1024*1024+1))
	if err != nil {
		return driver.Attachment{}, err
	}
	if len(data) > 5*1024*1024 {
		return driver.Attachment{}, fmt.Errorf("%s exceeds 5 MB", filepath.Base(path))
	}
	mime := http.DetectContentType(data)
	if !slices.Contains([]string{"image/png", "image/jpeg", "image/gif", "image/webp"}, mime) {
		return driver.Attachment{}, fmt.Errorf("%s is not a supported image", filepath.Base(path))
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > 24_000_000 {
		return driver.Attachment{}, fmt.Errorf("%s is invalid or exceeds 24 megapixels", filepath.Base(path))
	}
	return driver.Attachment{Name: filepath.Base(path), MIME: mime, Data: base64.StdEncoding.EncodeToString(data)}, nil
}

func (a *app) image(key string, attachment driver.Attachment) *ui.Bitmap {
	if image, ok := a.images[key]; ok {
		return image
	}
	data, err := base64.StdEncoding.DecodeString(attachment.Data)
	if err != nil {
		return nil
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || int64(config.Width)*int64(config.Height) > 24_000_000 {
		return nil
	}
	image, err := ui.DecodeBitmap(data)
	if err != nil {
		return nil
	}
	a.images[key] = image
	return image
}
