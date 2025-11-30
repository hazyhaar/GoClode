// Package ui provides the conversational chat interface
package ui

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hazyhaar/GoClode/internal/core"
	"github.com/hazyhaar/GoClode/internal/git"
	"github.com/hazyhaar/GoClode/internal/providers"
	"github.com/hazyhaar/GoClode/internal/session"
	"github.com/chzyer/readline"
)

// Chat is the main conversational interface
type Chat struct {
	engine   *core.Engine
	modules  *core.ModuleManager
	registry *providers.Registry
	session  *session.Manager
	git      *git.Manager
	parser   *IntentParser

	rl      *readline.Instance
	ctx     context.Context
	cancel  context.CancelFunc

	// State
	debugMode    bool
	shutdownOnce sync.Once
}

// NewChat creates a new chat interface
func NewChat(engine *core.Engine) (*Chat, error) {
	ctx, cancel := context.WithCancel(context.Background())

	// Initialize components
	modules := core.NewModuleManager(engine)
	registry := providers.NewRegistry(engine.DB())
	sessionMgr := session.NewManager(engine)
	gitMgr := git.NewManager("")
	parser := NewIntentParser(engine.DB())

	// Setup readline
	rl, err := readline.NewEx(&readline.Config{
		Prompt:          "\033[36m>\033[0m ",
		HistoryFile:     ".goclode/history",
		InterruptPrompt: "^C",
		EOFPrompt:       "exit",
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("readline: %w", err)
	}

	chat := &Chat{
		engine:   engine,
		modules:  modules,
		registry: registry,
		session:  sessionMgr,
		git:      gitMgr,
		parser:   parser,
		rl:       rl,
		ctx:      ctx,
		cancel:   cancel,
	}

	// Set provider in git for commit messages
	if p := registry.Current(); p != nil {
		gitMgr.SetProvider(p.ID())
	}

	return chat, nil
}

// Run starts the chat loop
func (c *Chat) Run() error {
	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		c.shutdown()
	}()

	// Create session
	providerID := "cerebras"
	if p := c.registry.Current(); p != nil {
		providerID = p.ID()
	}

	sess, err := c.session.Create(providerID)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}

	// Welcome message
	c.printWelcome(sess)

	// Emit session start event
	c.modules.Emit("session_start", map[string]interface{}{
		"session_id": sess.ID,
		"provider":   providerID,
	})

	// Main loop
	for {
		line, err := c.rl.Readline()
		if err != nil {
			if err == readline.ErrInterrupt {
				continue
			}
			if err == io.EOF {
				break
			}
			return err
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Parse intent
		intent := c.parser.Parse(line)
		if intent == nil {
			continue
		}

		// Handle intent
		if err := c.handleIntent(intent); err != nil {
			fmt.Printf("\033[31mError: %v\033[0m\n", err)
		}
	}

	c.shutdown()
	return nil
}

// handleIntent routes intents to handlers
func (c *Chat) handleIntent(intent *Intent) error {
	// Emit intent event for debugging
	c.modules.Emit("intent_parsed", map[string]interface{}{
		"type":       string(intent.Type),
		"content":    intent.Content,
		"files":      intent.Files,
		"action":     intent.Action,
		"confidence": intent.Confidence,
	})

	switch intent.Type {
	case IntentExit:
		c.shutdown()
		os.Exit(0)

	case IntentHelp:
		c.printHelp()

	case IntentHistory:
		return c.showHistory()

	case IntentStatus:
		return c.showStatus()

	case IntentDiff:
		return c.showDiff()

	case IntentUndo:
		return c.handleUndo()

	case IntentSwitch:
		return c.handleSwitch(intent.Provider)

	case IntentConfig:
		return c.handleConfig(intent.Args)

	case IntentDebug:
		return c.toggleDebug()

	case IntentFeedback:
		return c.handleFeedback(intent.Raw)

	case IntentCode, IntentQuestion:
		return c.handleChat(intent)

	default:
		return c.handleChat(intent)
	}

	return nil
}

