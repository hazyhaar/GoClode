// Package providers - Cerebras LLM provider with SSE streaming
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

// CerebrasProvider implements the Provider interface for Cerebras API
type CerebrasProvider struct {
	config  *ProviderConfig
	client  *http.Client
	apiKey  string
}

// NewCerebrasProvider creates a new Cerebras provider
func NewCerebrasProvider(config *ProviderConfig) *CerebrasProvider {
	if config == nil {
		config = &ProviderConfig{
			ID:           "cerebras",
			Name:         "Cerebras",
			BaseURL:      "https://api.cerebras.ai/v1",
			APIKeyEnv:    "CEREBRAS_API_KEY",
			DefaultModel: "llama-3.3-70b",
		}
	}

	return &CerebrasProvider{
		config: config,
		client: &http.Client{
			Timeout: 5 * time.Minute, // Long timeout for streaming
		},
		apiKey: os.Getenv(config.APIKeyEnv),
	}
}

// ID returns the provider identifier
func (p *CerebrasProvider) ID() string {
	return p.config.ID
}

// Name returns the human-readable name
func (p *CerebrasProvider) Name() string {
	return p.config.Name
}

// Models returns available models
func (p *CerebrasProvider) Models() []string {
	return []string{
		"llama-3.3-70b",
		"llama3.1-8b",
		"llama3.1-70b",
	}
}

// IsAvailable checks if the provider is configured
func (p *CerebrasProvider) IsAvailable() bool {
	return p.apiKey != ""
}

// cerebrasRequest is the Cerebras API request format
type cerebrasRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream"`
}

// cerebrasResponse is the Cerebras API response format
type cerebrasResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// cerebrasStreamChunk is the SSE chunk format
type cerebrasStreamChunk struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role    string `json:"role,omitempty"`
			Content string `json:"content,omitempty"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason,omitempty"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage,omitempty"`
}

// Generate sends a prompt and returns the full response
func (p *CerebrasProvider) Generate(ctx context.Context, req *Request) (*Response, error) {
	if !p.IsAvailable() {
		return nil, fmt.Errorf("Cerebras API key not configured (set %s)", p.config.APIKeyEnv)
	}

	model := req.Model
	if model == "" {
		model = p.config.DefaultModel
	}

	temp := req.Temperature
	if temp == 0 {
		temp = 0.7
	}

	cereq := &cerebrasRequest{
		Model:       model,
		Messages:    req.Messages,
		Temperature: temp,
		MaxTokens:   req.MaxTokens,
		Stream:      false,
	}

	start := time.Now()
	body, err := json.Marshal(cereq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.config.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	var ceres cerebrasResponse
	if err := json.NewDecoder(resp.Body).Decode(&ceres); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	content := ""
	if len(ceres.Choices) > 0 {
		content = ceres.Choices[0].Message.Content
	}

	return &Response{
		ID:        ceres.ID,
		Model:     ceres.Model,
		Content:   content,
		TokensIn:  ceres.Usage.PromptTokens,
		TokensOut: ceres.Usage.CompletionTokens,
		Latency:   time.Since(start).Milliseconds(),
		Raw:       ceres,
	}, nil
}

// Stream sends a prompt and streams the response
func (p *CerebrasProvider) Stream(ctx context.Context, req *Request) (<-chan StreamChunk, error) {
	if !p.IsAvailable() {
		return nil, fmt.Errorf("Cerebras API key not configured (set %s)", p.config.APIKeyEnv)
	}

	model := req.Model
	if model == "" {
		model = p.config.DefaultModel
	}

	temp := req.Temperature
	if temp == 0 {
		temp = 0.7
	}

	cereq := &cerebrasRequest{
		Model:       model,
		Messages:    req.Messages,
		Temperature: temp,
		MaxTokens:   req.MaxTokens,
		Stream:      true,
	}

	body, err := json.Marshal(cereq)
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
		// Increase buffer size for large responses
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

			// SSE format: "data: {...}"
			if !strings.HasPrefix(line, "data: ") {
				continue
			}

			data := strings.TrimPrefix(line, "data: ")

			// End of stream
			if data == "[DONE]" {
				ch <- StreamChunk{Done: true, TokensIn: tokensIn, TokensOut: tokensOut}
				return
			}

			var chunk cerebrasStreamChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}

			// Extract content delta
			if len(chunk.Choices) > 0 {
				delta := chunk.Choices[0].Delta.Content
				if delta != "" {
					ch <- StreamChunk{Delta: delta}
				}

				// Check for finish
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
