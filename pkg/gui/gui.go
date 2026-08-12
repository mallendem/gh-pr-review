// Package gui provides a Bubble Tea TUI for the bulk approval workflow: pick
// the PR authors to review, walk the content hashes their PRs produced, approve
// or decline each one, then commit the approvals in a batch.
package gui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mallendem/gh-pr-review/pkg/approve"
	"github.com/mallendem/gh-pr-review/pkg/gh"
)

type phase int

const (
	phaseUsers phase = iota
	phaseReview
	phaseSettings
)

// Top-row columns, left to right.
const (
	colHashes = iota
	colChanges
	colPRs
	colStaged
	numColumns
)

const shortHashLen = 6

// model holds the GUI state.
type model struct {
	phase  phase
	set    *gh.ReviewSet
	client *gh.GhClient

	hashes []string
	dec    approve.Decisions

	propagate bool
	dryRun    bool
	status    string

	// Review layout state.
	hashIndex int
	col       int
	focusRow  int             // 0 = top columns, 1 = PR body
	offsets   [numColumns]int // vertical scroll per top column
	changeH   int             // horizontal scroll for the changes column
	occTab    int             // selected occurrence tab in the changes column
	viewport  viewport.Model

	termWidth  int
	termHeight int

	// Settings panel.
	settings       settings
	settingsField  int
	settingsCursor int
	settingsEdit   string

	confirmCommit bool

	// Scrollable log popup, reused for commit results and fetch warnings.
	showLog   bool
	logTitle  string
	logLines  []string
	logOffset int

	// User selection panel.
	availableUsers []string
	userSelected   map[string]bool
	userCursor     int
	userOffset     int
}

// New creates and returns a Bubble Tea program configured for the user.
func New(user string, propagate bool, dryRun bool) (*tea.Program, error) {
	session, err := approve.PrepareGUI(user)
	if err != nil {
		return nil, err
	}

	m := &model{
		// A named user means the hashes are already filtered, so skip selection.
		phase:          phaseUsers,
		set:            session.Set,
		client:         session.Client,
		hashes:         session.Hashes,
		dec:            approve.NewDecisions(),
		propagate:      propagate,
		dryRun:         dryRun,
		availableUsers: session.AvailableUsers,
		userSelected:   map[string]bool{},
		settings:       loadSettings(),
	}
	if user != "" {
		m.phase = phaseReview
		m.selectHash(0)
	}
	if n := len(session.Set.Warnings); n > 0 {
		m.status = fmt.Sprintf("%d PR(s) could not be read — press l for details", n)
	}
	return tea.NewProgram(m, tea.WithAltScreen()), nil
}

// Run starts the GUI program and blocks until it exits.
func Run(user string, propagate bool, dryRun bool) error {
	p, err := New(user, propagate, dryRun)
	if err != nil {
		return err
	}
	_, err = p.Run()
	return err
}

// Init implements tea.Model.
func (m *model) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
		return m, nil

	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			return m, tea.Quit
		}
		switch {
		case m.phase == phaseUsers:
			return m, m.updateUserSelection(msg.String())
		case m.phase == phaseSettings:
			// Handled first so plain letters, including q, reach the editor.
			m.updateSettings(msg)
			return m, nil
		case m.showLog:
			m.updateLog(msg.String())
			return m, nil
		case m.confirmCommit:
			m.updateConfirmation(msg.String())
			return m, nil
		default:
			return m, m.updateReview(msg.String())
		}
	}
	return m, nil
}

func (m *model) resize(width, height int) {
	m.termWidth = width
	m.termHeight = height
	_, bottom := m.rowHeights()
	m.viewport = viewport.New(max(width-borderCells-paddingCells, 10), max(bottom-borderCells-paddingCells, 1))
	m.refreshBody()
}

// --- Geometry ---

// rowHeights returns the outer height (borders included) of the top column row
// and of the PR body box. Together with the header and footer they add up to
// the terminal height exactly.
func (m *model) rowHeights() (top, bottom int) {
	usable := max(m.termHeight-2, 8) // minus header and footer
	bottom = max(usable/3, 6)
	top = usable - bottom
	if top < 5 {
		top = 5
		bottom = max(usable-top, 3)
	}
	return top, bottom
}

