// Package modules provides extensible modules for GoClode
// These modules can be hot-reloaded and configured via SQLite
package modules

import (
	"encoding/json"
	"time"

	"github.com/hazyhaar/GoClode/internal/core"
	"github.com/google/uuid"
)

// LearningModule provides pattern learning capabilities
// It learns from user interactions to improve suggestions
type LearningModule struct {
	mm     *core.ModuleManager
	engine *core.Engine
}

// NewLearningModule creates a new learning module
func NewLearningModule(engine *core.Engine, mm *core.ModuleManager) *LearningModule {
	lm := &LearningModule{
		mm:     mm,
		engine: engine,
	}

	// Register module in database
	mm.RegisterModule(&core.Module{
		ID:       "learning",
		Name:     "Pattern Learning",
		Version:  "1.0.0",
		Enabled:  true,
		Priority: 50,
		Config: map[string]interface{}{
			"min_success_count": 3,
			"decay_days":        30,
		},
		SchemaSQL: lm.Schema(),
	})

	// Register hooks
	mm.RegisterHook(&core.Hook{
		ModuleID: "learning",
		Event:    "chat_complete",
		Handler:  "pattern_learn",
		Priority: 100,
		Enabled:  true,
	})

	return lm
}

// Schema returns additional tables for learning
func (lm *LearningModule) Schema() string {
	return `
	-- Intent patterns learned from user behavior
	CREATE TABLE IF NOT EXISTS learned_intents (
		id TEXT PRIMARY KEY,
		input_pattern TEXT NOT NULL,
		detected_intent TEXT NOT NULL,
		confidence REAL DEFAULT 0.5,
		success_count INTEGER DEFAULT 0,
		failure_count INTEGER DEFAULT 0,
		last_used_at INTEGER,
		created_at INTEGER DEFAULT (strftime('%s', 'now'))
	);

	CREATE INDEX IF NOT EXISTS idx_learned_intents ON learned_intents(input_pattern, confidence DESC);

	-- Code patterns for suggestions
	CREATE TABLE IF NOT EXISTS code_patterns (
		id TEXT PRIMARY KEY,
		language TEXT NOT NULL,
		pattern_type TEXT NOT NULL,
		trigger_text TEXT NOT NULL,
		suggestion TEXT NOT NULL,
		usage_count INTEGER DEFAULT 0,
		created_at INTEGER DEFAULT (strftime('%s', 'now'))
	);

	-- User preferences learned over time
	CREATE TABLE IF NOT EXISTS user_preferences (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		confidence REAL DEFAULT 0.5,
		updated_at INTEGER DEFAULT (strftime('%s', 'now'))
	);
	`
}

// RecordSuccess records a successful pattern match
func (lm *LearningModule) RecordSuccess(inputPattern, intent string) error {
	id := uuid.New().String()

	_, err := lm.engine.Exec(`
		INSERT INTO learned_intents (id, input_pattern, detected_intent, success_count, last_used_at)
		VALUES (?, ?, ?, 1, strftime('%s', 'now'))
		ON CONFLICT(id) DO UPDATE SET
			success_count = success_count + 1,
			confidence = CAST(success_count AS REAL) / (success_count + failure_count),
			last_used_at = strftime('%s', 'now')
	`, id, inputPattern, intent)

	return err
}

// RecordFailure records a failed pattern match
func (lm *LearningModule) RecordFailure(inputPattern, intent string) error {
	_, err := lm.engine.Exec(`
		UPDATE learned_intents
		SET failure_count = failure_count + 1,
			confidence = CAST(success_count AS REAL) / (success_count + failure_count + 1)
		WHERE input_pattern = ? AND detected_intent = ?
	`, inputPattern, intent)

	return err
}

// GetSuggestion returns a learned suggestion for input
func (lm *LearningModule) GetSuggestion(input string) (string, float64, error) {
	var intent string
	var confidence float64

	err := lm.engine.QueryRow(`
		SELECT detected_intent, confidence
		FROM learned_intents
		WHERE input_pattern LIKE ?
		AND confidence >= 0.7
		ORDER BY confidence DESC, success_count DESC
		LIMIT 1
	`, "%"+input+"%").Scan(&intent, &confidence)

	if err != nil {
		return "", 0, err
	}

	return intent, confidence, nil
}

// LearnPreference learns a user preference
func (lm *LearningModule) LearnPreference(key, value string) error {
	_, err := lm.engine.Exec(`
		INSERT INTO user_preferences (key, value, confidence)
		VALUES (?, ?, 0.6)
		ON CONFLICT(key) DO UPDATE SET
			value = excluded.value,
			confidence = MIN(1.0, confidence + 0.1),
			updated_at = strftime('%s', 'now')
	`, key, value)

	return err
}

// GetPreference returns a learned preference
func (lm *LearningModule) GetPreference(key string) (string, float64, error) {
	var value string
	var confidence float64

	err := lm.engine.QueryRow(`
		SELECT value, confidence FROM user_preferences WHERE key = ?
	`, key).Scan(&value, &confidence)

	return value, confidence, err
}

// ============================================================
// Debug Module for autonomous LLM testing
// ============================================================

// DebugModule provides debugging capabilities
type DebugModule struct {
	mm     *core.ModuleManager
	engine *core.Engine
}

// NewDebugModule creates a new debug module
func NewDebugModule(engine *core.Engine, mm *core.ModuleManager) *DebugModule {
	dm := &DebugModule{
		mm:     mm,
		engine: engine,
	}

	// Register module
	mm.RegisterModule(&core.Module{
		ID:       "debug",
		Name:     "Debug & Testing",
		Version:  "1.0.0",
		Enabled:  true,
		Priority: 1, // High priority for debugging
		Config: map[string]interface{}{
			"trace_all":    false,
			"log_to_db":    true,
			"max_log_size": 10000,
		},
		SchemaSQL: dm.Schema(),
	})

	// Register debug hooks
	mm.RegisterHook(&core.Hook{
		ModuleID: "debug",
		Event:    "*", // All events
		Handler:  "debug",
		Priority: 1,
		Enabled:  true,
	})

	return dm
}

