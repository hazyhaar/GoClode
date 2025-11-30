// Package ui provides the chat interface and intent parsing
package ui

import (
	"database/sql"
	"encoding/json"
	"regexp"
	"strings"
)

// IntentType represents the type of user intent
type IntentType string

const (
	IntentCode        IntentType = "code"          // Create/modify code
	IntentUndo        IntentType = "undo"          // Undo last action
	IntentRedo        IntentType = "redo"          // Redo last undo
	IntentSwitch      IntentType = "switch"        // Switch provider/model
	IntentQuestion    IntentType = "question"      // Ask a question
	IntentCommand     IntentType = "command"       // Slash command
	IntentHelp        IntentType = "help"          // Help request
	IntentHistory     IntentType = "history"       // View history
	IntentDiff        IntentType = "diff"          // View diff
	IntentStatus      IntentType = "status"        // Git/session status
	IntentConfig      IntentType = "config"        // Configuration
	IntentExit        IntentType = "exit"          // Exit/quit
	IntentFeedback    IntentType = "feedback"      // Positive/negative feedback
	IntentDebug       IntentType = "debug"         // Debug mode
)

// Intent represents a parsed user intent
type Intent struct {
	Type       IntentType
	Files      []string
	Action     string // create, modify, delete
	Content    string
	Provider   string
	Model      string
	Command    string
	Args       []string
	Confidence float64
	Raw        string
}

// IntentParser parses user input into intents
type IntentParser struct {
	db             *sql.DB
	patterns       map[IntentType][]string
	filePatterns   []*regexp.Regexp
	actionPatterns map[string][]string
}

// NewIntentParser creates a new intent parser
func NewIntentParser(db *sql.DB) *IntentParser {
	ip := &IntentParser{
		db:       db,
		patterns: make(map[IntentType][]string),
		filePatterns: []*regexp.Regexp{
			regexp.MustCompile(`([a-zA-Z0-9_\-./]+\.(go|md|txt|json|yaml|yml|js|ts|py|rs|c|cpp|h|hpp|java|rb|sh|sql|html|css|xml))`),
			regexp.MustCompile(`(?:dans|in|fichier|file)\s+["']?([a-zA-Z0-9_\-./]+)["']?`),
			regexp.MustCompile(`["']([a-zA-Z0-9_\-./]+\.[a-zA-Z]+)["']`),
		},
		actionPatterns: map[string][]string{
			"create": {"crée", "créer", "create", "nouveau", "new", "ajoute", "add", "génère", "generate", "make", "write"},
			"modify": {"modifie", "modifier", "modify", "change", "update", "edit", "fix", "corrige", "améliore", "refactor"},
			"delete": {"supprime", "supprimer", "delete", "remove", "efface", "enlève"},
		},
	}

	// Load patterns from database (hot-reloadable)
	ip.loadPatterns()

	return ip
}

// loadPatterns loads intent patterns from the database
func (ip *IntentParser) loadPatterns() {
	rows, err := ip.db.Query(`
		SELECT name, patterns, action FROM intents WHERE enabled = 1 ORDER BY priority
	`)
	if err != nil {
		// Use defaults if DB query fails
		ip.setDefaults()
		return
	}
	defer rows.Close()

	for rows.Next() {
		var name, patternsJSON, action string
		if err := rows.Scan(&name, &patternsJSON, &action); err != nil {
			continue
		}

		var patterns []string
		json.Unmarshal([]byte(patternsJSON), &patterns)

		intentType := IntentType(action)
		ip.patterns[intentType] = append(ip.patterns[intentType], patterns...)
	}

	// Add defaults if empty
	if len(ip.patterns) == 0 {
		ip.setDefaults()
	}
}

func (ip *IntentParser) setDefaults() {
	ip.patterns = map[IntentType][]string{
		IntentUndo:     {"annule", "undo", "reviens", "cancel", "revert", "défais"},
		IntentRedo:     {"refais", "redo", "again"},
		IntentSwitch:   {"change de modèle", "utilise", "switch to", "use provider", "use model"},
		IntentHelp:     {"aide", "help", "/help", "comment", "how to", "?"},
		IntentHistory:  {"historique", "history", "/history", "messages"},
		IntentDiff:     {"diff", "/diff", "changes", "modifications", "qu'est-ce qui a changé"},
		IntentStatus:   {"status", "/status", "état", "stats"},
		IntentConfig:   {"config", "/config", "configuration", "settings", "paramètres"},
		IntentExit:     {"exit", "quit", "/exit", "/quit", "bye", "au revoir", "sortir"},
		IntentFeedback: {"👍", "👎", "+1", "-1", "good", "bad", "bien", "mal", "merci"},
		IntentDebug:    {"/debug", "debug mode", "mode debug"},
	}
}

