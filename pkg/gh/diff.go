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
	// Locate every run first, so a block's context can stop short of its
	// neighbours. Letting it run on made the changes pane show a neighbouring
	// bump as if the selected hash covered it too.
	type span struct{ start, end int } // end is exclusive
	var spans []span
	for i := 0; i < len(hunk); {
		if !isChangeLine(hunk[i]) {
			i++
			continue
		}
		start := i
		for i < len(hunk) && isChangeLine(hunk[i]) {
			i++
		}
		spans = append(spans, span{start, i})
	}

	blocks := make([]diffBlock, 0, len(spans))
	for n, s := range spans {
		lines := make([]string, 0, s.end-s.start)
		for _, line := range hunk[s.start:s.end] {
			lines = append(lines, normalizeHunkLine(line, file))
		}

		// Context between two blocks belongs to both of them.
		from := max(s.start-maxStoredContext, 0)
		if n > 0 {
			from = max(from, spans[n-1].end)
		}
		to := min(s.end+maxStoredContext, len(hunk))
		if n+1 < len(spans) {
			to = min(to, spans[n+1].start)
		}

		sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
		blocks = append(blocks, diffBlock{
			hash:  hex.EncodeToString(sum[:]),
			file:  file,
			lines: lines,
			raw:   hunk[from:to],
		})
	}
	return blocks
}

func isChangeLine(line string) bool {
	return strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")
}
