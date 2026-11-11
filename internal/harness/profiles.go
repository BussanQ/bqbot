package harness

import (
	"bqbot/internal/store"
	"errors"
)

// SaveProfile serializes profile edits with deletion and notifies live clients.
func (h *Harness) SaveProfile(bot store.Bot) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.deleting[bot.ID] {
		return errors.New("bot is closing")
	}
	if err := h.Store.SaveProfile(bot); err != nil {
		return err
	}
	saved, err := h.Bot(bot.ID)
	if err != nil {
		return err
	}
	h.metadataLocked(Update{BotID: bot.ID, Bot: &saved})
	return nil
}

// CloneBot copies a profile without racing deletion or copying runtime state.
func (h *Harness) CloneBot(id string) (store.Bot, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.deleting[id] {
		return store.Bot{}, errors.New("bot is closing")
	}
	bot, err := h.Store.CloneBot(id)
	if err == nil {
		h.metadataLocked(Update{BotID: bot.ID, Bot: &bot})
	}
	return bot, err
}

// UpdateModelOptions changes only composer settings, on the bot and engine the
// person actually opened. An old picker cannot recreate a bot or revert a profile edit.
func (h *Harness) UpdateModelOptions(id string, options store.Bot, validate func(store.Bot) error) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.deleting[id] {
		return errors.New("伙伴已关闭")
	}
	b, err := h.Bot(id)
	if err != nil {
		return err
	}
	if b.Engine != options.Engine {
		return errors.New("伙伴的 Agent 已切换，请重新打开模型列表")
	}
	b.Model, b.Effort, b.ApprovalMode = options.Model, options.Effort, options.ApprovalMode
	if err = validate(b); err != nil {
		return err
	}
	if err = h.Store.SaveProfile(b); err != nil {
		return err
	}
	saved, err := h.Bot(id)
	if err != nil {
		return err
	}
	h.metadataLocked(Update{BotID: id, Bot: &saved})
	return nil
}
