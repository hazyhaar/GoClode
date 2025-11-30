package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewEngine(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// Create engine
	engine, err := NewEngine(dbPath)
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}
	defer engine.Close()

	// Verify DB file exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("Database file not created")
	}

	// Verify path
	if engine.Path() != dbPath {
		t.Errorf("Path mismatch: got %s, want %s", engine.Path(), dbPath)
	}
}

func TestConfig(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	engine, err := NewEngine(dbPath)
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}
	defer engine.Close()

	// Test default config
	val, err := engine.GetConfig("default_provider")
	if err != nil {
		t.Errorf("GetConfig failed: %v", err)
	}
	if val != "cerebras" {
		t.Errorf("Default provider: got %s, want cerebras", val)
	}

	// Test SetConfig
	if err := engine.SetConfig("test_key", "test_value"); err != nil {
		t.Errorf("SetConfig failed: %v", err)
	}

	val, err = engine.GetConfig("test_key")
	if err != nil {
		t.Errorf("GetConfig failed: %v", err)
	}
	if val != "test_value" {
		t.Errorf("Config value: got %s, want test_value", val)
	}

	// Test GetConfigBool
	if err := engine.SetConfig("bool_key", "true"); err != nil {
		t.Errorf("SetConfig failed: %v", err)
	}
	if !engine.GetConfigBool("bool_key") {
		t.Error("GetConfigBool should return true")
	}

	// Test GetConfigInt
	if err := engine.SetConfig("int_key", "42"); err != nil {
		t.Errorf("SetConfig failed: %v", err)
	}
	if engine.GetConfigInt("int_key") != 42 {
		t.Errorf("GetConfigInt: got %d, want 42", engine.GetConfigInt("int_key"))
	}
}

func TestSchema(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	engine, err := NewEngine(dbPath)
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}
	defer engine.Close()

	// Verify tables exist
	tables := []string{
		"config",
		"sessions",
		"messages",
		"providers",
		"modules",
		"module_hooks",
		"files_modified",
		"git_commits",
		"learning_patterns",
		"feedback",
		"prompts",
		"intents",
	}

	for _, table := range tables {
		var name string
		err := engine.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?",
			table,
		).Scan(&name)
		if err != nil {
			t.Errorf("Table %s not found: %v", table, err)
		}
	}

	// Verify providers are seeded
	var count int
	engine.QueryRow("SELECT COUNT(*) FROM providers").Scan(&count)
	if count < 2 {
		t.Errorf("Expected at least 2 providers, got %d", count)
	}

	// Verify intents are seeded
	engine.QueryRow("SELECT COUNT(*) FROM intents").Scan(&count)
	if count < 5 {
		t.Errorf("Expected at least 5 intents, got %d", count)
	}
}

func TestExec(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	engine, err := NewEngine(dbPath)
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}
	defer engine.Close()

	// Test Exec
	affected, err := engine.Exec("INSERT INTO config (key, value) VALUES (?, ?)", "exec_test", "value")
	if err != nil {
		t.Errorf("Exec failed: %v", err)
	}
	if affected != 1 {
		t.Errorf("Expected 1 affected row, got %d", affected)
	}
}

func TestQuery(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	engine, err := NewEngine(dbPath)
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}
	defer engine.Close()

	// Query config
	rows, err := engine.Query("SELECT key, value FROM config LIMIT 3")
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			t.Errorf("Scan failed: %v", err)
		}
		count++
	}

	if count == 0 {
		t.Error("Expected at least 1 config row")
	}
}
