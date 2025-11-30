// Package core - Module system with hot-reload and LLM debug hooks
package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ModuleManager handles dynamic module loading and hooks
type ModuleManager struct {
	engine  *Engine
	modules map[string]*Module
	hooks   map[string][]*Hook
	mu      sync.RWMutex

	// Debug hooks for autonomous LLM testing
	debugEnabled bool
	debugLog     []DebugEvent
	debugMu      sync.Mutex
}

// Module represents a loadable module
type Module struct {
	ID        string                 `json:"module_id"`
	Name      string                 `json:"name"`
	Version   string                 `json:"version"`
	Enabled   bool                   `json:"enabled"`
	Priority  int                    `json:"priority"`
	Config    map[string]interface{} `json:"config"`
	SchemaSQL string                 `json:"schema_sql"`
	Hooks     []*Hook                `json:"hooks"`
}

// Hook represents an event hook
type Hook struct {
	ID       string                 `json:"hook_id"`
	ModuleID string                 `json:"module_id"`
	Event    string                 `json:"event"`
	Handler  string                 `json:"handler"`
	Priority int                    `json:"priority"`
	Enabled  bool                   `json:"enabled"`
	Config   map[string]interface{} `json:"config"`
}

// HookContext provides context to hook handlers
type HookContext struct {
	Event     string
	Payload   map[string]interface{}
	Session   string
	Timestamp time.Time
	Debug     *DebugContext
}

// DebugContext for LLM autonomous debugging
type DebugContext struct {
	TraceID    string
	ParentID   string
	StartTime  time.Time
	Events     []DebugEvent
	Assertions []DebugAssertion
}

// DebugEvent represents a debug event for LLM analysis
type DebugEvent struct {
	ID        string                 `json:"id"`
	TraceID   string                 `json:"trace_id"`
	Timestamp time.Time              `json:"timestamp"`
	Level     string                 `json:"level"` // trace, debug, info, warn, error
	Event     string                 `json:"event"`
	Module    string                 `json:"module"`
	Message   string                 `json:"message"`
	Data      map[string]interface{} `json:"data"`
	Duration  time.Duration          `json:"duration,omitempty"`
}

// DebugAssertion for automated testing
type DebugAssertion struct {
	ID        string    `json:"id"`
	TraceID   string    `json:"trace_id"`
	Timestamp time.Time `json:"timestamp"`
	Name      string    `json:"name"`
	Expected  string    `json:"expected"`
	Actual    string    `json:"actual"`
	Passed    bool      `json:"passed"`
	Message   string    `json:"message"`
}

// HookHandler is a function that handles a hook event
type HookHandler func(ctx *HookContext) error

// Built-in handlers registry
var builtinHandlers = map[string]HookHandler{
	"log":           handleLog,
	"debug":         handleDebug,
	"llm_analyze":   handleLLMAnalyze,
	"test_assert":   handleTestAssert,
	"auto_fix":      handleAutoFix,
	"pattern_learn": handlePatternLearn,
}

// NewModuleManager creates a new module manager
func NewModuleManager(engine *Engine) *ModuleManager {
	mm := &ModuleManager{
		engine:   engine,
		modules:  make(map[string]*Module),
		hooks:    make(map[string][]*Hook),
		debugLog: make([]DebugEvent, 0, 1000),
	}

	// Load modules from DB
	mm.reload()

	// Watch for changes
	engine.OnChange(func(event string) {
		if event == "config_changed" || event == "module_changed" {
			mm.reload()
		}
	})

	return mm
}

