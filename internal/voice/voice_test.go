package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/audio"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProviderRequests(t *testing.T) {
	wav, _ := pcmWAV(make([]byte, 480))
	for _, provider := range []string{"elevenlabs", "fish", "xai", "chatterbox"} {
		t.Run(provider, func(t *testing.T) {
			synth := New()
			synth.Getenv = func(string) string { return "secret-test" }
			synth.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != "POST" {
					t.Fatal(r.Method)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				auth := r.Header.Get("Authorization")
				if provider == "elevenlabs" {
					auth = r.Header.Get("xi-api-key")
					if !strings.HasSuffix(r.URL.Path, "/voice") || r.URL.Query().Get("output_format") != "pcm_24000" {
						t.Fatal(r.URL)
					}
				}
				if !strings.Contains(auth, "secret-test") {
					t.Fatal("missing authentication")
				}
				if provider == "chatterbox" && (body["input"] != "hello" || body["response_format"] != "wav") {
					t.Fatal(body)
				}
				if provider != "chatterbox" && body["text"] != "hello" {
					t.Fatal(body)
				}
				if provider == "fish" && (body["reference_id"] != "voice" || r.Header.Get("model") != "s1") {
					t.Fatal(body)
				}
				if provider == "xai" && (body["voice_id"] != "voice" || r.URL.Path != "/v1/tts") {
					t.Fatal(body)
				}
				data := wav
				if provider == "elevenlabs" || provider == "xai" {
					data = make([]byte, 480)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), Header: http.Header{}}, nil
			})
			cfg := Config{Enabled: true, Provider: provider, Voice: "voice", KeyEnv: "TTS_KEY"}
			if provider == "chatterbox" {
				cfg.URL = "http://127.0.0.1:8000"
			}
			data, err := synth.Synthesize(context.Background(), cfg, "hello")
			if err != nil {
				t.Fatal(err)
			}
			decoder, format, err := audio.NewDecoder(bytes.NewReader(data))
			if err != nil || format != "wav" || decoder.SampleRate() != 24000 {
				t.Fatalf("decoder=%v format=%s err=%v", decoder, format, err)
			}
		})
	}
}

func TestSpeechBoundaries(t *testing.T) {
	for _, c := range []Config{
		{Enabled: true, Provider: "unknown"},
		{Enabled: true, Provider: "chatterbox", URL: "http://example.com"},
		{Enabled: true, Provider: "chatterbox", URL: "http://user:pass@127.0.0.1"},
		{Enabled: true, Provider: "chatterbox", URL: "http://127.0.0.1?secret=x"},
		{Enabled: true, Provider: "elevenlabs", Voice: "voice", KeyEnv: "literal-secret-value"},
	} {
		if c.Validate() == nil {
			t.Fatalf("accepted invalid config %+v", c)
		}
	}
	s := New()
	s.Getenv = func(string) string { return "secret-test" }
	s.HTTP.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader("secret-test private text"))}, nil
	})
	c := Config{Enabled: true, Provider: "xai", KeyEnv: "TTS_KEY"}
	_, err := s.Synthesize(context.Background(), c, "hello")
	if err == nil || strings.Contains(err.Error(), "secret-test") {
		t.Fatal(err)
	}
	c.Enabled = false
	if _, err = s.Synthesize(context.Background(), c, "hello"); err == nil {
		t.Fatal("disabled speech allowed")
	}
	c.Enabled = true
	if _, err = s.Synthesize(context.Background(), c, strings.Repeat("字", MaxTextRunes+1)); err == nil {
		t.Fatal("oversized text accepted")
	}
}

func TestPlayerCancelAndClose(t *testing.T) {
	var p Player
	started := make(chan struct{})
	canceled := make(chan struct{})
	if err := p.Start(func(ctx context.Context) ([]byte, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return nil, ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	p.Stop()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("synthesis was not canceled")
	}
	if p.Status().State != "idle" {
		t.Fatal(p.Status())
	}
	p.Close()
	if err := p.Start(func(context.Context) ([]byte, error) { t.Fatal("closed player invoked fetch"); return nil, nil }); err == nil {
		t.Fatal("closed player accepted speech")
	}
}

func TestReplacementCancelsStaleSynthesis(t *testing.T) {
	var p Player
	defer p.Close()
	first := make(chan struct{})
	second := make(chan struct{})
	_ = p.Start(func(ctx context.Context) ([]byte, error) { <-ctx.Done(); close(first); return nil, ctx.Err() })
	_ = p.Start(func(ctx context.Context) ([]byte, error) { close(second); <-ctx.Done(); return nil, ctx.Err() })
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("old synthesis survived")
	}
	<-second
	if p.Status().State != "synthesizing" {
		t.Fatal(p.Status())
	}
}
