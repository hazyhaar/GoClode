package ui

import (
	"path/filepath"
	"testing"

	"github.com/hazyhaar/GoClode/internal/core"
)

func setupTestDB(t *testing.T) *core.Engine {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	engine, err := core.NewEngine(dbPath)
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}

	return engine
}

func TestIntentParser_Parse(t *testing.T) {
	engine := setupTestDB(t)
	defer engine.Close()

	parser := NewIntentParser(engine.DB())

	tests := []struct {
		name     string
		input    string
		wantType IntentType
	}{
		{"empty", "", IntentType("")},
		{"undo french", "annule ça", IntentUndo},
		{"undo english", "undo", IntentUndo},
		{"help french", "aide", IntentHelp},
		{"help english", "/help", IntentHelp},
		{"history", "/history", IntentHistory},
		{"exit", "/exit", IntentExit},
		{"switch provider", "utilise openrouter", IntentSwitch}, // matches "utilise" in switch patterns
		{"code request", "Crée un fichier README.md", IntentCode},
		{"debug", "/debug", IntentDebug},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent := parser.Parse(tt.input)

			if tt.input == "" {
				if intent != nil {
					t.Error("Expected nil for empty input")
				}
				return
			}

			if intent == nil {
				t.Fatal("Expected non-nil intent")
			}

			if intent.Type != tt.wantType {
				t.Errorf("Type = %v, want %v", intent.Type, tt.wantType)
			}
		})
	}
}

func TestIntentParser_ExtractFiles(t *testing.T) {
	engine := setupTestDB(t)
	defer engine.Close()

	parser := NewIntentParser(engine.DB())

	tests := []struct {
		name          string
		input         string
		wantFilesLen  int  // Check length instead of exact match
	}{
		{
			name:         "explicit path",
			input:        "Crée un fichier README.md",
			wantFilesLen: 1,
		},
		{
			name:         "go file",
			input:        "Modifie utils/math.go",
			wantFilesLen: 1,
		},
		{
			name:         "multiple files",
			input:        "Edit main.go and config.json",
			wantFilesLen: 2,
		},
		{
			name:         "no explicit file",
			input:        "Ajoute une fonction",
			wantFilesLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent := parser.Parse(tt.input)
			if intent == nil {
				t.Fatal("Expected non-nil intent")
			}

			if len(intent.Files) != tt.wantFilesLen {
				t.Errorf("Files count = %d, want %d (files: %v)", len(intent.Files), tt.wantFilesLen, intent.Files)
			}
		})
	}
}

func TestIntentParser_DetectAction(t *testing.T) {
	engine := setupTestDB(t)
	defer engine.Close()

	parser := NewIntentParser(engine.DB())

	tests := []struct {
		name       string
		input      string
		wantAction string
	}{
		{"create french", "crée un fichier", "create"},
		{"create english", "create a file", "create"},
		{"modify french", "modifie le fichier", "modify"},
		{"modify english", "update the file", "modify"},
		{"delete french", "supprime le fichier", "delete"},
		{"delete english", "remove the file", "delete"},
		{"default", "do something", "modify"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent := parser.Parse(tt.input)
			if intent == nil {
				t.Fatal("Expected non-nil intent")
			}

			if intent.Action != tt.wantAction {
				t.Errorf("Action = %v, want %v", intent.Action, tt.wantAction)
			}
		})
	}
}

func TestIntentParser_ExtractProvider(t *testing.T) {
	engine := setupTestDB(t)
	defer engine.Close()

	parser := NewIntentParser(engine.DB())

	// These inputs match the "switch" pattern, so they get IntentSwitch type
	// and the provider is extracted
	tests := []struct {
		name         string
		input        string
		wantProvider string
		wantType     IntentType
	}{
		{"cerebras", "utilise cerebras", "cerebras", IntentSwitch},
		{"openrouter", "switch to openrouter", "openrouter", IntentSwitch},
		{"openai", "use openai", "", IntentCode},  // "use" is not in switch patterns
		{"claude", "utilise claude", "anthropic", IntentSwitch},
		{"unknown", "switch to something", "", IntentSwitch},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent := parser.Parse(tt.input)
			if intent == nil {
				t.Fatal("Expected non-nil intent")
			}

			if intent.Type != tt.wantType {
				t.Errorf("Type = %v, want %v", intent.Type, tt.wantType)
			}

			if intent.Provider != tt.wantProvider {
				t.Errorf("Provider = %v, want %v", intent.Provider, tt.wantProvider)
			}
		})
	}
}

func TestIntentParser_SlashCommands(t *testing.T) {
	engine := setupTestDB(t)
	defer engine.Close()

	parser := NewIntentParser(engine.DB())

	tests := []struct {
		name        string
		input       string
		wantType    IntentType
		wantCommand string
	}{
		{"help", "/help", IntentHelp, "help"},
		{"history", "/history", IntentHistory, "history"},
		{"diff", "/diff", IntentDiff, "diff"},
		{"status", "/status", IntentStatus, "status"},
		{"config", "/config", IntentConfig, "config"},
		{"exit", "/exit", IntentExit, "exit"},
		{"quit", "/quit", IntentExit, "quit"},
		{"undo", "/undo", IntentUndo, "undo"},
		{"debug", "/debug", IntentDebug, "debug"},
		{"provider", "/provider cerebras", IntentSwitch, "provider"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent := parser.Parse(tt.input)
			if intent == nil {
				t.Fatal("Expected non-nil intent")
			}

			if intent.Type != tt.wantType {
				t.Errorf("Type = %v, want %v", intent.Type, tt.wantType)
			}

			if intent.Command != tt.wantCommand {
				t.Errorf("Command = %v, want %v", intent.Command, tt.wantCommand)
			}
		})
	}
}
