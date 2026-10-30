package store

import (
	"path/filepath"

	"bqbot/internal/voice"
)

// VoiceSettings contains public synthesis settings; credentials stay in the process environment.
func (s *Store) VoiceSettings() (voice.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var v voice.Config
	err := readJSONFile(filepath.Join(s.dir, "voice.json"), &v)
	return v, err
}

func (s *Store) SaveVoiceSettings(v voice.Config) error {
	if err := v.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSON(filepath.Join(s.dir, "voice.json"), v)
}