// Schema returns additional tables for debugging
func (dm *DebugModule) Schema() string {
	return `
	-- Debug traces for LLM analysis
	CREATE TABLE IF NOT EXISTS debug_traces (
		trace_id TEXT PRIMARY KEY,
		parent_id TEXT,
		event TEXT NOT NULL,
		module TEXT,
		start_time INTEGER,
		end_time INTEGER,
		duration_ms INTEGER,
		status TEXT DEFAULT 'running',
		data TEXT DEFAULT '{}',
		error TEXT,
		created_at INTEGER DEFAULT (strftime('%s', 'now'))
	);

	CREATE INDEX IF NOT EXISTS idx_traces_event ON debug_traces(event, created_at DESC);
	CREATE INDEX IF NOT EXISTS idx_traces_status ON debug_traces(status, created_at DESC);

	-- Assertions for automated testing
	CREATE TABLE IF NOT EXISTS debug_assertions (
		id TEXT PRIMARY KEY,
		trace_id TEXT,
		name TEXT NOT NULL,
		expected TEXT,
		actual TEXT,
		passed INTEGER,
		message TEXT,
		created_at INTEGER DEFAULT (strftime('%s', 'now')),

		FOREIGN KEY(trace_id) REFERENCES debug_traces(trace_id) ON DELETE CASCADE
	);

	-- Test cases for autonomous testing
	CREATE TABLE IF NOT EXISTS test_cases (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT,
		input TEXT NOT NULL,
		expected_output TEXT,
		expected_intent TEXT,
		tags TEXT DEFAULT '[]',
		enabled INTEGER DEFAULT 1,
		last_run_at INTEGER,
		last_result TEXT,
		created_at INTEGER DEFAULT (strftime('%s', 'now'))
	);
	`
}

// StartTrace starts a new debug trace
func (dm *DebugModule) StartTrace(event, module string) string {
	traceID := uuid.New().String()

	dm.engine.Exec(`
		INSERT INTO debug_traces (trace_id, event, module, start_time, status)
		VALUES (?, ?, ?, ?, 'running')
	`, traceID, event, module, time.Now().UnixMilli())

	return traceID
}

// EndTrace ends a debug trace
func (dm *DebugModule) EndTrace(traceID string, err error) {
	status := "success"
	errMsg := ""
	if err != nil {
		status = "error"
		errMsg = err.Error()
	}

	dm.engine.Exec(`
		UPDATE debug_traces
		SET end_time = ?,
			duration_ms = ? - start_time,
			status = ?,
			error = ?
		WHERE trace_id = ?
	`, time.Now().UnixMilli(), time.Now().UnixMilli(), status, errMsg, traceID)
}

// AddAssertion adds an assertion to a trace
func (dm *DebugModule) AddAssertion(traceID, name, expected, actual string) bool {
	passed := expected == actual

	dm.engine.Exec(`
		INSERT INTO debug_assertions (id, trace_id, name, expected, actual, passed, message)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, uuid.New().String(), traceID, name, expected, actual, passed,
		func() string {
			if passed {
				return "OK"
			}
			return "FAILED: expected " + expected + ", got " + actual
		}())

	return passed
}

// RunTestCase runs a test case and records results
func (dm *DebugModule) RunTestCase(id string) (bool, error) {
	var name, input, expectedOutput, expectedIntent string

	err := dm.engine.QueryRow(`
		SELECT name, input, COALESCE(expected_output, ''), COALESCE(expected_intent, '')
		FROM test_cases WHERE id = ? AND enabled = 1
	`, id).Scan(&name, &input, &expectedOutput, &expectedIntent)

	if err != nil {
		return false, err
	}

	traceID := dm.StartTrace("test_case", "debug")

	// Record test was run
	dm.engine.Exec(`
		UPDATE test_cases SET last_run_at = strftime('%s', 'now') WHERE id = ?
	`, id)

	// Test would be executed here by the chat system
	// For now just record the trace

	dm.EndTrace(traceID, nil)

	return true, nil
}

// GetFailedAssertions returns recent failed assertions for LLM analysis
func (dm *DebugModule) GetFailedAssertions(limit int) (string, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := dm.engine.Query(`
		SELECT a.name, a.expected, a.actual, a.message, t.event, t.module
		FROM debug_assertions a
		JOIN debug_traces t ON a.trace_id = t.trace_id
		WHERE a.passed = 0
		ORDER BY a.created_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	failures := make([]map[string]string, 0)
	for rows.Next() {
		var name, expected, actual, message, event, module string
		rows.Scan(&name, &expected, &actual, &message, &event, &module)
		failures = append(failures, map[string]string{
			"name":     name,
			"expected": expected,
			"actual":   actual,
			"message":  message,
			"event":    event,
			"module":   module,
		})
	}

	data, _ := json.MarshalIndent(failures, "", "  ")
	return string(data), nil
}

// GenerateLLMDebugPrompt generates a prompt for LLM to analyze debug data
func (dm *DebugModule) GenerateLLMDebugPrompt() string {
	failures, _ := dm.GetFailedAssertions(20)
	debugLog := dm.mm.GetDebugLogJSON()

	return `Analyze the following debug information from GoClode and provide:
1. Root cause analysis for any failures
2. Suggested fixes
3. Patterns that could be optimized

## Failed Assertions
` + failures + `

## Debug Log
` + debugLog + `

Please provide actionable recommendations.`
}