// handleChat handles code/question intents
func (c *Chat) handleChat(intent *Intent) error {
	provider := c.registry.Current()
	if provider == nil {
		return fmt.Errorf("no provider available")
	}

	// Build messages with context
	messages, err := c.buildMessages(intent)
	if err != nil {
		return err
	}

	// Save user message
	c.session.AddMessage("user", intent.Raw, nil)

	// Show thinking indicator
	fmt.Print("\033[90m🤔 Thinking...\033[0m")

	// Stream response
	start := time.Now()
	stream, err := provider.Stream(c.ctx, &providers.Request{
		Messages:    messages,
		Temperature: 0.7,
	})
	if err != nil {
		fmt.Println()
		return fmt.Errorf("stream: %w", err)
	}

	// Clear thinking indicator
	fmt.Print("\r\033[K")

	var fullResponse strings.Builder
	var tokensIn, tokensOut int

	for chunk := range stream {
		if chunk.Error != nil {
			return chunk.Error
		}

		if chunk.Delta != "" {
			fmt.Print(chunk.Delta)
			fullResponse.WriteString(chunk.Delta)
		}

		if chunk.Done {
			tokensIn = chunk.TokensIn
			tokensOut = chunk.TokensOut
		}
	}
	fmt.Println()

	response := fullResponse.String()
	latency := time.Since(start).Milliseconds()

	// Save assistant message
	c.session.AddMessage("assistant", response, &providers.Response{
		TokensIn:  tokensIn,
		TokensOut: tokensOut,
		Latency:   latency,
		Model:     provider.ID(),
	})

	// Extract and apply file changes
	changes := c.extractFileChanges(response)
	if len(changes) > 0 {
		if err := c.applyChanges(changes); err != nil {
			fmt.Printf("\033[33m⚠️  Could not apply changes: %v\033[0m\n", err)
		}
	}

	// Emit completion event
	c.modules.Emit("chat_complete", map[string]interface{}{
		"tokens_in":  tokensIn,
		"tokens_out": tokensOut,
		"latency_ms": latency,
		"files":      len(changes),
	})

	return nil
}

// buildMessages builds the message list for the LLM
func (c *Chat) buildMessages(intent *Intent) ([]providers.Message, error) {
	// Get system prompt
	systemPrompt, _ := c.engine.GetConfig("system_prompt")
	if systemPrompt == "" {
		systemPrompt = `You are GoClode, an AI coding assistant. Help users write and modify code.
For file changes, use this format:

**File: path/to/file.ext**
` + "```" + `language
// complete file content here
` + "```" + `

Be concise and direct.`
	}

	messages := []providers.Message{
		{Role: "system", Content: systemPrompt},
	}

	// Add context from previous messages
	maxContext := c.engine.GetConfigInt("max_context_messages")
	if maxContext <= 0 {
		maxContext = 20
	}

	contextMessages, _ := c.session.GetContextMessages(maxContext)
	messages = append(messages, contextMessages...)

	// Add current message
	messages = append(messages, providers.Message{
		Role:    "user",
		Content: intent.Raw,
	})

	return messages, nil
}