// columnWidths returns the content width of each top column. They always sum to
// termWidth-8, so the four bordered boxes occupy the terminal exactly: any
// overshoot makes every row wrap and the whole grid double-spaces.
func (m *model) columnWidths() [numColumns]int {
	total := max(m.termWidth, 40) - numColumns*borderCells

	// Marker, space and the short hash, with slack in case the terminal renders
	// the check mark double-width.
	hash := min(shortHashLen+3+paddingCells+scrollbarCells, total/4)
	staged := min(40, max(total/5, 10))
	prs := min(48, max(total/4, 10))
	mid := total - hash - staged - prs
	// Borrow from the side columns rather than letting the middle overflow.
	for mid < 12 && (prs > 10 || staged > 10) {
		if prs >= staged {
			prs--
		} else {
			staged--
		}
		mid++
	}
	return [numColumns]int{hash, mid, prs, staged}
}

// topContentHeight is the number of lines inside a top column, borders excluded.
func (m *model) topContentHeight() int {
	top, _ := m.rowHeights()
	return max(top-borderCells, 1)
}

// --- Selection ---

func (m *model) selectedHash() string {
	if m.hashIndex >= 0 && m.hashIndex < len(m.hashes) {
		return m.hashes[m.hashIndex]
	}
	return ""
}

// selectHash moves the selection and resets everything scoped to a single hash.
func (m *model) selectHash(index int) {
	if len(m.hashes) == 0 {
		m.hashIndex = 0
		return
	}
	m.hashIndex = min(max(index, 0), len(m.hashes)-1)
	m.occTab = 0
	m.changeH = 0
	m.offsets[colChanges] = 0
	m.offsets[colPRs] = 0
	m.offsets[colStaged] = 0
	m.refreshBody()
}

// refreshBody loads the selected hash's first PR description into the viewport.
func (m *model) refreshBody() {
	body := "(no PR)"
	if prs := m.set.HashPRs[m.selectedHash()]; len(prs) > 0 {
		if b, err := gh.PrBody(prs[0]); err == nil {
			body = b
		} else {
			body = "(no body)"
		}
	}
	m.viewport.SetContent(body)
	m.viewport.GotoTop()
}

// occurrences returns every file/PR location where the selected hash appears.
func (m *model) occurrences() []gh.Occurrence {
	return m.set.Occurrences[m.selectedHash()]
}

// occurrenceLabels renders one tab label per occurrence, disambiguating repeats
// of the same change within one file.
func (m *model) occurrenceLabels() []string {
	occs := m.occurrences()
	labels := make([]string, len(occs))
	counts := map[string]int{}
	for i, o := range occs {
		labels[i] = fmt.Sprintf("%s (%s)", o.File, shortenPRURL(o.PrURL))
		counts[labels[i]]++
	}
	seen := map[string]int{}
	for i, l := range labels {
		if counts[l] > 1 {
			seen[l]++
			labels[i] = fmt.Sprintf("%s #%d", l, seen[l])
		}
	}
	return labels
}

// changeLines returns the diff lines shown for the selected occurrence tab.
func (m *model) changeLines() []string {
	occs := m.occurrences()
	if len(occs) == 0 {
		return m.set.Changes[m.selectedHash()]
	}
	return filterContextLines(occs[min(m.occTab, len(occs)-1)].Raw, m.settings.contextLines)
}

// stagedPRs are the PRs that would be approved if the reviewer committed now.
func (m *model) stagedPRs() []string {
	return m.dec.StagedPRs(m.set.PRHashes)
}

// shortenPRURL turns "https://github.com/owner/repo/pull/123" into "owner/repo#123".
func shortenPRURL(url string) string {
	parts := strings.Split(url, "/")
	if len(parts) >= 7 {
		return fmt.Sprintf("%s/%s#%s", parts[3], parts[4], parts[6])
	}
	return url
}

func shortHash(h string) string {
	if len(h) > shortHashLen {
		return h[:shortHashLen]
	}
	return h
}

// --- Review phase input ---

