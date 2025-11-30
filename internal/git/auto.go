// Package git provides automatic git operations for GoClode
package git

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Manager handles git operations
type Manager struct {
	workDir  string
	provider string
	version  string
}

// FileChange represents a file change
type FileChange struct {
	Path      string
	Operation string // create, modify, delete
	Before    string
	After     string
	Diff      string
}

// NewManager creates a new git manager
func NewManager(workDir string) *Manager {
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	return &Manager{
		workDir: workDir,
		version: "0.1.0",
	}
}

// SetProvider sets the current provider name for commit messages
func (m *Manager) SetProvider(provider string) {
	m.provider = provider
}

// IsRepo checks if the current directory is a git repository
func (m *Manager) IsRepo() bool {
	gitDir := filepath.Join(m.workDir, ".git")
	info, err := os.Stat(gitDir)
	return err == nil && info.IsDir()
}

// CurrentBranch returns the current git branch
func (m *Manager) CurrentBranch() (string, error) {
	out, err := m.exec("git", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CurrentCommit returns the current commit hash
func (m *Manager) CurrentCommit() (string, error) {
	out, err := m.exec("git", "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// AutoCommit commits changes with GoClode metadata
func (m *Manager) AutoCommit(files []string, message string) (string, error) {
	if !m.IsRepo() {
		return "", fmt.Errorf("not a git repository")
	}

	if len(files) == 0 {
		return "", fmt.Errorf("no files to commit")
	}

	// Stage files
	for _, file := range files {
		if _, err := m.exec("git", "add", file); err != nil {
			return "", fmt.Errorf("stage %s: %w", file, err)
		}
	}

	// Check if there are staged changes
	status, err := m.exec("git", "diff", "--cached", "--name-only")
	if err != nil {
		return "", fmt.Errorf("check staged: %w", err)
	}

	if strings.TrimSpace(status) == "" {
		return "", fmt.Errorf("no changes to commit")
	}

	// Build commit message with metadata
	provider := m.provider
	if provider == "" {
		provider = "unknown"
	}

	fullMessage := fmt.Sprintf(`%s

Generated-by: GoClode v%s
Provider: %s
Timestamp: %s`, message, m.version, provider, time.Now().Format(time.RFC3339))

	// Commit
	if _, err := m.exec("git", "commit", "-m", fullMessage); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}

	// Get commit hash
	hash, err := m.CurrentCommit()
	if err != nil {
		return "", fmt.Errorf("get commit hash: %w", err)
	}

	return hash, nil
}

// Undo reverts the last GoClode commit
func (m *Manager) Undo() (string, error) {
	if !m.IsRepo() {
		return "", fmt.Errorf("not a git repository")
	}

	// Find last GoClode commit
	lastCommit, err := m.LastGoClodeCommit()
	if err != nil {
		return "", err
	}

	// Revert (non-destructive)
	if _, err := m.exec("git", "revert", "--no-edit", lastCommit); err != nil {
		return "", fmt.Errorf("revert %s: %w", lastCommit, err)
	}

	return lastCommit, nil
}

// LastGoClodeCommit returns the hash of the last GoClode commit
func (m *Manager) LastGoClodeCommit() (string, error) {
	out, err := m.exec("git", "log", "--grep=Generated-by: GoClode", "-1", "--format=%H")
	if err != nil {
		return "", fmt.Errorf("find GoClode commit: %w", err)
	}

	hash := strings.TrimSpace(out)
	if hash == "" {
		return "", fmt.Errorf("no GoClode commit found")
	}

	return hash, nil
}

// GetDiff returns the diff for a file or all staged changes
func (m *Manager) GetDiff(file string) (string, error) {
	args := []string{"diff"}
	if file != "" {
		args = append(args, "--", file)
	}

	out, err := m.exec("git", args...)
	if err != nil {
		return "", err
	}

	return out, nil
}

// GetLastDiff returns the diff of the last commit
func (m *Manager) GetLastDiff() (string, error) {
	out, err := m.exec("git", "diff", "HEAD~1", "HEAD")
	if err != nil {
		return "", err
	}
	return out, nil
}

// Status returns the git status
func (m *Manager) Status() (string, error) {
	return m.exec("git", "status", "--porcelain")
}

// HasChanges checks if there are uncommitted changes
func (m *Manager) HasChanges() bool {
	status, err := m.Status()
	if err != nil {
		return false
	}
	return strings.TrimSpace(status) != ""
}

// Init initializes a new git repository
func (m *Manager) Init() error {
	if m.IsRepo() {
		return nil // Already a repo
	}

	_, err := m.exec("git", "init")
	return err
}

// GetFileContent reads file content (before changes)
func (m *Manager) GetFileContent(path string) (string, error) {
	content, err := os.ReadFile(filepath.Join(m.workDir, path))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(content), nil
}

// Log returns recent commits
func (m *Manager) Log(count int) ([]CommitInfo, error) {
	if count <= 0 {
		count = 10
	}

	format := "%H|%s|%an|%at"
	out, err := m.exec("git", "log", fmt.Sprintf("-n%d", count), fmt.Sprintf("--format=%s", format))
	if err != nil {
		return nil, err
	}

	commits := make([]CommitInfo, 0)
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 4)
		if len(parts) < 4 {
			continue
		}

		var timestamp int64
		fmt.Sscanf(parts[3], "%d", &timestamp)

		commits = append(commits, CommitInfo{
			Hash:      parts[0],
			Message:   parts[1],
			Author:    parts[2],
			Timestamp: time.Unix(timestamp, 0),
			IsGoClode: strings.Contains(parts[1], "GoClode"),
		})
	}

	return commits, nil
}

// CommitInfo represents a git commit
type CommitInfo struct {
	Hash      string
	Message   string
	Author    string
	Timestamp time.Time
	IsGoClode bool
}

// exec runs a git command and returns output
func (m *Manager) exec(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = m.workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		errMsg := stderr.String()
		if errMsg == "" {
			errMsg = err.Error()
		}
		return "", fmt.Errorf("%s: %s", strings.Join(append([]string{name}, args...), " "), errMsg)
	}

	return stdout.String(), nil
}
