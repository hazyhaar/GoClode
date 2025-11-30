// Package session manages conversation sessions with SQLite persistence
package session

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/anthropics/goclode/internal/core"
	"github.com/anthropics/goclode/internal/providers"
	"github.com/google/uuid"
)

// Manager handles session lifecycle
type Manager struct {
	engine    *core.Engine
	sessionID string
	provider  string
}

// Session represents a conversation session
type Session struct {
	ID           string            `json:"session_id"`
	CreatedAt    time.Time         `json:"created_at"`
	LastActiveAt time.Time         `json:"last_active_at"`
	GitBranch    string            `json:"git_branch,omitempty"`
	GitCommit    string            `json:"git_commit_start,omitempty"`
	ProviderID   string            `json:"provider_id"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// Message represents a conversation message
type Message struct {
	ID        string    `json:"message_id"`
	SessionID string    `json:"session_id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Provider  string    `json:"provider_id,omitempty"`
	Model     string    `json:"model,omitempty"`
	TokensIn  int       `json:"tokens_in"`
	TokensOut int       `json:"tokens_out"`
	LatencyMs int       `json:"latency_ms"`
	CreatedAt time.Time `json:"created_at"`
}

// NewManager creates a new session manager
func NewManager(engine *core.Engine) *Manager {
	return &Manager{
		engine: engine,
	}
}

// Create creates a new session
func (m *Manager) Create(providerID string) (*Session, error) {
	sessionID := uuid.New().String()

	_, err := m.engine.Exec(`
		INSERT INTO sessions (session_id, provider_id)
		VALUES (?, ?)
	`, sessionID, providerID)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	m.sessionID = sessionID
	m.provider = providerID

	return &Session{
		ID:           sessionID,
		CreatedAt:    time.Now(),
		LastActiveAt: time.Now(),
		ProviderID:   providerID,
	}, nil
}

// Current returns the current session ID
func (m *Manager) Current() string {
	return m.sessionID
}