// Parse parses user input and returns an Intent
func (ip *IntentParser) Parse(input string) *Intent {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil
	}

	intent := &Intent{
		Raw:        input,
		Confidence: 0.5,
	}

	// 1. Slash command?
	if strings.HasPrefix(input, "/") {
		return ip.parseCommand(input)
	}

	// 2. Check for known patterns
	inputLower := strings.ToLower(input)

	for intentType, patterns := range ip.patterns {
		for _, pattern := range patterns {
			if strings.Contains(inputLower, strings.ToLower(pattern)) {
				intent.Type = intentType
				intent.Content = input
				intent.Confidence = 0.8

				// Extract provider for switch intent
				if intentType == IntentSwitch {
					intent.Provider = ip.extractProvider(input)
				}

				return intent
			}
		}
	}

	// 3. Detect files
	intent.Files = ip.extractFiles(input)

	// 4. Detect action
	intent.Action = ip.detectAction(input)

	// 5. Default to code intent
	intent.Type = IntentCode
	intent.Content = input
	intent.Confidence = 0.6

	return intent
}

// parseCommand parses a slash command
func (ip *IntentParser) parseCommand(input string) *Intent {
	parts := strings.Fields(input)
	if len(parts) == 0 {
		return nil
	}

	command := strings.TrimPrefix(parts[0], "/")
	args := parts[1:]

	intent := &Intent{
		Type:       IntentCommand,
		Command:    command,
		Args:       args,
		Raw:        input,
		Confidence: 1.0,
	}

	// Map commands to intent types
	switch command {
	case "help":
		intent.Type = IntentHelp
	case "history":
		intent.Type = IntentHistory
	case "diff":
		intent.Type = IntentDiff
	case "status":
		intent.Type = IntentStatus
	case "config":
		intent.Type = IntentConfig
	case "exit", "quit":
		intent.Type = IntentExit
	case "undo":
		intent.Type = IntentUndo
	case "redo":
		intent.Type = IntentRedo
	case "debug":
		intent.Type = IntentDebug
	case "provider", "model", "switch":
		intent.Type = IntentSwitch
		if len(args) > 0 {
			intent.Provider = args[0]
		}
	}

	return intent
}

// extractFiles extracts file paths from input
func (ip *IntentParser) extractFiles(input string) []string {
	files := make([]string, 0)
	seen := make(map[string]bool)

	for _, pattern := range ip.filePatterns {
		matches := pattern.FindAllStringSubmatch(input, -1)
		for _, match := range matches {
			if len(match) > 1 {
				file := match[1]
				if !seen[file] {
					seen[file] = true
					files = append(files, file)
				}
			}
		}
	}

	return files
}

// detectAction detects the action type from input
func (ip *IntentParser) detectAction(input string) string {
	inputLower := strings.ToLower(input)

	for action, patterns := range ip.actionPatterns {
		for _, pattern := range patterns {
			if strings.Contains(inputLower, pattern) {
				return action
			}
		}
	}

	return "modify" // Default
}

// extractProvider extracts provider name from input
func (ip *IntentParser) extractProvider(input string) string {
	inputLower := strings.ToLower(input)

	providers := map[string][]string{
		"cerebras":   {"cerebras"},
		"openrouter": {"openrouter", "open router"},
		"openai":     {"openai", "gpt", "chatgpt"},
		"anthropic":  {"anthropic", "claude"},
		"google":     {"google", "gemini"},
	}

	for provider, patterns := range providers {
		for _, pattern := range patterns {
			if strings.Contains(inputLower, pattern) {
				return provider
			}
		}
	}

	return ""
}

// Reload reloads patterns from the database
func (ip *IntentParser) Reload() {
	ip.loadPatterns()
}
