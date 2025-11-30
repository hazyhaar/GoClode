// Package providers defines LLM provider interfaces and common utilities
package providers

import (
	"context"
)

// Provider is the interface all LLM providers must implement
type Provider interface {
	// ID returns the provider identifier
	ID() string

	// Name returns the human-readable provider name
	Name() string

	// Generate sends a prompt and returns the full response
	Generate(ctx context.Context, req *Request) (*Response, error)

	// Stream sends a prompt and streams the response
	Stream(ctx context.Context, req *Request) (<-chan StreamChunk, error)

	// Models returns available models for this provider
	Models() []string

	// IsAvailable checks if the provider is configured and available
	IsAvailable() bool
}

// Request represents a generation request
type Request struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream"`

	// Provider-specific options
	Options map[string]interface{} `json:"options,omitempty"`
}

// Message represents a chat message
type Message struct {
	Role    string `json:"role"` // system, user, assistant
	Content string `json:"content"`
}

// Response represents a generation response
type Response struct {
	ID       string `json:"id"`
	Model    string `json:"model"`
	Content  string `json:"content"`
	TokensIn  int   `json:"tokens_in"`
	TokensOut int   `json:"tokens_out"`
	Latency   int64 `json:"latency_ms"`

	// Raw response for debugging
	Raw interface{} `json:"raw,omitempty"`
}

// StreamChunk represents a streaming response chunk
type StreamChunk struct {
	Delta     string `json:"delta"`
	TokensIn  int    `json:"tokens_in,omitempty"`
	TokensOut int    `json:"tokens_out,omitempty"`
	Done      bool   `json:"done"`
	Error     error  `json:"error,omitempty"`
}

// ProviderConfig from database
type ProviderConfig struct {
	ID           string `json:"provider_id"`
	Name         string `json:"name"`
	BaseURL      string `json:"base_url"`
	APIKeyEnv    string `json:"api_key_env"`
	DefaultModel string `json:"default_model"`
	Enabled      bool   `json:"enabled"`
	Priority     int    `json:"priority"`
	RateLimitRPM int    `json:"rate_limit_rpm"`
}
