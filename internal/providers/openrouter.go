// Package providers - OpenRouter LLM provider
package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// OpenRouterProvider implements the Provider interface for OpenRouter API
type OpenRouterProvider struct {
	config *ProviderConfig
	client *http.Client
	apiKey string
}

// NewOpenRouterProvider creates a new OpenRouter provider
func NewOpenRouterProvider(config *ProviderConfig) *OpenRouterProvider {
	if config == nil {
		config = &ProviderConfig{
			ID:           "openrouter",
			Name:         "OpenRouter",
			BaseURL:      "https://openrouter.ai/api/v1",
			APIKeyEnv:    "OPENROUTER_API_KEY",
			DefaultModel: "meta-llama/llama-3.1-70b-instruct",
		}
	}

	return &OpenRouterProvider{
		config: config,
		client: &http.Client{
			Timeout: 5 * time.Minute,
		},
		apiKey: os.Getenv(config.APIKeyEnv),
	}
}

// ID returns the provider identifier
func (p *OpenRouterProvider) ID() string {
	return p.config.ID
}

// Name returns the human-readable name
func (p *OpenRouterProvider) Name() string {
	return p.config.Name
}

// Models returns available models
func (p *OpenRouterProvider) Models() []string {
	return []string{
		"meta-llama/llama-3.1-70b-instruct",
		"meta-llama/llama-3.1-8b-instruct",
		"anthropic/claude-3.5-sonnet",
		"openai/gpt-4o",
		"google/gemini-pro-1.5",
	}
}

// IsAvailable checks if the provider is configured
func (p *OpenRouterProvider) IsAvailable() bool {
	return p.apiKey != ""
}

// openrouterRequest is the OpenRouter API request format (OpenAI-compatible)
type openrouterRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream"`
}

// Generate sends a prompt and returns the full response
func (p *OpenRouterProvider) Generate(ctx context.Context, req *Request) (*Response, error) {
	if !p.IsAvailable() {
		return nil, fmt.Errorf("OpenRouter API key not configured (set %s)", p.config.APIKeyEnv)
	}

	model := req.Model
	if model == "" {
		model = p.config.DefaultModel
	}

	temp := req.Temperature
	if temp == 0 {
		temp = 0.7
	}

	orreq := &openrouterRequest{
		Model:       model,
		Messages:    req.Messages,
		Temperature: temp,
		MaxTokens:   req.MaxTokens,
		Stream:      false,
	}

	start := time.Now()
	body, err := json.Marshal(orreq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.config.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("HTTP-Referer", "https://github.com/anthropics/goclode")
	httpReq.Header.Set("X-Title", "GoClode")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	var orres cerebrasResponse // Same format as OpenAI
	if err := json.NewDecoder(resp.Body).Decode(&orres); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	content := ""
	if len(orres.Choices) > 0 {
		content = orres.Choices[0].Message.Content
	}

	return &Response{
		ID:        orres.ID,
		Model:     orres.Model,
		Content:   content,
		TokensIn:  orres.Usage.PromptTokens,
		TokensOut: orres.Usage.CompletionTokens,
		Latency:   time.Since(start).Milliseconds(),
		Raw:       orres,
	}, nil
}

// Stream sends a prompt and streams the response
func (p *OpenRouterProvider) Stream(ctx context.Context, req *Request) (<-chan StreamChunk, error) {
	if !p.IsAvailable() {
		return nil, fmt.Errorf("OpenRouter API key not configured (set %s)", p.config.APIKeyEnv)
	}

	model := req.Model
	if model == "" {
		model = p.config.DefaultModel
	}

	temp := req.Temperature
	if temp == 0 {
		temp = 0.7
	}

	orreq := &openrouterRequest{
		Model:       model,
		Messages:    req.Messages,
		Temperature: temp,
		MaxTokens:   req.MaxTokens,
		Stream:      true,
	}

	body, err := json.Marshal(orreq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.config.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("HTTP-Referer", "https://github.com/anthropics/goclode")
	httpReq.Header.Set("X-Title", "GoClode")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	ch := make(chan StreamChunk, 100)

	go func() {
		defer close(ch)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)

		var tokensIn, tokensOut int

		for scanner.Scan() {
			select {
			case <-ctx.Done():
				ch <- StreamChunk{Error: ctx.Err(), Done: true}
				return
			default:
			}

			line := scanner.Text()

			if !strings.HasPrefix(line, "data: ") {
				continue
			}

			data := strings.TrimPrefix(line, "data: ")

			if data == "[DONE]" {
				ch <- StreamChunk{Done: true, TokensIn: tokensIn, TokensOut: tokensOut}
				return
			}

			var chunk cerebrasStreamChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}

			if len(chunk.Choices) > 0 {
				delta := chunk.Choices[0].Delta.Content
				if delta != "" {
					ch <- StreamChunk{Delta: delta}
				}

				if chunk.Choices[0].FinishReason != "" {
					if chunk.Usage != nil {
						tokensIn = chunk.Usage.PromptTokens
						tokensOut = chunk.Usage.CompletionTokens
					}
				}
			}
		}

		if err := scanner.Err(); err != nil {
			ch <- StreamChunk{Error: err, Done: true}
		}
	}()

	return ch, nil
}
