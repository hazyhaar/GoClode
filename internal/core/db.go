// Package core provides the SQL-driven dynamic engine for GoClode.
// This is the heart of the system - everything is configurable via SQLite.
package core

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	_ "modernc.org/sqlite"
)

// Engine is the core SQL engine with hot-reload capabilities.
// All configuration, modules, and learning data lives in SQLite.
type Engine struct {
	db       *sql.DB
	dbPath   string
	mu       sync.RWMutex
	watchers []func(event string)
	ctx      context.Context
	cancel   context.CancelFunc

	// Hot-reload channels
	configVersion int64
	reloadCh      chan struct{}
}

// NewEngine creates a new SQL engine with the database at the given path.
// If path is empty, creates a session-based DB in .goclode/
func NewEngine(dbPath string) (*Engine, error) {
	if dbPath == "" {
		// Create session-based DB
		goclodeDir := ".goclode"
		if err := os.MkdirAll(goclodeDir, 0755); err != nil {
			return nil, fmt.Errorf("create .goclode dir: %w", err)
		}
		timestamp := time.Now().Format("2006-01-02_15-04-05")
		dbPath = filepath.Join(goclodeDir, fmt.Sprintf("session_%s.db", timestamp))
	}

	// Open with WAL mode for concurrent reads
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Test connection
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	e := &Engine{
		db:       db,
		dbPath:   dbPath,
		ctx:      ctx,
		cancel:   cancel,
		reloadCh: make(chan struct{}, 1),
	}

	// Initialize schema
	if err := e.initSchema(); err != nil {
		return nil, fmt.Errorf("init schema: %w", err)
	}

	// Start config watcher
	go e.watchConfig()

	return e, nil
}

// DB returns the underlying database connection for direct queries.
func (e *Engine) DB() *sql.DB {
	return e.db
}

// Path returns the database file path.
func (e *Engine) Path() string {
	return e.dbPath
}