// extractFileChanges extracts file changes from LLM response
func (c *Chat) extractFileChanges(response string) []FileChange {
	changes := make([]FileChange, 0)
	seen := make(map[string]bool)

	// Find all code blocks with their language
	codeBlockPattern := regexp.MustCompile("(?s)```([a-z]+)\n(.*?)```")
	codeBlocks := codeBlockPattern.FindAllStringSubmatchIndex(response, -1)

	// Extension map for languages
	langToExt := map[string]string{
		"go": ".go", "python": ".py", "py": ".py", "javascript": ".js", "js": ".js",
		"typescript": ".ts", "ts": ".ts", "rust": ".rs", "java": ".java",
		"json": ".json", "yaml": ".yaml", "yml": ".yml", "sql": ".sql",
		"sh": ".sh", "bash": ".sh", "markdown": ".md", "md": ".md",
	}

	// Filename patterns to look for before each code block
	filenamePatterns := []*regexp.Regexp{
		regexp.MustCompile("`([a-zA-Z0-9_\\-./]+\\.[a-z]+)`"),                          // `filename.ext`
		regexp.MustCompile("\\*\\*(?:File:?)?\\s*([a-zA-Z0-9_\\-./]+\\.[a-z]+)\\*\\*"), // **File: name**
		regexp.MustCompile("([a-zA-Z0-9_\\-./]+\\.[a-z]{1,4})\\s*[:：]"),               // filename.ext:
	}

	for _, blockIdx := range codeBlocks {
		if len(blockIdx) < 6 {
			continue
		}

		lang := response[blockIdx[2]:blockIdx[3]]
		content := response[blockIdx[4]:blockIdx[5]]

		// Look for filename in text before this code block (up to 500 chars)
		searchStart := blockIdx[0] - 500
		if searchStart < 0 {
			searchStart = 0
		}
		textBefore := response[searchStart:blockIdx[0]]

		var filename string
		for _, pattern := range filenamePatterns {
			matches := pattern.FindAllStringSubmatch(textBefore, -1)
			if len(matches) > 0 {
				filename = matches[len(matches)-1][1]
				break
			}
		}

		// If no filename found, generate one based on language
		if filename == "" {
			ext, ok := langToExt[lang]
			if !ok {
				continue
			}
			filename = "main" + ext
		}

		if seen[filename] {
			continue
		}
		seen[filename] = true

		content = strings.TrimSuffix(content, "\n")
		if strings.TrimSpace(content) == "" {
			continue
		}

		changes = append(changes, FileChange{
			Path:    filename,
			Content: content,
		})
	}

	return changes
}

// FileChange represents a file to be created/modified
type FileChange struct {
	Path    string
	Content string
}

// applyChanges applies file changes and commits
func (c *Chat) applyChanges(changes []FileChange) error {
	if len(changes) == 0 {
		return nil
	}

	// Show summary
	fmt.Println("\n\033[33m📁 Files to modify:\033[0m")
	for _, ch := range changes {
		exists := fileExists(ch.Path)
		if exists {
			fmt.Printf("  📝 %s (modify)\n", ch.Path)
		} else {
			fmt.Printf("  ✨ %s (create)\n", ch.Path)
		}
	}

	// Ask for confirmation if enabled
	if c.engine.GetConfigBool("confirm_changes") {
		fmt.Print("\n\033[36mApply changes? [Y/n] \033[0m")
		var confirm string
		fmt.Scanln(&confirm)
		confirm = strings.ToLower(strings.TrimSpace(confirm))
		if confirm != "" && confirm != "y" && confirm != "yes" {
			fmt.Println("\033[33m❌ Cancelled\033[0m")
			return nil
		}
	}

	// Apply changes
	filePaths := make([]string, 0, len(changes))
	for _, ch := range changes {
		// Create directories if needed
		dir := ch.Path[:max(0, strings.LastIndex(ch.Path, "/"))]
		if dir != "" {
			os.MkdirAll(dir, 0755)
		}

		// Get content before for recording
		contentBefore, _ := c.git.GetFileContent(ch.Path)
		operation := "modify"
		if contentBefore == "" {
			operation = "create"
		}

		// Write file
		if err := os.WriteFile(ch.Path, []byte(ch.Content), 0644); err != nil {
			return fmt.Errorf("write %s: %w", ch.Path, err)
		}

		// Record change
		c.session.RecordFileChange(ch.Path, operation, contentBefore, ch.Content, "")
		filePaths = append(filePaths, ch.Path)

		fmt.Printf("\033[32m✓ %s\033[0m\n", ch.Path)
	}

	// Auto-commit if enabled
	if c.engine.GetConfigBool("auto_commit") && c.git.IsRepo() {
		message := fmt.Sprintf("GoClode: %s", summarizeChanges(changes))
		hash, err := c.git.AutoCommit(filePaths, message)
		if err != nil {
			fmt.Printf("\033[33m⚠️  Git commit failed: %v\033[0m\n", err)
		} else {
			c.session.RecordGitCommit(hash, message, len(filePaths))
			fmt.Printf("\033[90m📦 Committed: %s\033[0m\n", hash[:8])
		}
	}

	fmt.Println("\033[32m✓ Done\033[0m")
	return nil
}

