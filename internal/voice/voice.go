// Package voice synthesizes speech without exposing provider credentials to callers.
package voice

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxAudioBytes = 16 << 20
const MaxTextRunes = 5000

// Config stores only public provider settings and an environment variable name.
// Chatterbox uses a separately installed OpenAI-compatible local TTS server.
type Config struct {
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider"`
	Voice    string `json:"voice"`
	Model    string `json:"model"`
	KeyEnv   string `json:"key_env"`
	URL      string `json:"url"`
	AutoRead bool   `json:"auto_read"`
	Language string `json:"language"`
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (c Config) Validate() error {
	switch c.Provider {
	case "", "say", "elevenlabs", "fish", "xai", "chatterbox":
	default:
		return errors.New("unknown speech provider")
	}
	if c.Enabled && c.Provider == "" {
		return errors.New("choose a speech provider")
	}
	if c.KeyEnv != "" && !envName.MatchString(c.KeyEnv) {
		return errors.New("API key must reference an environment variable name")
	}
	if len(c.Voice) > 200 || len(c.Model) > 200 || len(c.Language) > 40 {
		return errors.New("speech setting is too long")
	}
	if strings.ContainsAny(c.Voice+c.Model+c.Language, "\r\n\x00") {
		return errors.New("invalid speech setting")
	}
	if c.URL != "" {
		u, err := url.Parse(c.URL)
		if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return errors.New("Chatterbox URL must be a loopback HTTP origin")
		}
		ip := net.ParseIP(u.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return errors.New("Chatterbox must use a literal loopback address")
		}
	}
	if c.Enabled {
		if c.Provider == "elevenlabs" && c.Voice == "" {
			return errors.New("ElevenLabs voice ID is required")
		}
		if c.Provider == "chatterbox" && c.URL == "" {
			return errors.New("local Chatterbox URL is required")
		}
		if c.Provider != "say" && c.Provider != "chatterbox" && c.KeyEnv == "" {
			return errors.New("API key environment variable is required")
		}
	}
	return nil
}

// Synthesizer makes bounded, cancelable requests and never follows redirects.
type Synthesizer struct {
	HTTP   *http.Client
	Getenv func(string) string
}

func New() *Synthesizer {
	return &Synthesizer{HTTP: &http.Client{Timeout: 40 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, ResponseHeaderTimeout: 30 * time.Second}}, Getenv: os.Getenv}
}

// Synthesize returns WAV data. Neither credentials nor provider error bodies are returned.
func (s *Synthesizer) Synthesize(ctx context.Context, c Config, text string) ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if !c.Enabled {
		return nil, errors.New("speech is disabled")
	}
	if strings.TrimSpace(text) == "" || !utf8.ValidString(text) || utf8.RuneCountInString(text) > MaxTextRunes {
		return nil, errors.New("speech text must contain 1–5000 characters")
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	if c.Provider == "say" {
		return synthesizeSay(ctx, c.Voice, text)
	}
	endpoint, authHeader, token := "", "Authorization", ""
	if c.KeyEnv != "" {
		token = s.Getenv(c.KeyEnv)
	}
	if c.Provider != "chatterbox" && strings.TrimSpace(token) == "" {
		return nil, errors.New("speech API key environment variable is not set")
	}
	payload := map[string]any{}
	pcm := false
	switch c.Provider {
	case "elevenlabs":
		endpoint = "https://api.elevenlabs.io/v1/text-to-speech/" + url.PathEscape(c.Voice) + "?output_format=pcm_24000"
		authHeader = "xi-api-key"
		payload = map[string]any{"text": text, "model_id": defaultValue(c.Model, "eleven_multilingual_v2")}
		pcm = true
	case "fish":
		endpoint = "https://api.fish.audio/v1/tts"
		payload = map[string]any{"text": text, "format": "wav"}
		if c.Voice != "" {
			payload["reference_id"] = c.Voice
		}
	case "xai":
		endpoint = "https://api.x.ai/v1/tts"
		payload = map[string]any{"text": text, "voice_id": defaultValue(c.Voice, "eve"), "language": defaultValue(strings.Split(c.Language, "-")[0], "auto"), "output_format": map[string]any{"codec": "pcm", "sample_rate": 24000}}
		pcm = true
	case "chatterbox":
		endpoint = strings.TrimRight(c.URL, "/") + "/v1/audio/speech"
		payload = map[string]any{"input": text, "model": defaultValue(c.Model, "chatterbox"), "voice": defaultValue(c.Voice, "default"), "response_format": "wav"}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.New("invalid speech request")
	}
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("invalid speech endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "audio/wav, audio/pcm, application/octet-stream")
	if token != "" {
		if authHeader == "Authorization" {
			token = "Bearer " + token
		}
		req.Header.Set(authHeader, token)
	}
	if c.Provider == "fish" {
		req.Header.Set("model", defaultValue(c.Model, "s1"))
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("speech provider request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("speech provider returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxAudioBytes+1))
	if err != nil {
		return nil, errors.New("cannot read speech audio")
	}
	if len(data) == 0 || len(data) > MaxAudioBytes {
		return nil, errors.New("speech audio is empty or too large")
	}
	if pcm {
		return pcmWAV(data)
	}
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, errors.New("speech provider did not return WAV audio")
	}
	return data, nil
}

func defaultValue(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func pcmWAV(data []byte) ([]byte, error) {
	if len(data)%2 != 0 {
		return nil, errors.New("invalid PCM audio length")
	}
	b := make([]byte, 44+len(data))
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+len(data)))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], 24000)
	binary.LittleEndian.PutUint32(b[28:], 48000)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(len(data)))
	copy(b[44:], data)
	return b, nil
}

func synthesizeSay(ctx context.Context, voice, text string) ([]byte, error) {
	if runtime.GOOS != "darwin" {
		return nil, errors.New("macOS say is only available on macOS")
	}
	dir, err := os.MkdirTemp("", "bqbot-speech-")
	if err != nil {
		return nil, errors.New("cannot create temporary speech file")
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "speech.wav")
	args := []string{"-o", path, "--file-format=WAVE", "--data-format=LEI16@24000"}
	if voice != "" {
		args = append(args, "-v", voice)
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/say", args...)
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("macOS speech synthesis failed; check the installed voice")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot read synthesized speech")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxAudioBytes+1))
	if err != nil || len(b) == 0 || len(b) > MaxAudioBytes {
		return nil, errors.New("synthesized speech is empty or too large")
	}
	return b, nil
}