func (m *model) updateReview(k string) tea.Cmd {
	switch k {
	case "q", "esc":
		return tea.Quit
	case "tab":
		m.focusRow = (m.focusRow + 1) % 2
		return nil
	case "a":
		m.col = max(m.col-1, 0)
		m.focusRow = 0
		return nil
	case "d":
		m.col = min(m.col+1, numColumns-1)
		m.focusRow = 0
		return nil
	case "p":
		m.phase = phaseSettings
		m.settingsField = 0
		m.loadSettingsField()
		return nil
	case "l":
		m.openLog("Warnings", m.set.Warnings)
		return nil
	case "x":
		m.approveSelected()
		return nil
	case "f":
		m.declineSelected()
		return nil
	case "c":
		if len(m.stagedPRs()) == 0 {
			m.status = "no staged PRs to commit"
		} else {
			m.confirmCommit = true
		}
		return nil
	case "alt+a":
		m.changeH = max(m.changeH-1, 0)
		return nil
	case "alt+d":
		widths := m.columnWidths()
		textW := max(widths[colChanges]-paddingCells-scrollbarCells, 4)
		longest := 0
		for _, l := range m.changeLines() {
			longest = max(longest, len([]rune(l)))
		}
		m.changeH = min(m.changeH+1, max(longest-textW, 0))
		return nil
	}

	if m.focusRow == 1 {
		switch k {
		case "w":
			m.viewport.ScrollUp(1)
		case "s":
			m.viewport.ScrollDown(1)
		case "pgup":
			m.viewport.PageUp()
		case "pgdown":
			m.viewport.PageDown()
		}
		return nil
	}

	// e/r step through the occurrence tabs of the changes column.
	if k == "e" || k == "r" {
		if n := len(m.occurrences()); n > 0 {
			if k == "e" {
				m.occTab = max(m.occTab-1, 0)
			} else {
				m.occTab = min(m.occTab+1, n-1)
			}
			m.offsets[colChanges] = 0
			m.changeH = 0
		}
		return nil
	}

	if k != "w" && k != "s" {
		return nil
	}
	delta := 1
	if k == "w" {
		delta = -1
	}
	if m.col == colHashes {
		m.selectHash(m.hashIndex + delta)
		ensureVisible(&m.offsets[colHashes], m.hashIndex, m.paneVisibleLines(colHashes))
		return nil
	}
	total := m.paneTotalLines(m.col)
	visible := m.paneVisibleLines(m.col)
	m.offsets[m.col] = clampOffset(m.offsets[m.col]+delta, total, visible)
	return nil
}

// paneVisibleLines is how many scrolling lines a top column can display.
func (m *model) paneVisibleLines(col int) int {
	header := 0
	if col == colChanges && len(m.occurrences()) > 0 {
		header = 1 // tab bar
	}
	return max(m.topContentHeight()-1-header, 1)
}

// paneTotalLines is how many content lines a top column holds in total.
func (m *model) paneTotalLines(col int) int {
	switch col {
	case colHashes:
		return len(m.hashes)
	case colChanges:
		return len(m.changeLines())
	case colPRs:
		return len(m.relatedPRLines())
	case colStaged:
		return len(m.stagedPRs())
	}
	return 0
}

func (m *model) approveSelected() {
	h := m.selectedHash()
	if h == "" {
		return
	}
	m.dec.Approve(h)
	if m.propagate {
		approve.ApproveLinkedHashes(h, m.dec, m.set)
	}
	m.reconcileSkipped()
	m.status = fmt.Sprintf("approved %s", shortHash(h))
}

func (m *model) declineSelected() {
	h := m.selectedHash()
	if h == "" {
		return
	}
	m.dec.Decline(h)
	approve.DeclineLinkedHashes(h, m.dec, m.set)
	m.reconcileSkipped()
	m.status = fmt.Sprintf("declined %s", shortHash(h))
}

// reconcileSkipped clears the skip flag from PRs that have since become fully
// approved, e.g. because the hash that caused the skip was approved after all.
func (m *model) reconcileSkipped() {
	for prKey, hashes := range m.set.PRHashes {
		if m.dec.Skipped[prKey] && m.dec.PrApproved(hashes) {
			delete(m.dec.Skipped, prKey)
		}
	}
}

// --- Review phase rendering ---