// SetSession sets the current session
func (m *Manager) SetSession(sessionID string) error {
	// Verify session exists
	var id string
	err := m.engine.QueryRow("SELECT session_id FROM sessions WHERE session_id = ?", sessionID).Scan(&id)
	if err == sql.ErrNoRows {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	if err != nil {
		return err
	}

	m.sessionID = sessionID
	return nil
}

// AddMessage adds a message to the current session
func (m *Manager) AddMessage(role, content string, resp *providers.Response) error {
	if m.sessionID == "" {
		return fmt.Errorf("no active session")
	}

	messageID := uuid.New().String()
	var tokensIn, tokensOut, latencyMs int
	var model, providerID string

	if resp != nil {
		tokensIn = resp.TokensIn
		tokensOut = resp.TokensOut
		latencyMs = int(resp.Latency)
		model = resp.Model
	}

	if m.provider != "" {
		providerID = m.provider
	}

	_, err := m.engine.Exec(`
		INSERT INTO messages (message_id, session_id, role, content, provider_id, model, tokens_in, tokens_out, latency_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, messageID, m.sessionID, role, content, providerID, model, tokensIn, tokensOut, latencyMs)

	if err != nil {
		return fmt.Errorf("add message: %w", err)
	}

	// Update session last active
	_, _ = m.engine.Exec(`
		UPDATE sessions SET last_active_at = strftime('%s', 'now') WHERE session_id = ?
	`, m.sessionID)

	return nil
}

// GetMessages returns all messages for the current session
func (m *Manager) GetMessages(limit int) ([]Message, error) {
	if m.sessionID == "" {
		return nil, fmt.Errorf("no active session")
	}

	if limit <= 0 {
		limit = 100
	}

	rows, err := m.engine.Query(`
		SELECT message_id, session_id, role, content,
			   COALESCE(provider_id, ''), COALESCE(model, ''),
			   tokens_in, tokens_out, latency_ms, created_at
		FROM messages
		WHERE session_id = ?
		ORDER BY created_at ASC
		LIMIT ?
	`, m.sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	messages := make([]Message, 0)
	for rows.Next() {
		var msg Message
		var createdAt int64

		err := rows.Scan(&msg.ID, &msg.SessionID, &msg.Role, &msg.Content,
			&msg.Provider, &msg.Model, &msg.TokensIn, &msg.TokensOut, &msg.LatencyMs, &createdAt)
		if err != nil {
			continue
		}
		msg.CreatedAt = time.Unix(createdAt, 0)
		messages = append(messages, msg)
	}

	return messages, nil
}

// GetContextMessages returns recent messages for LLM context
func (m *Manager) GetContextMessages(maxMessages int) ([]providers.Message, error) {
	messages, err := m.GetMessages(maxMessages)
	if err != nil {
		return nil, err
	}

	result := make([]providers.Message, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == "system" || msg.Role == "user" || msg.Role == "assistant" {
			result = append(result, providers.Message{
				Role:    msg.Role,
				Content: msg.Content,
			})
		}
	}

	return result, nil
}

// RecordFileChange records a file modification
func (m *Manager) RecordFileChange(filePath, operation, contentBefore, contentAfter, diff string) error {
	if m.sessionID == "" {
		return fmt.Errorf("no active session")
	}

	fileID := uuid.New().String()

	_, err := m.engine.Exec(`
		INSERT INTO files_modified (file_id, session_id, file_path, operation, content_before, content_after, diff)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, fileID, m.sessionID, filePath, operation, contentBefore, contentAfter, diff)

	return err
}

// RecordGitCommit records a git commit
func (m *Manager) RecordGitCommit(gitHash, message string, filesChanged int) error {
	if m.sessionID == "" {
		return fmt.Errorf("no active session")
	}

	commitID := uuid.New().String()

	_, err := m.engine.Exec(`
		INSERT INTO git_commits (commit_id, session_id, git_hash, commit_message, files_changed)
		VALUES (?, ?, ?, ?, ?)
	`, commitID, m.sessionID, gitHash, message, filesChanged)

	return err
}

// SetProvider changes the current provider
func (m *Manager) SetProvider(providerID string) error {
	m.provider = providerID

	if m.sessionID != "" {
		_, err := m.engine.Exec(`
			UPDATE sessions SET provider_id = ? WHERE session_id = ?
		`, providerID, m.sessionID)
		return err
	}

	return nil
}

// GetStats returns session statistics
func (m *Manager) GetStats() (map[string]interface{}, error) {
	if m.sessionID == "" {
		return nil, fmt.Errorf("no active session")
	}

	stats := make(map[string]interface{})

	// Message count
	var msgCount int
	m.engine.QueryRow("SELECT COUNT(*) FROM messages WHERE session_id = ?", m.sessionID).Scan(&msgCount)
	stats["messages"] = msgCount

	// Token totals
	var tokensIn, tokensOut int
	m.engine.QueryRow(`
		SELECT COALESCE(SUM(tokens_in), 0), COALESCE(SUM(tokens_out), 0)
		FROM messages WHERE session_id = ?
	`, m.sessionID).Scan(&tokensIn, &tokensOut)
	stats["tokens_in"] = tokensIn
	stats["tokens_out"] = tokensOut

	// File changes
	var fileCount int
	m.engine.QueryRow("SELECT COUNT(*) FROM files_modified WHERE session_id = ?", m.sessionID).Scan(&fileCount)
	stats["files_modified"] = fileCount

	// Commits
	var commitCount int
	m.engine.QueryRow("SELECT COUNT(*) FROM git_commits WHERE session_id = ?", m.sessionID).Scan(&commitCount)
	stats["commits"] = commitCount

	return stats, nil
}

// ListSessions returns recent sessions
func (m *Manager) ListSessions(limit int) ([]Session, error) {
	if limit <= 0 {
		limit = 10
	}

	rows, err := m.engine.Query(`
		SELECT session_id, created_at, last_active_at,
			   COALESCE(git_branch, ''), COALESCE(git_commit_start, ''),
			   COALESCE(provider_id, 'cerebras'), COALESCE(metadata, '{}')
		FROM sessions
		ORDER BY last_active_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sessions := make([]Session, 0)
	for rows.Next() {
		var s Session
		var createdAt, lastActiveAt int64
		var metadataJSON string

		err := rows.Scan(&s.ID, &createdAt, &lastActiveAt, &s.GitBranch, &s.GitCommit, &s.ProviderID, &metadataJSON)
		if err != nil {
			continue
		}
		s.CreatedAt = time.Unix(createdAt, 0)
		s.LastActiveAt = time.Unix(lastActiveAt, 0)
		json.Unmarshal([]byte(metadataJSON), &s.Metadata)
		sessions = append(sessions, s)
	}

	return sessions, nil
}

// RecordFeedback records user feedback on a message
func (m *Manager) RecordFeedback(messageID string, rating int, feedbackType, content string) error {
	feedbackID := uuid.New().String()

	_, err := m.engine.Exec(`
		INSERT INTO feedback (feedback_id, message_id, rating, feedback_type, content)
		VALUES (?, ?, ?, ?, ?)
	`, feedbackID, messageID, rating, feedbackType, content)

	return err
}