// initSchema creates all tables if they don't exist.
func (e *Engine) initSchema() error {
	schema := `
	-- ============================================================
	-- CONFIG: Hot-reloadable configuration
	-- ============================================================
	CREATE TABLE IF NOT EXISTS config (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		type TEXT DEFAULT 'string' CHECK (type IN ('string', 'int', 'bool', 'json')),
		description TEXT,
		updated_at INTEGER DEFAULT (strftime('%s', 'now')),
		version INTEGER DEFAULT 1
	);

	-- Config change trigger for hot-reload detection
	CREATE TRIGGER IF NOT EXISTS config_version_bump
	AFTER UPDATE ON config
	BEGIN
		UPDATE config SET version = version + 1, updated_at = strftime('%s', 'now') WHERE key = NEW.key;
	END;

	-- ============================================================
	-- SESSIONS: Conversation sessions
	-- ============================================================
	CREATE TABLE IF NOT EXISTS sessions (
		session_id TEXT PRIMARY KEY,
		created_at INTEGER DEFAULT (strftime('%s', 'now')),
		last_active_at INTEGER DEFAULT (strftime('%s', 'now')),
		git_branch TEXT,
		git_commit_start TEXT,
		provider_id TEXT DEFAULT 'cerebras',
		metadata TEXT DEFAULT '{}'
	);

	-- ============================================================
	-- MESSAGES: Conversation history
	-- ============================================================
	CREATE TABLE IF NOT EXISTS messages (
		message_id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL,
		role TEXT CHECK (role IN ('user', 'assistant', 'system')),
		content TEXT NOT NULL,
		provider_id TEXT,
		model TEXT,
		tokens_in INTEGER DEFAULT 0,
		tokens_out INTEGER DEFAULT 0,
		latency_ms INTEGER DEFAULT 0,
		created_at INTEGER DEFAULT (strftime('%s', 'now')),
		metadata TEXT DEFAULT '{}',

		FOREIGN KEY(session_id) REFERENCES sessions(session_id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, created_at);

	-- ============================================================
	-- PROVIDERS: Dynamic provider configuration (hot-reloadable)
	-- ============================================================
	CREATE TABLE IF NOT EXISTS providers (
		provider_id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		base_url TEXT NOT NULL,
		api_key_env TEXT NOT NULL,
		default_model TEXT NOT NULL,
		enabled INTEGER DEFAULT 1,
		priority INTEGER DEFAULT 100,
		rate_limit_rpm INTEGER DEFAULT 60,
		config TEXT DEFAULT '{}',
		created_at INTEGER DEFAULT (strftime('%s', 'now'))
	);

	-- ============================================================
	-- MODULES: Extensible module system (hot-reloadable)
	-- ============================================================
	CREATE TABLE IF NOT EXISTS modules (
		module_id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		version TEXT DEFAULT '1.0.0',
		enabled INTEGER DEFAULT 1,
		priority INTEGER DEFAULT 100,
		hooks TEXT DEFAULT '[]',
		config TEXT DEFAULT '{}',
		schema_sql TEXT,
		created_at INTEGER DEFAULT (strftime('%s', 'now')),
		updated_at INTEGER DEFAULT (strftime('%s', 'now'))
	);

	-- ============================================================
	-- MODULE_HOOKS: Event hooks for modules
	-- ============================================================
	CREATE TABLE IF NOT EXISTS module_hooks (
		hook_id TEXT PRIMARY KEY,
		module_id TEXT NOT NULL,
		event TEXT NOT NULL,
		handler TEXT NOT NULL,
		priority INTEGER DEFAULT 100,
		enabled INTEGER DEFAULT 1,
		config TEXT DEFAULT '{}',

		FOREIGN KEY(module_id) REFERENCES modules(module_id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_hooks_event ON module_hooks(event, enabled, priority);

	-- ============================================================
	-- FILES_MODIFIED: Track file changes
	-- ============================================================
	CREATE TABLE IF NOT EXISTS files_modified (
		file_id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL,
		message_id TEXT,
		file_path TEXT NOT NULL,
		operation TEXT CHECK (operation IN ('create', 'modify', 'delete')),
		content_before TEXT,
		content_after TEXT,
		diff TEXT,
		created_at INTEGER DEFAULT (strftime('%s', 'now')),

		FOREIGN KEY(session_id) REFERENCES sessions(session_id) ON DELETE CASCADE,
		FOREIGN KEY(message_id) REFERENCES messages(message_id) ON DELETE SET NULL
	);

	CREATE INDEX IF NOT EXISTS idx_files_session ON files_modified(session_id, created_at);

	-- ============================================================
	-- GIT_COMMITS: Track auto-commits
	-- ============================================================
	CREATE TABLE IF NOT EXISTS git_commits (
		commit_id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL,
		message_id TEXT,
		git_hash TEXT NOT NULL,
		commit_message TEXT NOT NULL,
		files_changed INTEGER DEFAULT 0,
		created_at INTEGER DEFAULT (strftime('%s', 'now')),

		FOREIGN KEY(session_id) REFERENCES sessions(session_id) ON DELETE CASCADE
	);

	-- ============================================================
	-- LEARNING: Pattern learning for future modules
	-- ============================================================
	CREATE TABLE IF NOT EXISTS learning_patterns (
		pattern_id TEXT PRIMARY KEY,
		pattern_type TEXT NOT NULL,
		input_pattern TEXT NOT NULL,
		output_pattern TEXT,
		success_count INTEGER DEFAULT 0,
		failure_count INTEGER DEFAULT 0,
		last_used_at INTEGER,
		metadata TEXT DEFAULT '{}',
		created_at INTEGER DEFAULT (strftime('%s', 'now'))
	);

	CREATE INDEX IF NOT EXISTS idx_patterns_type ON learning_patterns(pattern_type, success_count DESC);

	-- ============================================================
	-- FEEDBACK: User feedback for learning
	-- ============================================================
	CREATE TABLE IF NOT EXISTS feedback (
		feedback_id TEXT PRIMARY KEY,
		message_id TEXT NOT NULL,
		rating INTEGER CHECK (rating BETWEEN -1 AND 1),
		feedback_type TEXT,
		content TEXT,
		created_at INTEGER DEFAULT (strftime('%s', 'now')),

		FOREIGN KEY(message_id) REFERENCES messages(message_id) ON DELETE CASCADE
	);

	-- ============================================================
	-- PROMPTS: Dynamic prompt templates (hot-reloadable)
	-- ============================================================
	CREATE TABLE IF NOT EXISTS prompts (
		prompt_id TEXT PRIMARY KEY,
		name TEXT NOT NULL UNIQUE,
		template TEXT NOT NULL,
		variables TEXT DEFAULT '[]',
		category TEXT DEFAULT 'general',
		enabled INTEGER DEFAULT 1,
		version INTEGER DEFAULT 1,
		created_at INTEGER DEFAULT (strftime('%s', 'now')),
		updated_at INTEGER DEFAULT (strftime('%s', 'now'))
	);

	-- ============================================================
	-- INTENTS: Intent classification rules (hot-reloadable)
	-- ============================================================
	CREATE TABLE IF NOT EXISTS intents (
		intent_id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		patterns TEXT NOT NULL,
		action TEXT NOT NULL,
		priority INTEGER DEFAULT 100,
		enabled INTEGER DEFAULT 1,
		config TEXT DEFAULT '{}',
		created_at INTEGER DEFAULT (strftime('%s', 'now'))
	);

	CREATE INDEX IF NOT EXISTS idx_intents_priority ON intents(enabled, priority DESC);

	-- ============================================================
	-- SEED DATA
	-- ============================================================

	-- Default providers
	INSERT OR IGNORE INTO providers (provider_id, name, base_url, api_key_env, default_model, priority) VALUES
	('cerebras', 'Cerebras', 'https://api.cerebras.ai/v1', 'CEREBRAS_API_KEY', 'zai-glm-4.6', 1);

	-- Default config
	INSERT OR IGNORE INTO config (key, value, type, description) VALUES
	('default_provider', 'cerebras', 'string', 'Default LLM provider'),
	('auto_commit', 'true', 'bool', 'Auto-commit changes to git'),
	('confirm_changes', 'true', 'bool', 'Ask confirmation before applying changes'),
	('stream_output', 'true', 'bool', 'Stream LLM output token by token'),
	('max_context_messages', '20', 'int', 'Max messages to include in context'),
	('temperature', '0.7', 'string', 'LLM temperature'),
	('system_prompt', 'You are GoClode, an AI coding assistant. You help users write, modify, and understand code. When asked to create or modify files, output the complete file content in markdown code blocks with the filename.', 'string', 'System prompt for LLM');

	-- Default intents (hot-reloadable patterns)
	INSERT OR IGNORE INTO intents (intent_id, name, patterns, action, priority) VALUES
	('undo', 'Undo', '["annule", "undo", "reviens", "cancel", "revert"]', 'undo', 1),
	('switch', 'Switch Provider', '["change de modèle", "utilise", "switch to", "use provider"]', 'switch', 2),
	('help', 'Help', '["aide", "help", "/help", "comment"]', 'help', 3),
	('history', 'History', '["historique", "history", "/history"]', 'history', 4),
	('diff', 'Diff', '["diff", "/diff", "changes", "modifications"]', 'diff', 5);

	-- Default prompts
	INSERT OR IGNORE INTO prompts (prompt_id, name, template, category) VALUES
	('system_default', 'Default System', 'You are GoClode, an AI coding assistant. Help users write and modify code. For file changes, use this format:

**File: path/to/file.ext**
` + "```" + `language
// complete file content
` + "```" + `

Be concise and direct.', 'system'),
	('code_review', 'Code Review', 'Review the following code for bugs, security issues, and improvements:\n\n{{code}}', 'analysis'),
	('explain', 'Explain Code', 'Explain what this code does in simple terms:\n\n{{code}}', 'analysis');
	`

	_, err := e.db.Exec(schema)
	return err
}