// View implements tea.Model.
func (m *model) View() string {
	switch {
	case m.phase == phaseUsers:
		return m.viewUserSelection()
	case m.phase == phaseSettings:
		return m.viewSettings()
	case m.showLog:
		return m.viewLog()
	case m.confirmCommit:
		return m.viewConfirmation()
	}

	widths := m.columnWidths()
	height := m.topContentHeight()

	panes := []pane{
		m.hashPane(widths[colHashes]),
		m.changePane(widths[colChanges]),
		m.prPane(widths[colPRs]),
		m.stagedPane(widths[colStaged]),
	}
	boxes := make([]string, len(panes))
	for i := range panes {
		panes[i].focused = m.focusRow == 0 && m.col == i
		panes[i].offset = clampOffset(m.offsets[i], len(panes[i].lines), panes[i].visibleLines(height))
		m.offsets[i] = panes[i].offset
		boxes[i] = panes[i].render(height)
	}
	top := lipgloss.JoinHorizontal(lipgloss.Top, boxes...)

	_, bottomOuter := m.rowHeights()
	bottomStyle := lipgloss.NewStyle().
		Width(max(m.termWidth-borderCells, 10)).
		Height(max(bottomOuter-borderCells, 1)).
		Border(lipgloss.NormalBorder()).
		Padding(0, 1)
	if m.focusRow == 1 {
		bottomStyle = bottomStyle.BorderForeground(focusBorder)
	}
	body := m.viewport.View()
	if strings.TrimSpace(body) == "" {
		body = "(no PR body)"
	}

	selected := "-"
	if h := m.selectedHash(); h != "" {
		selected = shortHash(h)
	}
	header := fit(fmt.Sprintf("hash %d/%d: %s | staged: %d | %s",
		min(m.hashIndex+1, len(m.hashes)), len(m.hashes), selected, len(m.stagedPRs()), m.status), m.termWidth)
	footer := fit(" tab: row • a/d: column • w/s: scroll • e/r: file • x: approve • f: decline • c: commit • p: settings • l: log • q: quit", m.termWidth)

	return lipgloss.JoinVertical(lipgloss.Left, header, top, bottomStyle.Render(body), footer)
}

func (m *model) hashPane(width int) pane {
	lines := make([]styledLine, 0, len(m.hashes))
	for _, h := range m.hashes {
		marker, style := " ", lipgloss.NewStyle()
		switch {
		case m.dec.Approved[h]:
			marker, style = "✓", addStyle
		case m.dec.Declined[h]:
			marker, style = "x", delStyle
		}
		lines = append(lines, styled(marker+" "+shortHash(h), style))
	}
	selected := -1
	if m.focusRow == 0 && m.col == colHashes && len(lines) > 0 {
		selected = m.hashIndex
	}
	return pane{title: "Hashes", lines: lines, selected: selected, width: width}
}

func (m *model) changePane(width int) pane {
	p := pane{title: "Changes", width: width, selected: -1}
	textW := max(width-paddingCells-scrollbarCells, 4)
	if tabs := m.renderTabBar(textW); tabs != "" {
		p.header = []string{tabs}
	}
	for _, l := range m.changeLines() {
		style := lipgloss.NewStyle()
		switch {
		case strings.HasPrefix(l, "+"):
			style = addStyle
		case strings.HasPrefix(l, "-"):
			style = delStyle
		case l == "...":
			style = dimStyle
		}
		p.lines = append(p.lines, styled(hscroll(l, m.changeH, textW), style))
	}
	if len(p.lines) == 0 {
		p.lines = []styledLine{plain("(no changes)")}
	}
	return p
}

// hscroll applies the horizontal offset to a diff line, marking both edges when
// content is cut off.
func hscroll(line string, offset, width int) string {
	r := []rune(line)
	if offset >= len(r) {
		return ""
	}
	out := string(r[offset:min(offset+width, len(r))])
	if offset > 0 {
		out = "«" + out
	}
	if len(r) > offset+width {
		out += "»"
	}
	return out
}

// renderTabBar draws the occurrence tabs, collapsing to "◄ [2/5] name ►" when
// the full list does not fit.
func (m *model) renderTabBar(width int) string {
	labels := m.occurrenceLabels()
	if len(labels) == 0 {
		return ""
	}
	tab := min(max(m.occTab, 0), len(labels)-1)

	plainLen := 0
	for _, l := range labels {
		plainLen += len(l) + 3
	}
	if plainLen <= width {
		parts := make([]string, len(labels))
		for i, l := range labels {
			if i == tab {
				parts[i] = selectedStyle.Bold(true).Render(" " + l + " ")
			} else {
				parts[i] = " " + l + " "
			}
		}
		return strings.Join(parts, "│")
	}

	prefix := fmt.Sprintf("◄ [%d/%d] ", tab+1, len(labels))
	suffix := " ►"
	maxName := max(width-len(prefix)-len(suffix), 3)
	name := labels[tab]
	if len(name) > maxName {
		name = "…" + name[len(name)-maxName+1:]
	}
	return prefix + titleStyle.Render(name) + suffix
}