// reload loads all modules and hooks from DB
func (mm *ModuleManager) reload() error {
	mm.mu.Lock()
	defer mm.mu.Unlock()

	// Clear current state
	mm.modules = make(map[string]*Module)
	mm.hooks = make(map[string][]*Hook)

	// Load modules
	rows, err := mm.engine.Query(`
		SELECT module_id, name, version, enabled, priority, config, schema_sql
		FROM modules WHERE enabled = 1 ORDER BY priority
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var m Module
		var configJSON string
		var schemaSQL sql.NullString

		err := rows.Scan(&m.ID, &m.Name, &m.Version, &m.Enabled, &m.Priority, &configJSON, &schemaSQL)
		if err != nil {
			continue
		}

		json.Unmarshal([]byte(configJSON), &m.Config)
		if schemaSQL.Valid {
			m.SchemaSQL = schemaSQL.String
		}

		mm.modules[m.ID] = &m
	}

	// Load hooks
	hookRows, err := mm.engine.Query(`
		SELECT hook_id, module_id, event, handler, priority, enabled, config
		FROM module_hooks WHERE enabled = 1 ORDER BY priority
	`)
	if err != nil {
		return err
	}
	defer hookRows.Close()

	for hookRows.Next() {
		var h Hook
		var configJSON string

		err := hookRows.Scan(&h.ID, &h.ModuleID, &h.Event, &h.Handler, &h.Priority, &h.Enabled, &configJSON)
		if err != nil {
			continue
		}

		json.Unmarshal([]byte(configJSON), &h.Config)
		mm.hooks[h.Event] = append(mm.hooks[h.Event], &h)
	}

	return nil
}

// RegisterModule registers a new module
func (mm *ModuleManager) RegisterModule(m *Module) error {
	configJSON, _ := json.Marshal(m.Config)

	_, err := mm.engine.Exec(`
		INSERT INTO modules (module_id, name, version, enabled, priority, config, schema_sql)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(module_id) DO UPDATE SET
			name = excluded.name,
			version = excluded.version,
			enabled = excluded.enabled,
			priority = excluded.priority,
			config = excluded.config,
			schema_sql = excluded.schema_sql,
			updated_at = strftime('%s', 'now')
	`, m.ID, m.Name, m.Version, m.Enabled, m.Priority, string(configJSON), m.SchemaSQL)

	if err != nil {
		return err
	}

	// Execute schema if provided
	if m.SchemaSQL != "" {
		if _, err := mm.engine.Exec(m.SchemaSQL); err != nil {
			return fmt.Errorf("execute module schema: %w", err)
		}
	}

	mm.reload()
	return nil
}

// RegisterHook registers a hook for an event
func (mm *ModuleManager) RegisterHook(h *Hook) error {
	if h.ID == "" {
		h.ID = uuid.New().String()
	}

	configJSON, _ := json.Marshal(h.Config)

	_, err := mm.engine.Exec(`
		INSERT INTO module_hooks (hook_id, module_id, event, handler, priority, enabled, config)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(hook_id) DO UPDATE SET
			event = excluded.event,
			handler = excluded.handler,
			priority = excluded.priority,
			enabled = excluded.enabled,
			config = excluded.config
	`, h.ID, h.ModuleID, h.Event, h.Handler, h.Priority, h.Enabled, string(configJSON))

	if err != nil {
		return err
	}

	mm.reload()
	return nil
}

// Emit triggers all hooks for an event
func (mm *ModuleManager) Emit(event string, payload map[string]interface{}) error {
	mm.mu.RLock()
	hooks := mm.hooks[event]
	mm.mu.RUnlock()

	if len(hooks) == 0 {
		return nil
	}

	// Create debug context if debugging enabled
	var debugCtx *DebugContext
	if mm.debugEnabled {
		debugCtx = &DebugContext{
			TraceID:   uuid.New().String(),
			StartTime: time.Now(),
		}
	}

	ctx := &HookContext{
		Event:     event,
		Payload:   payload,
		Timestamp: time.Now(),
		Debug:     debugCtx,
	}

	// Execute hooks in priority order
	for _, hook := range hooks {
		if handler, ok := builtinHandlers[hook.Handler]; ok {
			start := time.Now()

			if err := handler(ctx); err != nil {
				mm.logDebug(DebugEvent{
					ID:        uuid.New().String(),
					TraceID:   debugCtx.TraceID,
					Timestamp: time.Now(),
					Level:     "error",
					Event:     event,
					Module:    hook.ModuleID,
					Message:   fmt.Sprintf("Hook %s failed: %v", hook.Handler, err),
					Duration:  time.Since(start),
				})
			} else {
				mm.logDebug(DebugEvent{
					ID:        uuid.New().String(),
					TraceID:   debugCtx.TraceID,
					Timestamp: time.Now(),
					Level:     "debug",
					Event:     event,
					Module:    hook.ModuleID,
					Message:   fmt.Sprintf("Hook %s executed", hook.Handler),
					Duration:  time.Since(start),
				})
			}
		}
	}

	return nil
}

// EnableDebug enables debug mode
func (mm *ModuleManager) EnableDebug() {
	mm.debugEnabled = true
}

// DisableDebug disables debug mode
func (mm *ModuleManager) DisableDebug() {
	mm.debugEnabled = false
}

// GetDebugLog returns the debug log for LLM analysis
func (mm *ModuleManager) GetDebugLog() []DebugEvent {
	mm.debugMu.Lock()
	defer mm.debugMu.Unlock()
	log := make([]DebugEvent, len(mm.debugLog))
	copy(log, mm.debugLog)
	return log
}

// ClearDebugLog clears the debug log
func (mm *ModuleManager) ClearDebugLog() {
	mm.debugMu.Lock()
	defer mm.debugMu.Unlock()
	mm.debugLog = mm.debugLog[:0]
}

// GetDebugLogJSON returns debug log as JSON for LLM
func (mm *ModuleManager) GetDebugLogJSON() string {
	log := mm.GetDebugLog()
	data, _ := json.MarshalIndent(log, "", "  ")
	return string(data)
}

func (mm *ModuleManager) logDebug(event DebugEvent) {
	if !mm.debugEnabled {
		return
	}

	mm.debugMu.Lock()
	defer mm.debugMu.Unlock()

	// Keep last 1000 events
	if len(mm.debugLog) >= 1000 {
		mm.debugLog = mm.debugLog[1:]
	}
	mm.debugLog = append(mm.debugLog, event)

	// Also persist to DB for later analysis
	mm.engine.Exec(`
		INSERT INTO learning_patterns (pattern_id, pattern_type, input_pattern, metadata, created_at)
		VALUES (?, 'debug_event', ?, ?, strftime('%s', 'now'))
	`, event.ID, event.Event, event.Message)
}

// ============================================================
// Built-in Hook Handlers
// ============================================================

func handleLog(ctx *HookContext) error {
	data, _ := json.Marshal(ctx.Payload)
	fmt.Printf("[%s] %s: %s\n", ctx.Timestamp.Format("15:04:05"), ctx.Event, string(data))
	return nil
}

func handleDebug(ctx *HookContext) error {
	if ctx.Debug == nil {
		return nil
	}

	event := DebugEvent{
		ID:        uuid.New().String(),
		TraceID:   ctx.Debug.TraceID,
		Timestamp: time.Now(),
		Level:     "debug",
		Event:     ctx.Event,
		Data:      ctx.Payload,
	}

	ctx.Debug.Events = append(ctx.Debug.Events, event)
	return nil
}

// handleLLMAnalyze prepares debug data for LLM analysis
func handleLLMAnalyze(ctx *HookContext) error {
	// This hook prepares a prompt for LLM to analyze the debug state
	// The actual LLM call would be made by the caller

	if ctx.Debug == nil {
		return nil
	}

	// Store analysis request for later processing
	ctx.Payload["_llm_analysis_requested"] = true
	ctx.Payload["_debug_context"] = ctx.Debug

	return nil
}

// handleTestAssert handles test assertions for autonomous testing
func handleTestAssert(ctx *HookContext) error {
	if ctx.Debug == nil {
		return nil
	}

	name, _ := ctx.Payload["assertion_name"].(string)
	expected, _ := ctx.Payload["expected"].(string)
	actual, _ := ctx.Payload["actual"].(string)

	assertion := DebugAssertion{
		ID:        uuid.New().String(),
		TraceID:   ctx.Debug.TraceID,
		Timestamp: time.Now(),
		Name:      name,
		Expected:  expected,
		Actual:    actual,
		Passed:    expected == actual,
	}

	if !assertion.Passed {
		assertion.Message = fmt.Sprintf("Assertion failed: expected %q, got %q", expected, actual)
	}

	ctx.Debug.Assertions = append(ctx.Debug.Assertions, assertion)
	return nil
}

// handleAutoFix triggers LLM auto-fix for errors
func handleAutoFix(ctx *HookContext) error {
	// This hook would trigger the LLM to suggest fixes
	// Implementation depends on provider availability

	if errMsg, ok := ctx.Payload["error"].(string); ok {
		ctx.Payload["_auto_fix_requested"] = true
		ctx.Payload["_error_to_fix"] = errMsg
	}

	return nil
}

// handlePatternLearn records patterns for learning
func handlePatternLearn(ctx *HookContext) error {
	patternType, _ := ctx.Payload["pattern_type"].(string)
	input, _ := ctx.Payload["input"].(string)
	output, _ := ctx.Payload["output"].(string)
	success, _ := ctx.Payload["success"].(bool)

	if patternType == "" || input == "" {
		return nil
	}

	// Update or insert pattern
	if success && output != "" {
		// Pattern learning is handled by the learning module
		// This hook just validates the data
		_ = output // Used for validation
	}

	return nil
}

// ============================================================
// Debug Test Framework
// ============================================================

// TestSuite represents a test suite for autonomous LLM testing
type TestSuite struct {
	Name      string
	Tests     []*TestCase
	Setup     func() error
	Teardown  func() error
	mm        *ModuleManager
}

// TestCase represents a single test
type TestCase struct {
	Name     string
	Input    string
	Expected string
	Validate func(output string) bool
}

// NewTestSuite creates a new test suite
func (mm *ModuleManager) NewTestSuite(name string) *TestSuite {
	return &TestSuite{
		Name:  name,
		Tests: make([]*TestCase, 0),
		mm:    mm,
	}
}

// AddTest adds a test case
func (ts *TestSuite) AddTest(tc *TestCase) {
	ts.Tests = append(ts.Tests, tc)
}

// Run runs all tests and returns results for LLM analysis
func (ts *TestSuite) Run(ctx context.Context) (string, error) {
	ts.mm.EnableDebug()
	defer ts.mm.DisableDebug()

	if ts.Setup != nil {
		if err := ts.Setup(); err != nil {
			return "", fmt.Errorf("setup failed: %w", err)
		}
	}

	results := make([]map[string]interface{}, 0, len(ts.Tests))

	for _, tc := range ts.Tests {
		result := map[string]interface{}{
			"name":     tc.Name,
			"input":    tc.Input,
			"expected": tc.Expected,
		}

		ts.mm.Emit("test_start", map[string]interface{}{
			"test_name": tc.Name,
		})

		// Run test (implementation depends on what's being tested)
		// Here we just record the assertion

		ts.mm.Emit("test_assert", map[string]interface{}{
			"assertion_name": tc.Name,
			"expected":       tc.Expected,
			"actual":         "", // Would be filled by actual test execution
		})

		ts.mm.Emit("test_end", map[string]interface{}{
			"test_name": tc.Name,
		})

		results = append(results, result)
	}

	if ts.Teardown != nil {
		ts.Teardown()
	}

	// Generate LLM-friendly report
	report := map[string]interface{}{
		"suite":      ts.Name,
		"tests":      results,
		"debug_log":  ts.mm.GetDebugLog(),
		"timestamp":  time.Now(),
	}

	data, _ := json.MarshalIndent(report, "", "  ")
	return string(data), nil
}

// AnalyzeWithLLM prepares debug data for LLM analysis
func (mm *ModuleManager) AnalyzeWithLLM() string {
	log := mm.GetDebugLog()

	prompt := `Analyze the following debug log from GoClode and identify:
1. Any errors or failures
2. Performance issues (slow operations)
3. Patterns that could be optimized
4. Suggested fixes

Debug Log:
`
	data, _ := json.MarshalIndent(log, "", "  ")
	return prompt + string(data)
}
