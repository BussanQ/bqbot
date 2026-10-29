package driver

import (
	"context"
	"encoding/json"
	"errors"
)

// ApprovalRequest asks the person to authorize a tool or answer a question.
type ApprovalRequest struct {
	ID       string          `json:"id"`
	Kind     string          `json:"kind"` // tool or question
	Tool     string          `json:"tool,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
	Question string          `json:"question,omitempty"`
	Options  []string        `json:"options,omitempty"`
}

// ApprovalDecision is a single-use answer to an approval request.
type ApprovalDecision struct {
	Outcome string `json:"outcome"` // allow, deny, answer
	Answer  string `json:"answer,omitempty"`
}

type ApproveFunc func(context.Context, ApprovalRequest) (ApprovalDecision, error)

// Attachment holds an image as base64; MIME is validated before execution.
type Attachment struct {
	Name string `json:"name"`
	MIME string `json:"mime"`
	Data string `json:"data"`
}

// HistoryMessage is replayed when the provider has lost a session.
type HistoryMessage struct {
	Role        string       `json:"role"`
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// Model is an entry in an engine's model catalog.
type Model struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Engine  string   `json:"engine"`
	Efforts []string `json:"efforts,omitempty"`
}

// EngineSettings configure an engine without putting credentials in UI responses.
type EngineSettings struct {
	Path      string   `json:"path,omitempty"`
	BaseURL   string   `json:"base_url,omitempty"`
	APIKeyEnv string   `json:"api_key_env,omitempty"`
	Models    []string `json:"models,omitempty"`
}

// ErrSessionExpired means a provider rejected its continuation before producing output.
var ErrSessionExpired = errors.New("session expired")
