// GoClode - AI Coding Assistant
// A conversational CLI for coding with LLMs
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/anthropics/goclode/internal/core"
	"github.com/anthropics/goclode/internal/ui"
)

const version = "0.1.0"

func main() {
	// Flags
	var (
		showVersion = flag.Bool("version", false, "Show version")
		dbPath      = flag.String("db", "", "Database path (default: auto-generated in .goclode/)")
		debug       = flag.Bool("debug", false, "Enable debug mode")
	)

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `GoClode v%s - AI Coding Assistant

Usage: goclode [options]

Options:
`, version)
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
Examples:
  goclode                    Start interactive session
  goclode --debug            Start with debug logging
  goclode --db ./my.db       Use specific database

Environment Variables:
  CEREBRAS_API_KEY           Cerebras API key
  OPENROUTER_API_KEY         OpenRouter API key (optional)

For more info: https://github.com/anthropics/goclode
`)
	}

	flag.Parse()

	if *showVersion {
		fmt.Printf("GoClode v%s\n", version)
		return
	}

	// Create engine
	engine, err := core.NewEngine(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Create chat interface
	chat, err := ui.NewChat(engine)
	if err != nil {
		engine.Close()
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Enable debug if requested
	if *debug {
		// This would be set through the chat interface
		engine.SetConfig("debug_mode", "true")
	}

	// Run
	if err := chat.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