// relatedPRLines lists the PRs containing the selected hash, each followed by
// the other hashes that PR depends on.
func (m *model) relatedPRLines() []styledLine {
	var lines []styledLine
	prs := m.set.HashPRs[m.selectedHash()]
	if len(prs) == 0 {
		return []styledLine{plain("(no PRs)")}
	}
	for i, pr := range prs {
		prKey := pr.GetHTMLURL()
		lines = append(lines, m.prLabel(prKey, i))
		linked := m.set.PRHashes[prKey]
		for j, lh := range linked {
			connector := "├─"
			if j == len(linked)-1 {
				connector = "└─"
			}
			marker, style := "✓", addStyle
			switch {
			case m.dec.Declined[lh]:
				marker, style = "×", delStyle
			case !m.dec.Approved[lh]:
				marker, style = "?", dimStyle
			}
			lines = append(lines, styled(fmt.Sprintf("  %s %s %s", connector, shortHash(lh), marker), style))
		}
	}
	return lines
}

func (m *model) prPane(width int) pane {
	return pane{title: "Related PRs", lines: m.relatedPRLines(), selected: -1, width: width}
}

func (m *model) stagedPane(width int) pane {
	staged := m.stagedPRs()
	p := pane{title: "Staged changes", selected: -1, width: width}
	if len(staged) == 0 {
		p.lines = []styledLine{plain("(no staged PRs)")}
		return p
	}
	for i, prKey := range staged {
		p.lines = append(p.lines, m.prLabel(prKey, i))
	}
	return p
}

// prLabel renders a PR entry coloured by its current decision state.
func (m *model) prLabel(prKey string, idx int) styledLine {
	text := fmt.Sprintf("[%d] %s %s", idx+1, approve.VerifiedIcon(m.set.Verified[prKey]), shortenPRURL(prKey))
	hashes := m.set.PRHashes[prKey]
	switch {
	case m.dec.PrCommitted(hashes):
		return styled(text, committedStyl)
	case m.dec.PrDeclined(hashes):
		return styled(text, delStyle)
	case m.dec.PrApproved(hashes):
		return styled(text, addStyle)
	}
	return plain(text)
}

// --- User selection phase ---

func (m *model) updateUserSelection(k string) tea.Cmd {
	visible := max(m.termHeight-6, 1)
	switch k {
	case "q", "esc":
		return tea.Quit
	case "w", "up":
		m.userCursor = max(m.userCursor-1, 0)
		ensureVisible(&m.userOffset, m.userCursor, visible)
	case "s", "down":
		m.userCursor = min(m.userCursor+1, len(m.availableUsers)-1)
		ensureVisible(&m.userOffset, m.userCursor, visible)
	case " ", "x":
		if m.userCursor >= 0 && m.userCursor < len(m.availableUsers) {
			u := m.availableUsers[m.userCursor]
			m.userSelected[u] = !m.userSelected[u]
		}
	case "enter":
		var selected []string
		for _, u := range m.availableUsers {
			if m.userSelected[u] {
				selected = append(selected, u)
			}
		}
		if len(selected) == 0 {
			m.status = "select at least one user"
			return nil
		}
		m.hashes = approve.CollectHashesForUsers(strings.Join(selected, ","), m.set.UsersToHashes)
		m.phase = phaseReview
		m.selectHash(0)
	}
	return nil
}

// userPRCount returns the number of unique PRs opened by a user.
func (m *model) userPRCount(user string) int {
	seen := map[string]struct{}{}
	for _, prs := range m.set.UsersToHashes[user] {
		for _, pr := range prs {
			seen[pr.GetHTMLURL()] = struct{}{}
		}
	}
	return len(seen)
}

func (m *model) viewUserSelection() string {
	visible := max(m.termHeight-6, 1)
	width := max(m.termWidth-borderCells, 30)

	var lines []string
	for i, u := range m.availableUsers {
		check := "[ ]"
		if m.userSelected[u] {
			check = "[x]"
		}
		label := fit(fmt.Sprintf("%s %s (%d PRs)", check, u, m.userPRCount(u)), width-paddingCells)
		if i == m.userCursor {
			label = selectedStyle.Render(label)
		}
		lines = append(lines, label)
	}
	offset := clampOffset(m.userOffset, len(lines), visible)

	panel := lipgloss.NewStyle().
		Width(width).
		Height(visible).
		Border(lipgloss.NormalBorder()).
		Padding(0, 1).
		Render(strings.Join(window(lines, offset, visible), "\n"))

	return lipgloss.JoinVertical(lipgloss.Left,
		titleStyle.Render("Select users to review (space/x: toggle, enter: confirm, q: quit)"),
		panel,
		fit(m.status, max(m.termWidth, 1)))
}

