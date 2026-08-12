package gui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mallendem/gh-pr-review/pkg/approve"
	"github.com/mallendem/gh-pr-review/pkg/gh"
)

func testModel(width, height int) *model {
	hashes := []string{
		"d6e3a6111111", "d7ac24222222", "d7cfa8333333", "d7fa1d444444",
		"d90282555555", "dc3b82666666", "dc47d0777777", "dc5443888888",
	}
	set := &gh.ReviewSet{
		UsersToHashes: gh.GhPrHashMap{},
		Changes:       gh.HashChangeMap{},
		HashPRs:       gh.HashPrMap{},
		PRHashes:      gh.PrHashMap{},
		Verified:      gh.PrVerifiedMap{},
		Occurrences:   gh.HashOccurrences{},
	}
	for i, h := range hashes {
		set.Changes[h] = []string{"-old " + h, "+new " + h}
		set.PRHashes[fmt.Sprintf("https://github.com/elastic/some-long-repo-name/pull/%d", 1000+i)] = []string{h}
		set.Occurrences[h] = []gh.Occurrence{
			{
				PrURL: fmt.Sprintf("https://github.com/elastic/some-long-repo-name/pull/%d", 1000+i),
				File:  ".github/workflows/docker-publish.yml",
				Raw:   []string{" ctx", "-old " + h, "+new " + h, " ctx"},
			},
			{
				PrURL: fmt.Sprintf("https://github.com/elastic/other-repo/pull/%d", 2000+i),
				File:  ".github/workflows/updatecli.yml",
				Raw:   []string{" other ctx", "-old " + h, "+new " + h},
			},
		}
	}

	m := &model{
		phase:      phaseReview,
		set:        set,
		hashes:     hashes,
		dec:        approve.NewDecisions(),
		settings:   defaultSettings(),
		termWidth:  width,
		termHeight: height,
		status:     "ready",
	}
	m.dec.Approve(hashes[1])
	m.dec.Decline(hashes[0])
	return m
}

// assertFits is the regression guard for the bug that made every row wrap and
// the grid double-space: no rendered line may exceed the terminal width.
func assertFits(t *testing.T, view string, width, height int) {
	t.Helper()
	rows := strings.Split(view, "\n")
	for i, row := range rows {
		if w := lipgloss.Width(row); w > width {
			t.Errorf("row %d is %d cells wide, terminal is %d:\n%s", i, w, width, row)
		}
	}
	if len(rows) > height {
		t.Errorf("view is %d rows tall, terminal is %d", len(rows), height)
	}
}

func TestViewFitsTerminal(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}, {200, 50}, {240, 60}, {60, 20}, {45, 12}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m := testModel(size[0], size[1])
			m.resize(size[0], size[1])
			assertFits(t, m.View(), size[0], size[1])
		})
	}
}

func TestViewFitsWithFocusAndSelection(t *testing.T) {
	for col := range numColumns {
		m := testModel(120, 40)
		m.resize(120, 40)
		m.col = col
		m.hashIndex = 3
		assertFits(t, m.View(), 120, 40)
	}
}

func TestColumnWidthsSumToTerminal(t *testing.T) {
	for w := 40; w <= 300; w++ {
		m := testModel(w, 40)
		widths := m.columnWidths()
		total := numColumns * borderCells
		for _, cw := range widths {
			if cw < 1 {
				t.Fatalf("width %d produced a non-positive column: %v", w, widths)
			}
			total += cw
		}
		if total != w {
			t.Fatalf("terminal width %d: columns occupy %d cells (%v)", w, total, widths)
		}
	}
}

// The hash column must leave room for its own scrollbar; not doing so was what
// pushed the glyph onto its own line.
func TestHashColumnFitsMarkerHashAndScrollbar(t *testing.T) {
	m := testModel(120, 40)
	widths := m.columnWidths()
	p := m.hashPane(widths[colHashes])
	rendered := p.render(m.topContentHeight())
	for i, row := range strings.Split(rendered, "\n") {
		if w := lipgloss.Width(row); w != widths[colHashes]+borderCells {
			t.Fatalf("hash pane row %d is %d cells, want %d", i, w, widths[colHashes]+borderCells)
		}
	}
	if !strings.Contains(rendered, "d6e3a6") {
		t.Fatalf("hash pane dropped the hash text:\n%s", rendered)
	}
}

func TestEAndRSwitchOccurrenceContent(t *testing.T) {
	m := testModel(120, 40)
	m.resize(120, 40)
	m.col = colChanges

	first := strings.Join(m.changeLines(), "\n")
	m.updateReview("r")
	second := strings.Join(m.changeLines(), "\n")
	if first == second {
		t.Fatalf("r did not change the displayed diff, still:\n%s", first)
	}
	if !strings.Contains(second, "other ctx") {
		t.Fatalf("expected the second occurrence's context, got:\n%s", second)
	}

	m.updateReview("e")
	if got := strings.Join(m.changeLines(), "\n"); got != first {
		t.Fatalf("e did not return to the first occurrence:\n%s", got)
	}

	// Stepping past either end must clamp rather than panic.
	m.updateReview("e")
	m.updateReview("e")
	for range 5 {
		m.updateReview("r")
	}
	if m.occTab != len(m.occurrences())-1 {
		t.Fatalf("occTab = %d, want %d", m.occTab, len(m.occurrences())-1)
	}
}

func TestSelectingHashResetsOccurrenceTab(t *testing.T) {
	m := testModel(120, 40)
	m.resize(120, 40)
	m.occTab = 1
	m.selectHash(2)
	if m.occTab != 0 {
		t.Fatalf("occTab = %d after changing hash, want 0", m.occTab)
	}
}

