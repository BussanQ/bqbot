package service

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bqbot/internal/driver"
	"bqbot/internal/harness"
	"bqbot/internal/store"
	"bqbot/internal/voice"
)

func TestVoiceConfigurationAndSynthesis(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := harness.New(st, &driver.Echo{})
	defer h.Close()
	server, err := Listen(h, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client := NewClient(server.URL, server.Token)
	state, err := client.Voice()
	if err != nil || state.Settings.Enabled || state.Configured {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	if _, err := client.SynthesizeVoice(context.Background(), "hello"); err == nil {
		t.Fatal("default speech is enabled")
	}
	wav := make([]byte, 46)
	copy(wav, "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], 38)
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint16(wav[20:], 1)
	binary.LittleEndian.PutUint16(wav[22:], 1)
	binary.LittleEndian.PutUint32(wav[24:], 24000)
	binary.LittleEndian.PutUint32(wav[28:], 48000)
	binary.LittleEndian.PutUint16(wav[32:], 2)
	binary.LittleEndian.PutUint16(wav[34:], 16)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], 2)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/speech" {
			t.Errorf("path=%s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "audio/wav")
		w.Write(wav)
	}))
	defer provider.Close()
	cfg := voice.Config{Enabled: true, Provider: "chatterbox", URL: provider.URL}
	if err := client.SaveVoice(cfg); err != nil {
		t.Fatal(err)
	}
	data, err := client.SynthesizeVoice(context.Background(), "hello")
	if err != nil || len(data) != len(wav) {
		t.Fatalf("len=%d err=%v", len(data), err)
	}
	t.Setenv("BQBOT_TEST_VOICE_KEY", "private-key-not-config")
	cfg = voice.Config{Enabled: true, Provider: "xai", KeyEnv: "BQBOT_TEST_VOICE_KEY"}
	if err := client.SaveVoice(cfg); err != nil {
		t.Fatal(err)
	}
	state, err = client.Voice()
	if err != nil || !state.Configured {
		t.Fatalf("%+v %v", state, err)
	}
	saved, err := os.ReadFile(filepath.Join(dir, "voice.json"))
	if err != nil || strings.Contains(string(saved), "private-key-not-config") {
		t.Fatalf("settings persisted a credential: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "voice.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("settings permissions: %v", err)
	}
	cfg.URL = "https://untrusted.example"
	if err := client.SaveVoice(cfg); err == nil || strings.Contains(err.Error(), "private-key") {
		t.Fatal(err)
	}
}
