package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	"bqbot/internal/voice"
)

// VoiceState deliberately exposes only whether the referenced API key is set.
type VoiceState struct {
	Settings   voice.Config `json:"settings"`
	Configured bool         `json:"configured"`
}

func (s *Server) voiceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/voice", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.h.Store.VoiceSettings()
		result(w, VoiceState{Settings: v, Configured: v.KeyEnv != "" && os.Getenv(v.KeyEnv) != ""}, err)
	})
	mux.HandleFunc("PUT /v1/voice", func(w http.ResponseWriter, r *http.Request) {
		var v voice.Config
		if !decode(w, r, &v) {
			return
		}
		if err := v.Validate(); err != nil {
			fail(w, invalid(err.Error()))
			return
		}
		result(w, struct{}{}, s.h.Store.SaveVoiceSettings(v))
	})
	synth := voice.New()
	mux.HandleFunc("POST /v1/voice/synthesize", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		var v struct {
			Text string `json:"text"`
		}
		if !decode(w, r, &v) {
			return
		}
		settings, err := s.h.Store.VoiceSettings()
		if err != nil {
			fail(w, err)
			return
		}
		data, err := synth.Synthesize(ctx, settings, v.Text)
		if err != nil {
			fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "audio/wav")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	})
}

func (c *Client) Voice() (v VoiceState, err error) {
	err = c.do("GET", "/v1/voice", nil, &v)
	return
}
func (c *Client) SaveVoice(v voice.Config) error { return c.do("PUT", "/v1/voice", v, nil) }

// SynthesizeVoice preserves cancellation when the desktop stops playback during synthesis.
func (c *Client) SynthesizeVoice(ctx context.Context, text string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	// Speech synthesis can take longer than the normal API's header timeout.
	client := *c
	httpClient := *c.http
	if transport, ok := c.http.Transport.(*http.Transport); ok {
		transport = transport.Clone()
		transport.ResponseHeaderTimeout = 45 * time.Second
		defer transport.CloseIdleConnections()
		httpClient.Transport = transport
	}
	client.http = &httpClient
	resp, err := client.request(ctx, "POST", "/v1/voice/synthesize", map[string]string{"text": text})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, voice.MaxAudioBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > voice.MaxAudioBytes {
		return nil, errors.New("speech audio is too large")
	}
	return data, nil
}
