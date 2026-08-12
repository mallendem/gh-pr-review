package gh

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// maxStoredContext bounds how many context lines are kept on either side of a
// change block for display. The GUI trims further based on its own setting.
const maxStoredContext = 25

// diffBlock is one consecutive run of +/- lines inside a hunk, together with
// the file it belongs to and enough surrounding context to render it.
type diffBlock struct {
	hash  string
	file  string
	lines []string // normalized +/- lines — the hashed content
	raw   []string // the block plus surrounding context from its hunk
}

// parseDiff splits a unified diff into change blocks, one per consecutive run
// of +/- lines. Hashing per block rather than per hunk matters because git
// merges hunks whose changes are within a few lines of each other: the same
// logical change would otherwise hash differently depending on what happens to
// sit near it in a given repo.
func parseDiff(diff string) []diffBlock {
	var blocks []diffBlock
	var hunk []string
	file := ""
	inHunk := false

	flush := func() {
		blocks = append(blocks, splitHunk(hunk, file)...)
		hunk = nil
	}

	// The trailing newline would otherwise show up as a blank context line.
	for _, line := range strings.Split(strings.TrimSuffix(diff, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			inHunk = false
			if idx := strings.LastIndex(line, " b/"); idx >= 0 {
				file = line[idx+3:]
			}
		case strings.HasPrefix(line, "@@"):
			flush()
			inHunk = true
		case inHunk:
			hunk = append(hunk, line)
		}
	}
	flush()
	return blocks
}

// splitHunk turns a single hunk's lines into one block per run of +/- lines.
func splitHunk(hunk []string, file string) []diffBlock {
	var blocks []diffBlock
	for i := 0; i < len(hunk); {
		if !isChangeLine(hunk[i]) {
			i++
			continue
		}
		start := i
		var lines []string
		for i < len(hunk) && isChangeLine(hunk[i]) {
			lines = append(lines, normalizeHunkLine(hunk[i], file))
			i++
		}
		sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
		blocks = append(blocks, diffBlock{
			hash:  hex.EncodeToString(sum[:]),
			file:  file,
			lines: lines,
			raw:   hunk[max(0, start-maxStoredContext):min(len(hunk), i+maxStoredContext)],
		})
	}
	return blocks
}

func isChangeLine(line string) bool {
	return strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")
}
