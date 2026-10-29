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