// --- Settings phase ---

var settingsFields = []string{"Review comment", "Context lines"}

func (m *model) updateSettings(msg tea.KeyMsg) {
	switch msg.String() {
	case "esc":
		m.phase = phaseReview
		m.status = "settings unchanged"
	case "tab":
		m.saveSettingsField()
		m.settingsField = (m.settingsField + 1) % len(settingsFields)
		m.loadSettingsField()
	case "enter":
		m.saveSettingsField()
		m.phase = phaseReview
		m.changeH = 0
		m.offsets[colChanges] = 0
		if err := m.settings.save(); err != nil {
			m.status = fmt.Sprintf("settings applied but not saved: %v", err)
		} else {
			m.status = "settings saved"
		}
	case "backspace":
		if m.settingsCursor > 0 {
			r := []rune(m.settingsEdit)
			m.settingsEdit = string(r[:m.settingsCursor-1]) + string(r[m.settingsCursor:])
			m.settingsCursor--
		}
	case "left":
		m.settingsCursor = max(m.settingsCursor-1, 0)
	case "right":
		m.settingsCursor = min(m.settingsCursor+1, len([]rune(m.settingsEdit)))
	case "home":
		m.settingsCursor = 0
	case "end":
		m.settingsCursor = len([]rune(m.settingsEdit))
	default:
		// Only insert real text. Matching on the key *name* would type "up" or
		// "pgdown" into the field.
		var typed []rune
		switch msg.Type {
		case tea.KeyRunes:
			typed = msg.Runes
		case tea.KeySpace:
			typed = []rune{' '}
		default:
			return
		}
		r := []rune(m.settingsEdit)
		m.settingsEdit = string(r[:m.settingsCursor]) + string(typed) + string(r[m.settingsCursor:])
		m.settingsCursor += len(typed)
	}
}

func (m *model) loadSettingsField() {
	if m.settingsField == 0 {
		m.settingsEdit = m.settings.reviewComment
	} else {
		m.settingsEdit = fmt.Sprintf("%d", m.settings.contextLines)
	}
	m.settingsCursor = len([]rune(m.settingsEdit))
}

func (m *model) saveSettingsField() {
	if m.settingsField == 0 {
		m.settings.reviewComment = m.settingsEdit
		return
	}
	n := 0
	for _, ch := range m.settingsEdit {
		if ch >= '0' && ch <= '9' {
			n = n*10 + int(ch-'0')
		}
	}
	m.settings.contextLines = n
}

func (m *model) viewSettings() string {
	values := []string{m.settings.reviewComment, fmt.Sprintf("%d", m.settings.contextLines)}
	width := max(m.termWidth-borderCells, 40)

	lines := make([]string, len(settingsFields))
	for i, label := range settingsFields {
		display := values[i]
		if i == m.settingsField {
			r := []rune(m.settingsEdit)
			display = string(r[:m.settingsCursor]) + "█" + string(r[m.settingsCursor:])
		}
		lines[i] = fit(fmt.Sprintf("  %s: %s", label, display), width-paddingCells)
		if i == m.settingsField {
			lines[i] = selectedStyle.Render(lines[i])
		}
	}

	panel := lipgloss.NewStyle().
		Width(width).
		Border(lipgloss.NormalBorder()).
		Padding(1).
		Render(strings.Join(lines, "\n"))

	return lipgloss.JoinVertical(lipgloss.Left,
		titleStyle.Render("Settings (tab: switch field, enter: save & close, esc: cancel)"),
		panel)
}

// --- Confirmation dialog ---

func (m *model) updateConfirmation(k string) {
	switch k {
	case "y":
		m.commit()
	case "n", "esc":
		m.confirmCommit = false
		m.status = "commit cancelled"
	}
}