// watchConfig monitors config changes for hot-reload
func (e *Engine) watchConfig() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			var maxVersion int64
			err := e.db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM config").Scan(&maxVersion)
			if err != nil {
				continue
			}

			if maxVersion > e.configVersion {
				e.configVersion = maxVersion
				e.notifyWatchers("config_changed")
				select {
				case e.reloadCh <- struct{}{}:
				default:
				}
			}
		}
	}
}

// OnChange registers a callback for config/module changes
func (e *Engine) OnChange(fn func(event string)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.watchers = append(e.watchers, fn)
}

func (e *Engine) notifyWatchers(event string) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, fn := range e.watchers {
		go fn(event)
	}
}

// ReloadCh returns a channel that receives when config changes
func (e *Engine) ReloadCh() <-chan struct{} {
	return e.reloadCh
}

// GetConfig retrieves a config value
func (e *Engine) GetConfig(key string) (string, error) {
	var value string
	err := e.db.QueryRow("SELECT value FROM config WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// SetConfig sets a config value (triggers hot-reload)
func (e *Engine) SetConfig(key, value string) error {
	_, err := e.db.Exec(`
		INSERT INTO config (key, value, updated_at) VALUES (?, ?, strftime('%s', 'now'))
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = strftime('%s', 'now'), version = version + 1
	`, key, value)
	return err
}

// GetConfigBool retrieves a boolean config value
func (e *Engine) GetConfigBool(key string) bool {
	val, _ := e.GetConfig(key)
	return val == "true" || val == "1"
}

// GetConfigInt retrieves an integer config value
func (e *Engine) GetConfigInt(key string) int {
	val, _ := e.GetConfig(key)
	var i int
	fmt.Sscanf(val, "%d", &i)
	return i
}

// Close shuts down the engine gracefully
func (e *Engine) Close() error {
	e.cancel()

	// Checkpoint WAL before closing
	_, _ = e.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")

	return e.db.Close()
}

// WatchFile watches a file for changes (for external config files)
func (e *Engine) WatchFile(path string, callback func()) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	go func() {
		defer watcher.Close()
		for {
			select {
			case <-e.ctx.Done():
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&fsnotify.Write == fsnotify.Write {
					callback()
				}
			case <-watcher.Errors:
				// Ignore errors
			}
		}
	}()

	return watcher.Add(path)
}

// Exec executes a query and returns rows affected
func (e *Engine) Exec(query string, args ...interface{}) (int64, error) {
	result, err := e.db.Exec(query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// Query executes a query and returns rows
func (e *Engine) Query(query string, args ...interface{}) (*sql.Rows, error) {
	return e.db.Query(query, args...)
}

// QueryRow executes a query and returns a single row
func (e *Engine) QueryRow(query string, args ...interface{}) *sql.Row {
	return e.db.QueryRow(query, args...)
}