// handleUndo reverts the last change
func (c *Chat) handleUndo() error {
	if !c.git.IsRepo() {
		return fmt.Errorf("not a git repository")
	}

	hash, err := c.git.Undo()
	if err != nil {
		return err
	}

	fmt.Printf("\033[32m✓ Reverted commit %s\033[0m\n", hash[:8])
	return nil
}

// handleSwitch switches provider
func (c *Chat) handleSwitch(providerID string) error {
	if providerID == "" {
		// List available providers
		fmt.Println("\n\033[33mAvailable providers:\033[0m")
		for _, p := range c.registry.List() {
			status := "\033[31m✗\033[0m"
			if p.IsAvailable() {
				status = "\033[32m✓\033[0m"
			}
			current := ""
			if c.registry.Current() != nil && p.ID() == c.registry.Current().ID() {
				current = " \033[36m(current)\033[0m"
			}
			fmt.Printf("  %s %s%s\n", status, p.Name(), current)
		}
		return nil
	}

	if err := c.registry.SetCurrent(providerID); err != nil {
		return err
	}

	c.session.SetProvider(providerID)
	c.git.SetProvider(providerID)

	fmt.Printf("\033[32m✓ Switched to %s\033[0m\n", providerID)
	return nil
}

// handleConfig handles config commands
func (c *Chat) handleConfig(args []string) error {
	if len(args) == 0 {
		// Show current config
		fmt.Println("\n\033[33mConfiguration:\033[0m")
		rows, err := c.engine.Query("SELECT key, value FROM config ORDER BY key")
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var key, value string
			rows.Scan(&key, &value)
			// Truncate long values
			if len(value) > 50 {
				value = value[:47] + "..."
			}
			fmt.Printf("  %s = %s\n", key, value)
		}
		return nil
	}

	if len(args) >= 2 {
		// Set config
		key := args[0]
		value := strings.Join(args[1:], " ")
		if err := c.engine.SetConfig(key, value); err != nil {
			return err
		}
		fmt.Printf("\033[32m✓ Set %s = %s\033[0m\n", key, value)
	}

	return nil
}

// toggleDebug toggles debug mode
func (c *Chat) toggleDebug() error {
	c.debugMode = !c.debugMode
	if c.debugMode {
		c.modules.EnableDebug()
		fmt.Println("\033[33m🔧 Debug mode enabled\033[0m")
	} else {
		c.modules.DisableDebug()
		fmt.Println("\033[33m🔧 Debug mode disabled\033[0m")
	}
	return nil
}

// handleFeedback handles feedback
func (c *Chat) handleFeedback(raw string) error {
	rating := 0
	if strings.Contains(raw, "👍") || strings.Contains(raw, "+1") || strings.Contains(raw, "good") || strings.Contains(raw, "merci") {
		rating = 1
		fmt.Println("\033[32m👍 Thanks for the positive feedback!\033[0m")
	} else if strings.Contains(raw, "👎") || strings.Contains(raw, "-1") || strings.Contains(raw, "bad") {
		rating = -1
		fmt.Println("\033[33m👎 Thanks for the feedback. I'll try to improve.\033[0m")
	}

	// Record feedback in learning_patterns for future use
	c.engine.Exec(`
		INSERT INTO learning_patterns (pattern_id, pattern_type, input_pattern, metadata)
		VALUES (?, 'feedback', ?, ?)
	`, fmt.Sprintf("fb_%d", time.Now().UnixNano()), raw, fmt.Sprintf(`{"rating":%d}`, rating))

	return nil
}

// showHistory shows message history
func (c *Chat) showHistory() error {
	messages, err := c.session.GetMessages(20)
	if err != nil {
		return err
	}

	fmt.Println("\n\033[33mRecent messages:\033[0m")
	for _, msg := range messages {
		role := msg.Role
		if role == "user" {
			role = "\033[36mYou\033[0m"
		} else if role == "assistant" {
			role = "\033[32mGoClode\033[0m"
		}

		content := msg.Content
		if len(content) > 100 {
			content = content[:97] + "..."
		}
		content = strings.ReplaceAll(content, "\n", " ")

		fmt.Printf("  %s: %s\n", role, content)
	}
	return nil
}