func (m *model) commit() {
	staged := m.stagedPRs()
	logs := approve.ProcessApprovals(m.set, m.dec, m.client, m.dryRun, m.settings.reviewComment)
	for _, prKey := range staged {
		for _, h := range m.set.PRHashes[prKey] {
			m.dec.Committed[h] = true
		}
	}
	m.reconcileSkipped()
	m.confirmCommit = false
	m.status = fmt.Sprintf("committed %d PR(s)", len(staged))
	m.refreshBody()
	m.openLog("Commit log", logs)
}

func (m *model) viewConfirmation() string {
	lines := []string{titleStyle.Render("Confirm approval of the following PRs?"), ""}
	for _, prKey := range m.stagedPRs() {
		lines = append(lines, fmt.Sprintf("  %s %s", approve.VerifiedIcon(m.set.Verified[prKey]), shortenPRURL(prKey)))
	}
	lines = append(lines, "")
	if m.settings.reviewComment != "" {
		lines = append(lines, fmt.Sprintf("  Review comment: %s", m.settings.reviewComment), "")
	}
	if m.dryRun {
		lines = append(lines, dimStyle.Render("  dry-run: nothing will be submitted"), "")
	}
	lines = append(lines, titleStyle.Render("  Press 'y' to confirm, 'n' to cancel"))

	width := min(max(m.termWidth-10, 60), 100)
	dialog := lipgloss.NewStyle().
		Width(width).
		Border(lipgloss.DoubleBorder()).
		BorderForeground(focusBorder).
		Padding(1, 2).
		Render(strings.Join(lines, "\n"))

	return lipgloss.Place(m.termWidth, m.termHeight, lipgloss.Center, lipgloss.Center, dialog)
}

// --- Log popup ---

func (m *model) openLog(title string, lines []string) {
	if len(lines) == 0 {
		m.status = "nothing to show in " + strings.ToLower(title)
		return
	}
	m.logTitle = title
	m.logLines = lines
	m.logOffset = 0
	m.showLog = true
}

func (m *model) logVisibleLines() int {
	return max(m.termHeight-4, 1)
}

func (m *model) updateLog(k string) {
	visible := m.logVisibleLines()
	switch k {
	case "enter", "q", "esc":
		m.showLog = false
	case "w", "up":
		m.logOffset = clampOffset(m.logOffset-1, len(m.logLines), visible)
	case "s", "down":
		m.logOffset = clampOffset(m.logOffset+1, len(m.logLines), visible)
	case "pgup":
		m.logOffset = clampOffset(m.logOffset-visible, len(m.logLines), visible)
	case "pgdown":
		m.logOffset = clampOffset(m.logOffset+visible, len(m.logLines), visible)
	}
}

func (m *model) viewLog() string {
	visible := m.logVisibleLines()
	offset := clampOffset(m.logOffset, len(m.logLines), visible)
	width := max(m.termWidth-borderCells, 40)
	textW := max(width-paddingCells-scrollbarCells, 10)

	bar := scrollbar(visible, len(m.logLines), offset)
	rows := make([]string, visible)
	for i := range visible {
		line := ""
		if idx := offset + i; idx < len(m.logLines) {
			line = m.logLines[idx]
		}
		rows[i] = fit(line, textW) + " " + bar[i]
	}

	panel := lipgloss.NewStyle().
		Width(width).
		Height(visible).
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("10")).
		Padding(0, 1).
		Render(strings.Join(rows, "\n"))

	return lipgloss.JoinVertical(lipgloss.Left,
		titleStyle.Render(fmt.Sprintf("%s (%d/%d lines) — w/s: scroll • enter/q: close",
			m.logTitle, min(offset+visible, len(m.logLines)), len(m.logLines))),
		panel)
}

// --- Context lines filtering ---

// filterContextLines keeps only n context lines around each +/- line, inserting
// "..." markers where context was elided.
func filterContextLines(lines []string, n int) []string {
	if n <= 0 {
		var result []string
		for _, l := range lines {
			if strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-") {
				result = append(result, l)
			}
		}
		return result
	}

	keep := make([]bool, len(lines))
	for i, l := range lines {
		if !strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "-") {
			continue
		}
		for j := max(0, i-n); j <= min(len(lines)-1, i+n); j++ {
			keep[j] = true
		}
	}

	var result []string
	lastKept := -1
	for i, l := range lines {
		if !keep[i] {
			continue
		}
		if lastKept >= 0 && i-lastKept > 1 {
			result = append(result, "...")
		}
		result = append(result, l)
		lastKept = i
	}
	return result
}