func TestScrollOffsetsPersistAcrossRenders(t *testing.T) {
	m := testModel(120, 12) // short terminal so the hash list overflows
	m.resize(120, 12)
	m.col = colHashes
	for range 6 {
		m.updateReview("s")
	}
	m.View()
	if m.offsets[colHashes] == 0 {
		t.Fatal("hash column never scrolled; offset was reset by render")
	}
	if m.hashIndex != 6 {
		t.Fatalf("hashIndex = %d, want 6", m.hashIndex)
	}
}

func TestQuitKeyDoesNotLeakIntoSettingsEditor(t *testing.T) {
	m := testModel(120, 40)
	m.resize(120, 40)
	m.updateReview("p")
	if m.phase != phaseSettings {
		t.Fatal("p did not open the settings panel")
	}
	m.settingsEdit = ""
	m.settingsCursor = 0
	for _, r := range "quick" {
		m.updateSettings(runeKey(r))
	}
	if m.settingsEdit != "quick" {
		t.Fatalf("settings editor = %q, want %q", m.settingsEdit, "quick")
	}
	if m.phase != phaseSettings {
		t.Fatal("typing q closed the settings panel")
	}
}

func TestSettingsEditorIgnoresNavigationKeyNames(t *testing.T) {
	m := testModel(120, 40)
	m.settingsField = 0
	m.settingsEdit = "hello"
	m.settingsCursor = 5
	for _, name := range []string{"up", "down", "pgup", "pgdown"} {
		m.updateSettings(namedKey(name))
	}
	if m.settingsEdit != "hello" {
		t.Fatalf("navigation keys were typed into the field: %q", m.settingsEdit)
	}
}

func TestStagedPRsRequireEveryHashApproved(t *testing.T) {
	m := testModel(120, 40)
	prKey := "https://github.com/elastic/multi/pull/1"
	m.set.PRHashes[prKey] = []string{"h1", "h2"}

	m.dec.Approve("h1")
	if contains(m.stagedPRs(), prKey) {
		t.Fatal("PR staged with only one of two hashes approved")
	}
	m.dec.Approve("h2")
	if !contains(m.stagedPRs(), prKey) {
		t.Fatal("PR not staged after all hashes approved")
	}
	m.dec.Decline("h2")
	if contains(m.stagedPRs(), prKey) {
		t.Fatal("PR still staged after a hash was declined")
	}
}

func TestFilterContextLines(t *testing.T) {
	lines := []string{"a", "b", "c", "d", "-e", "+f", "g", "h", "i", "j"}

	if got := filterContextLines(lines, 0); strings.Join(got, ",") != "-e,+f" {
		t.Fatalf("n=0 kept %v", got)
	}
	got := filterContextLines(lines, 1)
	if strings.Join(got, ",") != "d,-e,+f,g" {
		t.Fatalf("n=1 kept %v", got)
	}
	// A gap between kept regions must be marked.
	gapped := filterContextLines([]string{"-a", "x", "x", "x", "x", "x", "-b"}, 1)
	if !contains(gapped, "...") {
		t.Fatalf("expected a gap marker in %v", gapped)
	}
}

func TestShortenPRURL(t *testing.T) {
	got := shortenPRURL("https://github.com/elastic/apm-data/pull/623")
	if got != "elastic/apm-data#623" {
		t.Fatalf("got %q", got)
	}
	if got := shortenPRURL("nonsense"); got != "nonsense" {
		t.Fatalf("malformed URL should pass through, got %q", got)
	}
}

func TestOccurrenceLabelsDisambiguateRepeats(t *testing.T) {
	m := testModel(120, 40)
	h := m.hashes[0]
	m.set.Occurrences[h] = []gh.Occurrence{
		{PrURL: "https://github.com/elastic/r/pull/1", File: "a.yml"},
		{PrURL: "https://github.com/elastic/r/pull/1", File: "a.yml"},
		{PrURL: "https://github.com/elastic/r/pull/2", File: "b.yml"},
	}
	labels := m.occurrenceLabels()
	if labels[0] == labels[1] {
		t.Fatalf("repeated occurrences share a label: %v", labels)
	}
	if strings.Contains(labels[2], "#1") && strings.Contains(labels[2], "b.yml #") {
		t.Fatalf("unique occurrence was needlessly suffixed: %v", labels)
	}
}

func TestScrollbarThumbStaysInRange(t *testing.T) {
	for _, total := range []int{0, 1, 5, 10, 11, 100, 1000} {
		for _, visible := range []int{1, 3, 10} {
			for _, offset := range []int{0, 1, total / 2, max(total-visible, 0)} {
				bar := scrollbar(visible, total, clampOffset(offset, total, visible))
				if len(bar) != visible {
					t.Fatalf("scrollbar(%d,%d,%d) returned %d glyphs", visible, total, offset, len(bar))
				}
			}
		}
	}
}

func TestFitExactWidth(t *testing.T) {
	for _, s := range []string{"", "abc", "a much longer line than the width", "✓ d6e3a6", "«diff»"} {
		for _, w := range []int{1, 4, 8, 20} {
			if got := lipgloss.Width(fit(s, w)); got != w {
				t.Errorf("fit(%q, %d) is %d cells wide", s, w, got)
			}
		}
	}
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func runeKey(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

func namedKey(name string) tea.KeyMsg {
	switch name {
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "pgup":
		return tea.KeyMsg{Type: tea.KeyPgUp}
	case "pgdown":
		return tea.KeyMsg{Type: tea.KeyPgDown}
	}
	panic("unknown key " + name)
}
