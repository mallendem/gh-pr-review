package gui

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const settingsFileName = ".gh-pr-approver"

// settings holds user-configurable options for the GUI.
type settings struct {
	reviewComment string // comment to leave on approved PRs
	contextLines  int    // number of context lines to show around changes
}

// defaultSettings returns settings with default values.
func defaultSettings() settings {
	return settings{
		reviewComment: "This change has been reviewed by a human with a batch tool.",
		contextLines:  10,
	}
}

func settingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, settingsFileName), nil
}

// loadSettings reads ~/.gh-pr-approver if it exists and overrides defaults. The
// file uses a simple "key = value" format (one per line). Supported keys:
// review_comment, context_lines.
func loadSettings() settings {
	s := defaultSettings()
	path, err := settingsPath()
	if err != nil {
		return s
	}
	f, err := os.Open(path)
	if err != nil {
		return s
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "review_comment":
			s.reviewComment = strings.TrimSpace(value)
		case "context_lines":
			if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && n >= 0 {
				s.contextLines = n
			}
		}
	}
	return s
}

// save writes the settings back to ~/.gh-pr-approver so they survive a restart.
func (s settings) save() error {
	path, err := settingsPath()
	if err != nil {
		return err
	}
	content := fmt.Sprintf("review_comment = %s\ncontext_lines = %d\n", s.reviewComment, s.contextLines)
	return os.WriteFile(path, []byte(content), 0o600)
}