// showStatus shows session status
func (c *Chat) showStatus() error {
	stats, err := c.session.GetStats()
	if err != nil {
		return err
	}

	fmt.Println("\n\033[33mSession Status:\033[0m")
	fmt.Printf("  Messages: %d\n", stats["messages"])
	fmt.Printf("  Tokens: %d in / %d out\n", stats["tokens_in"], stats["tokens_out"])
	fmt.Printf("  Files modified: %d\n", stats["files_modified"])
	fmt.Printf("  Commits: %d\n", stats["commits"])

	if c.registry.Current() != nil {
		fmt.Printf("  Provider: %s\n", c.registry.Current().Name())
	}

	if c.git.IsRepo() {
		branch, _ := c.git.CurrentBranch()
		fmt.Printf("  Git branch: %s\n", branch)
		if c.git.HasChanges() {
			fmt.Printf("  \033[33m⚠️  Uncommitted changes\033[0m\n")
		}
	}

	return nil
}

// showDiff shows the last diff
func (c *Chat) showDiff() error {
	if !c.git.IsRepo() {
		return fmt.Errorf("not a git repository")
	}

	diff, err := c.git.GetLastDiff()
	if err != nil {
		// Try getting current diff instead
		diff, err = c.git.GetDiff("")
		if err != nil {
			return err
		}
	}

	if diff == "" {
		fmt.Println("\033[90mNo changes\033[0m")
		return nil
	}

	fmt.Println(diff)
	return nil
}

// printWelcome prints the welcome message
func (c *Chat) printWelcome(sess *session.Session) {
	fmt.Println()
	fmt.Println("\033[36m╔═══════════════════════════════════════╗\033[0m")
	fmt.Println("\033[36m║\033[0m  \033[1m🤖 GoClode\033[0m - AI Coding Assistant     \033[36m║\033[0m")
	fmt.Println("\033[36m╚═══════════════════════════════════════╝\033[0m")
	fmt.Println()

	if p := c.registry.Current(); p != nil {
		if p.IsAvailable() {
			fmt.Printf("\033[32m✓ Provider: %s\033[0m\n", p.Name())
		} else {
			fmt.Printf("\033[31m✗ Provider %s not configured\033[0m\n", p.Name())
			fmt.Printf("  Set %s environment variable\n", "CEREBRAS_API_KEY")
		}
	}

	if c.git.IsRepo() {
		branch, _ := c.git.CurrentBranch()
		fmt.Printf("\033[32m✓ Git: %s\033[0m\n", branch)
	}

	fmt.Printf("\033[90mSession: %s\033[0m\n", sess.ID[:8])
	fmt.Printf("\033[90mDB: %s\033[0m\n", c.engine.Path())
	fmt.Println()
	fmt.Println("Type your request or /help for commands.")
	fmt.Println()
}

// printHelp prints help
func (c *Chat) printHelp() {
	fmt.Print(`
` + "\033[33mCommands:\033[0m" + `
  /help       - Show this help
  /history    - Show message history
  /status     - Show session status
  /diff       - Show last changes
  /undo       - Undo last change
  /provider   - List/switch providers
  /config     - Show/set configuration
  /debug      - Toggle debug mode
  /exit       - Exit GoClode

` + "\033[33mExamples:\033[0m" + `
  "Create a README.md file"
  "Add a fibonacci function in utils/math.go"
  "Fix the bug in main.go"
  "Undo" or "Annule"
  "Switch to openrouter"
`)
}

// shutdown gracefully shuts down the chat
func (c *Chat) shutdown() {
	c.shutdownOnce.Do(func() {
		fmt.Println("\n\033[33m👋 Goodbye!\033[0m")

		// Emit shutdown event
		c.modules.Emit("session_end", map[string]interface{}{
			"session_id": c.session.Current(),
		})

		c.cancel()
		c.rl.Close()
		c.engine.Close()
	})
}

// Helper functions

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func summarizeChanges(changes []FileChange) string {
	if len(changes) == 1 {
		return fmt.Sprintf("update %s", changes[0].Path)
	}
	return fmt.Sprintf("update %d files", len(changes))
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
