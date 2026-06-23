package gui

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mallendem/gh-pr-review/pkg/approve"
	"github.com/mallendem/gh-pr-review/pkg/gh"
)

// This package provides a simple Bubble Tea-based TUI to perform the
// same workflow as ManualApproval but visually. It loads hashes, changes
// and PRs for a user and allows navigation and approvals.

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

// loadSettingsFromFile reads ~/.gh-pr-approver if it exists and overrides
// defaults. The file uses a simple "key = value" format (one per line).
// Supported keys: review_comment, context_lines.
func loadSettingsFromFile() settings {
	s := defaultSettings()
	home, err := os.UserHomeDir()
	if err != nil {
		return s
	}
	f, err := os.Open(filepath.Join(home, ".gh-pr-approver"))
	if err != nil {
		return s
	}
	defer f.Close()

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
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "review_comment":
			s.reviewComment = value
		case "context_lines":
			if n, err := strconv.Atoi(value); err == nil && n >= 0 {
				s.contextLines = n
			}
		}
	}
	return s
}

// model holds the GUI state.
type model struct {
	// phase: 0 = user selection, 1 = approval, 2 = settings
	phase int

	hashes       []string
	changeMap    gh.HashChangeMap
	rawChangeMap gh.HashRawChangeMap
	hashPrMap    gh.HashPrMap
	prMap        map[string][]string
	verifiedMap  gh.PrVerifiedMap
	client       *gh.GhClient

	approved  map[string]bool
	declined  map[string]bool
	prSkipped map[string]bool
	committed map[string]bool
	propagate bool
	hashIndex int // which hash list item is selected
	col       int // 0-left(hash),1-middle(change),2-right(prs)

	status string
	dryRun bool

	// Settings and confirmation
	settings      settings
	confirmCommit bool // when true, show confirmation dialog overlay
	settingsField int  // 0 = reviewComment, 1 = contextLines
	settingsCursor int // cursor position within current settings field edit
	settingsEdit  string // current edit buffer for settings field

	// Commit log popup
	commitLog       []string
	showCommitLog   bool
	commitLogOffset int

	// UI layout state
	viewport     viewport.Model
	focusRow     int // 0 = top row, 1 = bottom row
	termWidth    int
	termHeight   int
	bottomHeight int // height allocated to bottom (viewport) including borders
	topHeight    int // height for the top row columns including borders
	// offsets for scrolling in the top three columns
	hashOffset    int
	changeOffset  int
	prOffset      int
	stagedOffset  int
	stagedPRList  []string
	changeHOffset int // horizontal offset for changes column

	// File tab state for changes panel
	hashFileMap   gh.HashFileMap // hash → filename
	changeFileTab int            // index of selected file tab

	// User selection phase (phase 0) fields
	availableUsers   []string
	userSelected     map[string]bool
	userCursor       int
	userHashPrMap    gh.GhPrHashMap
	userScrollOffset int
}

// New creates and returns a Bubble Tea program configured for the user.
func New(user string, propagate bool, dryRun bool) (*tea.Program, error) {
	hashes, availableUsers, userHashPrMap, changeMap, hashPrMap, prMap, verifiedMap, hashFileMap, rawChangeMap, client, err := approve.PrepareGUI(user)
	if err != nil {
		return nil, err
	}

	// If user was provided (hashes already filtered), go straight to phase 1.
	// Otherwise start in phase 0 (user selection).
	phase := 0
	if user != "" {
		phase = 1
	}

	m := model{
		phase:          phase,
		hashes:         hashes,
		changeMap:      changeMap,
		rawChangeMap:   rawChangeMap,
		hashPrMap:      hashPrMap,
		prMap:          prMap,
		verifiedMap:    verifiedMap,
		client:         client,
		approved:       map[string]bool{},
		declined:       map[string]bool{},
		prSkipped:      map[string]bool{},
		committed:      map[string]bool{},
		propagate:      propagate,
		col:            0,
		dryRun:         dryRun,
		focusRow:       0,
		stagedOffset:   0,
		stagedPRList:   nil,
		hashFileMap:    hashFileMap,
		availableUsers: availableUsers,
		userSelected:   map[string]bool{},
		userCursor:     0,
		userHashPrMap:  userHashPrMap,
		settings:       loadSettingsFromFile(),
	}
	if phase == 1 {
		// compute initial staged list so the UI shows consistent state immediately
		m.updateStagedList()
	}
	// viewport will be sized once we receive a WindowSizeMsg in Update
	m.viewport = viewport.Model{}
	p := tea.NewProgram(m, tea.WithAltScreen())
	return p, nil
}

// Init implements tea.Model
func (m model) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		k := msg.String()
		if k == "q" || k == "esc" {
			return m, tea.Quit
		}

		// Phase 0: user selection
		if m.phase == 0 {
			return m.updateUserSelection(k)
		}

		// Phase 2: settings panel
		if m.phase == 2 {
			return m.updateSettings(k)
		}

		// Commit log popup
		if m.showCommitLog {
			return m.updateCommitLog(k)
		}

		// Confirmation dialog overlay
		if m.confirmCommit {
			return m.updateConfirmation(k)
		}

		// Phase 1: approval view (existing logic)
		// toggle focus between rows
		if k == "tab" {
			m.focusRow = (m.focusRow + 1) % 2
			return m, nil
		}

		// horizontal scroll for changes with alt+a / alt+d
		if k == "alt+a" {
			if m.changeHOffset > 0 {
				m.changeHOffset--
			}
			return m, nil
		}
		if k == "alt+d" {
			_, midWidth, _, _ := m.columnWidths()
			contentW := max(midWidth-4, 10)
			maxLen := 0
			for _, cl := range m.changesForFileTab() {
				maxLen = max(maxLen, len(cl))
			}
			if maxOff := maxLen - contentW; m.changeHOffset < maxOff {
				m.changeHOffset++
			}
			return m, nil
		}

		// column navigation (works in both rows)
		if k == "a" { // left
			if m.col > 0 {
				m.col--
			}
			m.focusRow = 0
			return m, nil
		}
		if k == "d" { // right
			if m.col < 3 {
				m.col++
			}
			m.focusRow = 0
			return m, nil
		}

		// If bottom row is focused, w/s should scroll the PR body viewport
		if m.focusRow == 1 {
			if k == "w" {
				m.viewport.LineUp(1)
				return m, nil
			}
			if k == "s" {
				m.viewport.LineDown(1)
				return m, nil
			}
			if k == "pgup" {
				m.viewport.PageUp()
				return m, nil
			}
			if k == "pgdown" {
				m.viewport.PageDown()
				return m, nil
			}
		}

		// when top row is focused, w/s navigate hashes
		if m.focusRow == 0 {
			// behavior depends on which top column is active:
			// - col 0 (hashes): w/s move the selection
			// - col 1 (changes): w/s scroll the changes pane
			// - col 2 (PRs): w/s scroll the PR pane
			// - col 3 (staged): w/s scroll the staged pane
			if m.col == 0 {
				if k == "w" { // up (selection)
					if m.hashIndex > 0 {
						m.hashIndex--
						// update viewport content when selection changes
						m.updateViewportContent()
						// ensure hashOffset keeps selection visible
						visible := m.topVisibleLines()
						ensureOffset(&m.hashOffset, m.hashIndex, visible)
					}
					return m, nil
				}
				if k == "s" { // down (selection)
					if m.hashIndex < len(m.hashes)-1 {
						m.hashIndex++
						m.updateViewportContent()
						visible := m.topVisibleLines()
						ensureOffset(&m.hashOffset, m.hashIndex, visible)
					}
					return m, nil
				}
			} else if m.col == 1 {
				// file tab navigation
				if k == "e" {
					if m.changeFileTab > 0 {
						m.changeFileTab--
						m.changeOffset = 0
						m.changeHOffset = 0
					}
					return m, nil
				}
				if k == "r" {
					files := m.changeFilesForHash()
					if m.changeFileTab < len(files)-1 {
						m.changeFileTab++
						m.changeOffset = 0
						m.changeHOffset = 0
					}
					return m, nil
				}
				// changes pane scroll
				if k == "w" {
					if m.changeOffset > 0 {
						m.changeOffset--
					}
					return m, nil
				}
				if k == "s" {
					// bound by number of change lines for active file tab
					changes := m.changesForFileTab()
					if len(changes) > 0 {
						visible := m.topVisibleLines() - 2 // minus title and tab bar
						if visible < 1 {
							visible = 1
						}
						maxOff := len(changes) - visible
						if maxOff < 0 {
							maxOff = 0
						}
						if m.changeOffset < maxOff {
							m.changeOffset++
						}
					}
					return m, nil
				}
			} else if m.col == 2 {
				// PRs pane scroll
				if k == "w" {
					if m.prOffset > 0 {
						m.prOffset--
					}
					return m, nil
				}
				if k == "s" {
					sel := ""
					if len(m.hashes) > 0 && m.hashIndex < len(m.hashes) {
						sel = m.hashes[m.hashIndex]
					}
					if sel != "" {
						if prs, ok := m.hashPrMap[sel]; ok {
							visible := m.topVisibleLines() - 1
							if visible < 1 {
								visible = 1
							}
							maxOff := len(prs) - visible
							if maxOff < 0 {
								maxOff = 0
							}
							if m.prOffset < maxOff {
								m.prOffset++
							}
						}
					}
					return m, nil
				}
			} else if m.col == 3 {
				// staged pane scroll
				if k == "w" {
					if m.stagedOffset > 0 {
						m.stagedOffset--
					}
					return m, nil
				}
				if k == "s" {
					sel := ""
					if len(m.hashes) > 0 && m.hashIndex < len(m.hashes) {
						sel = m.hashes[m.hashIndex]
					}
					if sel != "" {
						// staged pane scroll should be based on staged PRs (not hashes)
						stagedKeys := m.stagedPrKeys()
						visible := m.topVisibleLines() - 1
						if visible < 1 {
							visible = 1
						}
						maxOff := len(stagedKeys) - visible
						if maxOff < 0 {
							maxOff = 0
						}
						if m.stagedOffset < maxOff {
							m.stagedOffset++
						}
					}
					return m, nil
				}
			}
			if k == "x" { // approve
				if m.hashIndex >= 0 && m.hashIndex < len(m.hashes) {
					h := m.hashes[m.hashIndex]
					// mark approved and remove any declined marker for this hash
					delete(m.declined, h)
					m.approved[h] = true
					if m.propagate {
						// auto-approve linked hashes (quiet)
						approve.ApproveLinkedHashes(h, m.approved, m.declined, m.hashPrMap, m.prMap, true)
					}
					m.status = fmt.Sprintf("approved %s", h[:6])
					// ensure UI reflects the change immediately
					// reconcile any PRs that were skipped earlier and may now be eligible
					m.reconcilePrSkipped()
					m.updateStagedList()
					m.updateViewportContent()
				}
				return m, nil
			}
			if k == "f" { // decline selected hash with 'f'
				if m.hashIndex >= 0 && m.hashIndex < len(m.hashes) {
					h := m.hashes[m.hashIndex]
					// mark declined and remove any approved marker for this hash
					delete(m.approved, h)
					m.declined[h] = true
					// auto-decline linked hashes quietly and mark PRs skipped
					approve.DeclineLinkedHashes(h, m.declined, m.prSkipped, m.hashPrMap, m.prMap, true)
					// remove any hashes that got marked declined from approved map to keep state consistent
					for dh := range m.declined {
						if m.approved[dh] {
							delete(m.approved, dh)
						}
					}
					// update staged PR list and UI
					// reconcile skipped PRs in case some were unskipped by downstream effects
					m.reconcilePrSkipped()
					m.updateStagedList()
					m.status = fmt.Sprintf("declined %s", h[:6])
					m.updateViewportContent()
				}
				return m, nil
			}
			if k == "p" { // open settings panel
				m.phase = 2
				m.settingsField = 0
				m.loadCurrentSettingsField()
				return m, nil
			}
			if k == "c" { // commit changes — show confirmation dialog
				filtered := m.buildFilteredPrMap()
				if len(filtered) > 0 {
					m.confirmCommit = true
				} else {
					m.status = "no staged PRs to commit"
				}
				return m, nil
			}
		}

	case tea.WindowSizeMsg:
		// store terminal size, compute layout widths and initialize viewport
		m.termWidth = msg.Width
		m.termHeight = msg.Height

		// Reserve header/footer heights so the total rendered rows fit the terminal.
		// header (status line) = 1, footer (hint) = 1
		headerH := 1
		footerH := 1
		reserved := headerH + footerH

		// bottom outer height (including border/padding) should be roughly 1/3 of terminal but leave room for reserved lines
		bottomOuter := (msg.Height - reserved) / 3
		if bottomOuter < 6 {
			bottomOuter = 6
		}
		// top outer gets the rest
		topOuter := msg.Height - reserved - bottomOuter
		if topOuter < 3 {
			topOuter = 3
			// adjust bottomOuter if needed
			if msg.Height-reserved-topOuter >= 3 {
				bottomOuter = msg.Height - reserved - topOuter
			}
		}

		m.bottomHeight = bottomOuter
		m.topHeight = topOuter

		// viewport inner size must account for border and padding. We use padding=1 and border=1 on top/bottom,
		// so inner height = bottomOuter - borderTop - borderBottom - padTop - padBottom = bottomOuter - 4
		vpH := bottomOuter - 4
		if vpH < 1 {
			vpH = 1
		}
		// viewport width similarly accounts for left/right borders/padding; subtract 4
		vpW := msg.Width - 4
		if vpW < 10 {
			vpW = 10
		}

		m.viewport = viewport.New(vpW, vpH)
		m.updateViewportContent()
		// ensure viewport shows from the top after resize
		m.viewport.GotoTop()
		return m, nil
	}

	return m, nil
}

// updateViewportContent updates the viewport with the PR body of the currently selected PR (first PR for selected hash)
func (m *model) updateViewportContent() {
	selectedHash := m.selectedHash()
	body := ""
	if selectedHash != "" {
		if prs, ok := m.hashPrMap[selectedHash]; ok && len(prs) > 0 {
			if b, err := m.client.GetPrComment(prs[0]); err == nil {
				body = b
			} else {
				body = "(no body)"
			}
		} else {
			body = "(no PR)"
		}
	}
	m.viewport.SetContent(body)
	// reset viewport scroll to top so the beginning of the PR body is visible
	m.viewport.GotoTop()
	// reset top-column offsets for the newly selected hash so related panes start at top
	m.changeOffset = 0
	m.prOffset = 0
	m.stagedOffset = 0
	// auto-select the file tab matching this hash's file
	m.updateChangeFileTab()
	// refresh cached staged PRs whenever the visible selection or approvals change
	m.updateStagedList()
}

// updateStagedList recomputes and stores the list of PR keys that would be approved
func (m *model) updateStagedList() {
	m.stagedPRList = m.stagedPrKeys()
}

// View implements tea.Model
func (m model) View() string {
	if m.phase == 0 {
		return m.viewUserSelection()
	}
	if m.phase == 2 {
		return m.viewSettings()
	}
	if m.showCommitLog {
		return m.viewCommitLog()
	}
	// If confirmation dialog is showing, render it as an overlay
	if m.confirmCommit {
		return m.viewConfirmation()
	}

	leftWidth, midWidth, prWidth, stagedWidth := m.columnWidths()

	// apply the top row height to all three columns so the top region occupies the same vertical space
	// subtract estimated extra space used by borders and padding so the rendered blocks fit the terminal
	topOuter := m.topHeight
	if topOuter == 0 {
		topOuter = m.termHeight - m.bottomHeight - 2
		if topOuter < 3 {
			topOuter = 3
		}
	}
	// subtract 2 lines to account for border/padding differences (conservative)
	topBlockH := topOuter - 2
	if topBlockH < 1 {
		topBlockH = 1
	}
	yellowBorder := lipgloss.NewStyle().BorderForeground(lipgloss.Color("11"))
	baseCol := func(w int) lipgloss.Style {
		return lipgloss.NewStyle().Width(w).Height(topBlockH).MaxHeight(topBlockH + 2).Border(lipgloss.NormalBorder()).PaddingLeft(1).PaddingRight(1)
	}
	left := baseCol(leftWidth)
	mid := baseCol(midWidth)
	prStyle := baseCol(prWidth)
	stagedStyle := baseCol(stagedWidth)
	if m.focusRow == 0 {
		switch m.col {
		case 0:
			left = left.Inherit(yellowBorder)
		case 1:
			mid = mid.Inherit(yellowBorder)
		case 2:
			prStyle = prStyle.Inherit(yellowBorder)
		case 3:
			stagedStyle = stagedStyle.Inherit(yellowBorder)
		}
	}

	// build column title row
	titleStyle := lipgloss.NewStyle().Bold(true)
	leftTitle := titleStyle.Render("Hashes")
	midTitle := titleStyle.Render("Changes")
	rightTitle := titleStyle.Render("Related PRs")
	stagedTitle := titleStyle.Render("Staged changes")

	selectedHash := m.selectedHash()

	// left column: show hashes (6 chars)
	var leftLines []string
	leftLines = append(leftLines, leftTitle)
	// prepare full list of hash lines (without selection background)
	fullLeft := []string{}
	for _, h := range m.hashes {
		short := h
		if len(h) > 6 {
			short = h[:6]
		}
		marker := " "
		if m.approved[h] {
			marker = "✓"
		} else if m.declined[h] {
			marker = "x"
		}
		line := fmt.Sprintf("%s %s", marker, short)
		if m.approved[h] {
			line = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render(line)
		} else if m.declined[h] {
			line = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render(line)
		}
		fullLeft = append(fullLeft, line)
	}

	visible := m.topVisibleLines()
	// clamp offsets
	if m.hashOffset < 0 {
		m.hashOffset = 0
	}
	if m.hashOffset > len(fullLeft)-visible {
		m.hashOffset = len(fullLeft) - visible
	}
	if m.hashOffset < 0 {
		m.hashOffset = 0
	}

	window := sliceForWindow(fullLeft, m.hashOffset, visible)
	// append windowed lines and apply selection highlight if selection is within window
	for i, line := range window {
		origIndex := m.hashOffset + i
		if origIndex == m.hashIndex && m.col == 0 && m.focusRow == 0 {
			leftLines = append(leftLines, lipgloss.NewStyle().Background(lipgloss.Color("62")).Render(line))
		} else if m.col == 0 && m.focusRow == 0 {
			// column focused -> render with slight background to indicate focus
			leftLines = append(leftLines, lipgloss.NewStyle().Render(line))
		} else {
			leftLines = append(leftLines, line)
		}
	}

	// middle column: show changes for selected hash (with file tabs)
	var midLines []string
	midLines = append(midLines, midTitle)
	// render file tab bar
	tabBar := m.renderFileTabs(midWidth)
	if tabBar != "" {
		midLines = append(midLines, tabBar)
	}
	if selectedHash != "" {
		fullChanges := m.changesForFileTab()
		if len(fullChanges) > 0 {
			// subtract title + tab bar from visible lines
			headerLines := 1 // title
			if tabBar != "" {
				headerLines = 2 // title + tab bar
			}
			visible := m.topVisibleLines() - headerLines
			if visible < 1 {
				visible = 1
			}
			// clamp changeOffset
			if m.changeOffset < 0 {
				m.changeOffset = 0
			}
			if m.changeOffset > len(fullChanges)-visible {
				m.changeOffset = len(fullChanges) - visible
			}
			if m.changeOffset < 0 {
				m.changeOffset = 0
			}
			win := sliceForWindow(fullChanges, m.changeOffset, visible)
			// compute content width for changes (reserve padding inside mid box)
			contentW := midWidth - 4
			if contentW < 10 {
				contentW = 10
			}
			for _, cl := range win {
				display := cl
				// apply horizontal offset: show substring of the line
				r := []rune(display)
				start := m.changeHOffset
				if start < 0 {
					start = 0
				}
				if start >= len(r) {
					display = ""
				} else {
					end := start + contentW
					if end > len(r) {
						end = len(r)
					}
					display = string(r[start:end])
				}
				// add left/right indicators if truncated
				if m.changeHOffset > 0 {
					display = "«" + display
				}
				if len([]rune(cl)) > m.changeHOffset+contentW {
					display = display + "»"
				}
				// apply coloring based on line prefix
				if strings.HasPrefix(cl, "+") {
					display = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render(display)
				} else if strings.HasPrefix(cl, "-") {
					display = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render(display)
				} else if cl == "..." {
					display = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(display)
				}
				midLines = append(midLines, display)
			}
		} else {
			midLines = append(midLines, "(no changes)")
		}
	}

	// PRs column: Related PRs for selected hash
	var prLines []string
	prLines = append(prLines, rightTitle)
	var fullPRs []string
	if selectedHash != "" {
		if prs, ok := m.hashPrMap[selectedHash]; ok {
			for i, pr := range prs {
				prKey := pr.GetHTMLURL()
				fullPRs = append(fullPRs, m.renderPRLabel(prKey, i))
				// show linked hashes for this PR
				if linkedHashes, ok := m.prMap[prKey]; ok {
					for j, lh := range linkedHashes {
						short := lh
						if len(lh) > 6 {
							short = lh[:6]
						}
						connector := "├─"
						if j == len(linkedHashes)-1 {
							connector = "└─"
						}
						marker := "✓"
						color := lipgloss.Color("10")
						if m.declined[lh] {
							marker = "×"
							color = lipgloss.Color("9")
						} else if !m.approved[lh] {
							marker = "?"
							color = lipgloss.Color("8")
						}
						hashLine := lipgloss.NewStyle().Foreground(color).Render(
							fmt.Sprintf("  %s %s %s", connector, short, marker))
						fullPRs = append(fullPRs, hashLine)
					}
				}
			}
			visible := m.topVisibleLines()
			// clamp prOffset
			if m.prOffset < 0 {
				m.prOffset = 0
			}
			if m.prOffset > len(fullPRs)-visible {
				m.prOffset = len(fullPRs) - visible
			}
			if m.prOffset < 0 {
				m.prOffset = 0
			}
			win := sliceForWindow(fullPRs, m.prOffset, visible)
			for _, line := range win {
				if m.col == 2 && m.focusRow == 0 {
					prLines = append(prLines, lipgloss.NewStyle().Background(lipgloss.Color("62")).Render(line))
				} else {
					prLines = append(prLines, line)
				}
			}
		} else {
			prLines = append(prLines, "(no PRs)")
		}
	}

	// build staged PR list: PRs that would be approved (not skipped, not all-declined, all non-declined hashes approved)
	// Ensure it's sorted and non-nil
	// compute staged PRs fresh so the staged column always reflects current state
	stagedPRs := m.stagedPrKeys()

	var stagedLines []string
	stagedLines = append(stagedLines, stagedTitle)
	if len(stagedPRs) == 0 {
		stagedLines = append(stagedLines, "(no staged PRs)")
	} else {
		for i, prKey := range stagedPRs {
			stagedLines = append(stagedLines, m.renderPRLabel(prKey, i))
		}
	}

	// make staged column scrollable: clamp stagedOffset and window the lines
	visible = m.topVisibleLines()
	if m.stagedOffset < 0 {
		m.stagedOffset = 0
	}
	if m.stagedOffset > len(stagedLines)-visible {
		m.stagedOffset = len(stagedLines) - visible
	}
	if m.stagedOffset < 0 {
		m.stagedOffset = 0
	}
	stagedWindow := sliceForWindow(stagedLines, m.stagedOffset, visible)
	if stagedWindow == nil {
		stagedWindow = []string{stagedLines[0]}
	}

	// Add scrollbars to each column.
	// The hash column is too narrow for an inline scrollbar — skip it.
	// For the other columns each line is already bounded to (columnWidth-4) visible
	// chars by the horizontal scroll / change-width logic, so appending " █" (2 chars)
	// fits exactly within the (columnWidth-2) content area.
	visible = m.topVisibleLines()

	// changes scrollbar (based on content excluding title+tabbar)
	changesTotal := 0
	if selectedHash != "" {
		changesTotal = len(m.changesForFileTab())
	}
	headerCount := 1
	if tabBar != "" {
		headerCount = 2
	}
	changeScrollVisible := max(visible-headerCount, 1)
	changeScrollbar := renderScrollbar(changeScrollVisible, changesTotal, m.changeOffset)
	if len(midLines) > headerCount {
		contentLines := midLines[headerCount:]
		withScroll := appendScrollbar(contentLines, changeScrollbar, midWidth-2)
		midLines = append(midLines[:headerCount], withScroll...)
	}

	// PR scrollbar
	prScrollbar := renderScrollbar(len(prLines)-1, len(fullPRs), m.prOffset)
	prWithScroll := appendScrollbar(prLines[1:], prScrollbar, prWidth-2)
	prLines = append(prLines[:1], prWithScroll...)

	// staged scrollbar
	stagedScrollbar := renderScrollbar(len(stagedWindow), len(stagedLines), m.stagedOffset)
	stagedWindow = appendScrollbar(stagedWindow, stagedScrollbar, stagedWidth-2)

	leftBox := left.Render(strings.Join(leftLines, "\n"))
	midBox := mid.Render(strings.Join(midLines, "\n"))
	prBox := prStyle.Render(strings.Join(prLines, "\n"))
	stagedBox := stagedStyle.Render(strings.Join(stagedWindow, "\n"))

	top := lipgloss.JoinHorizontal(lipgloss.Top, leftBox, midBox, prBox, stagedBox)

	// bottom: show the viewport content (scrollable PR body). When focused, visually indicate focus.
	// Use the stored bottomHeight (including borders) for consistent sizing
	bottomOuter := m.bottomHeight
	if bottomOuter == 0 {
		bottomOuter = 10
	}
	// reduce bottom outer by 2 to allow for border/padding conservative fit
	bottomBlockH := bottomOuter - 2
	if bottomBlockH < 3 {
		bottomBlockH = 3
	}
	bottomStyle := lipgloss.NewStyle().Height(bottomBlockH).Border(lipgloss.NormalBorder()).Padding(1)
	if m.focusRow == 1 {
		bottomStyle = bottomStyle.Inherit(yellowBorder)
	}
	bodyView := m.viewport.View()
	if bodyView == "" {
		bodyView = "(no PR body)"
	}

	// footer with keybind hints (bottom-left)
	hint := "tab: switch row • a/d: left/right • w/s: up/down • e/r: file tabs • x: approve • f: decline • c: commit • p: settings • q: quit • alt+a/d: hscroll"
	footer := lipgloss.NewStyle().Padding(0, 1).Render(hint)

	bottom := bottomStyle.Render(bodyView)

	// join everything with footer below; no extra spacer lines so the layout fits the terminal exactly
	return lipgloss.JoinVertical(lipgloss.Left,
		fmt.Sprintf("Column: %d | Selected hash: %s | Status: %s", m.col+1, func() string {
			if selectedHash == "" {
				return "-"
			} else {
				return selectedHash[:6]
			}
		}(), m.status),
		top,
		bottom,
		footer,
	)
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

// ensureOffset ensures the given offset keeps the given index visible within the top-visible range.
func ensureOffset(offset *int, index int, visible int) {
	if index < *offset {
		// index is above visible range, move offset up
		*offset = index
	}
	if index >= *offset+visible {
		// index is below visible range, move offset down
		*offset = index - visible + 1
	}
}

// topVisibleLines computes the number of lines visible in the top columns based on the current terminal size and allocated top height.
func (m model) topVisibleLines() int {
	// Compute visible lines in the top columns (excluding the title line).
	// Derive topOuter similar to View: outer allocated, then conservative subtraction applied there.
	topOuter := m.topHeight
	if topOuter == 0 {
		topOuter = m.termHeight - m.bottomHeight - 2
		if topOuter < 3 {
			topOuter = 3
		}
	}
	// topBlockH is what View sets as Height for the blocks after conservative subtraction
	topBlockH := topOuter - 2
	if topBlockH < 1 {
		topBlockH = 1
	}
	// one line is used for the column title; the rest are usable lines
	visible := topBlockH - 1
	if visible < 1 {
		visible = 1
	}
	return visible
}

// helper to get a slice of strings from items starting at offset with length up to visible
func sliceForWindow(items []string, offset, visible int) []string {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(items) {
		return nil
	}
	end := offset + visible
	if end > len(items) {
		end = len(items)
	}
	return items[offset:end]
}

// isPRFullyApproved returns true if all hashes for the PR are approved (none declined).
func (m *model) isPRFullyApproved(phashes []string) bool {
	if len(m.approved) == 0 {
		return false
	}
	for _, ph := range phashes {
		if m.declined[ph] || !m.approved[ph] {
			return false
		}
	}
	return true
}

func (m *model) stagedPrKeys() []string {
	var stagedPRs []string
	for prKey, phashes := range m.prMap {
		if m.isPRFullyApproved(phashes) {
			if m.prSkipped[prKey] {
				delete(m.prSkipped, prKey)
			}
			stagedPRs = append(stagedPRs, prKey)
		}
	}
	sort.Strings(stagedPRs)
	return stagedPRs
}

// prApprovalState returns (allApproved, anyDeclined, committed) for a PR.
func (m *model) prApprovalState(prKey string) (bool, bool, bool) {
	hashes, ok := m.prMap[prKey]
	if !ok {
		return false, false, false
	}
	anyDeclined := false
	allApproved := true
	allCommitted := true
	for _, h := range hashes {
		if m.declined[h] {
			anyDeclined = true
			allApproved = false
		}
		if !m.approved[h] {
			allApproved = false
		}
		if !m.committed[h] {
			allCommitted = false
		}
	}
	return allApproved, anyDeclined, allCommitted
}

func (m *model) renderPRLabel(prKey string, idx int) string {
	verifiedIcon := approve.VerifiedIcon(m.verifiedMap[prKey])
	label := fmt.Sprintf("[%d] %s %s", idx+1, verifiedIcon, prKey)
	allApproved, anyDeclined, committed := m.prApprovalState(prKey)
	if committed {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Render(label)
	}
	if anyDeclined {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render(label)
	}
	if allApproved {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render(label)
	}
	return label
}

func (m *model) reconcilePrSkipped() {
	for prKey, phashes := range m.prMap {
		if m.prSkipped[prKey] && m.isPRFullyApproved(phashes) {
			delete(m.prSkipped, prKey)
		}
	}
}

// selectedHash returns the currently selected hash or empty string.
func (m model) selectedHash() string {
	if len(m.hashes) > 0 && m.hashIndex < len(m.hashes) {
		return m.hashes[m.hashIndex]
	}
	return ""
}

// changeFilesForHash returns the unique file paths where the selected hash's
// change is applied, across all PRs that contain it. Each entry is formatted
// as "repo#num:file" so you can see which PR/repo each file belongs to.
func (m model) changeFilesForHash() []string {
	sel := m.selectedHash()
	if sel == "" || len(m.hashFileMap) == 0 {
		return nil
	}
	prFiles, ok := m.hashFileMap[sel]
	if !ok || len(prFiles) == 0 {
		return nil
	}
	var files []string
	for prURL, file := range prFiles {
		// Format as "file (repo#num)" for context
		label := fmt.Sprintf("%s (%s)", file, shortenPRURL(prURL))
		files = append(files, label)
	}
	sort.Strings(files)
	return files
}

// shortenPRURL turns "https://github.com/owner/repo/pull/123" into "owner/repo#123".
func shortenPRURL(url string) string {
	// Expected format: https://github.com/{owner}/{repo}/pull/{number}
	parts := strings.Split(url, "/")
	if len(parts) >= 7 {
		return fmt.Sprintf("%s/%s#%s", parts[3], parts[4], parts[6])
	}
	return url
}

// changesForFileTab returns the change lines for the selected hash.
// When contextLines > 0, it uses the raw change map (with context lines)
// and filters to show only N context lines around changes.
func (m model) changesForFileTab() []string {
	sel := m.selectedHash()
	if sel == "" {
		return nil
	}
	if m.settings.contextLines >= 0 {
		if raw, ok := m.rawChangeMap[sel]; ok && len(raw) > 0 {
			return filterContextLines(raw, m.settings.contextLines)
		}
	}
	return m.changeMap[sel]
}

// renderFileTabs renders the file tab bar for the changes panel.
// If all tabs fit within the available width, render them normally.
// Otherwise, show a compact virtual tab: "◄ [2/5] filename ►"
func (m model) renderFileTabs(midWidth int) string {
	files := m.changeFilesForHash()
	if len(files) == 0 {
		return ""
	}
	tabIdx := m.changeFileTab
	if tabIdx < 0 || tabIdx >= len(files) {
		tabIdx = 0
	}
	contentW := midWidth - 4
	if contentW < 10 {
		contentW = 10
	}

	// Try rendering all tabs inline
	var parts []string
	for i, f := range files {
		if i == tabIdx {
			parts = append(parts, lipgloss.NewStyle().Bold(true).Background(lipgloss.Color("62")).Render(" "+f+" "))
		} else {
			parts = append(parts, " "+f+" ")
		}
	}
	line := strings.Join(parts, "│")

	// Check if the plain text (without ANSI) fits
	plainLen := 0
	for i, f := range files {
		plainLen += len(f) + 2
		if i > 0 {
			plainLen++
		}
	}

	if plainLen <= contentW {
		return lipgloss.NewStyle().MaxWidth(contentW).Render(line)
	}

	// Virtual tab mode: show ◄ [idx/total] shortened_name ►
	current := files[tabIdx]
	// shorten the filename if needed
	prefix := fmt.Sprintf("◄ [%d/%d] ", tabIdx+1, len(files))
	suffix := " ►"
	maxNameLen := contentW - len(prefix) - len(suffix)
	if maxNameLen < 3 {
		maxNameLen = 3
	}
	name := current
	if len(name) > maxNameLen {
		name = "…" + name[len(name)-maxNameLen+1:]
	}
	tabLine := prefix + lipgloss.NewStyle().Bold(true).Render(name) + suffix
	return tabLine
}

// updateChangeFileTab resets the file tab to the first entry when the hash changes.
func (m *model) updateChangeFileTab() {
	m.changeFileTab = 0
}

// columnWidths computes the 4-column layout widths (left, mid, pr, staged).
func (m model) columnWidths() (int, int, int, int) {
	termW := m.termWidth
	if termW == 0 {
		termW = 120
	}

	leftMax := 0
	for _, h := range m.hashes {
		short := h
		if len(h) > 6 {
			short = h[:6]
		}
		leftMax = max(leftMax, len(short))
	}
	leftWidth := max(leftMax+4, 8)

	maxPR := 0
	for _, prs := range m.hashPrMap {
		for _, pr := range prs {
			maxPR = max(maxPR, len(pr.GetHTMLURL()))
		}
	}
	prWidth := max(maxPR+6, 20)
	stagedWidth := min(36, termW/4)
	prWidth = min(prWidth, termW/3)

	midWidth := termW - leftWidth - prWidth - stagedWidth - 6
	if midWidth < 10 {
		extra := 10 - midWidth
		if prWidth-extra > 10 {
			prWidth -= extra
		} else {
			prWidth = 10
		}
		midWidth = termW - leftWidth - prWidth - stagedWidth - 6
		if midWidth < 10 {
			midWidth = 10
			stagedWidth = min(stagedWidth, 20)
		}
	}
	return leftWidth, midWidth, prWidth, stagedWidth
}

// --- Phase 0: User selection ---

// updateUserSelection handles key input during the user selection phase.
func (m model) updateUserSelection(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "w", "up":
		if m.userCursor > 0 {
			m.userCursor--
			visible := m.userSelectionVisibleLines()
			ensureOffset(&m.userScrollOffset, m.userCursor, visible)
		}
	case "s", "down":
		if m.userCursor < len(m.availableUsers)-1 {
			m.userCursor++
			visible := m.userSelectionVisibleLines()
			ensureOffset(&m.userScrollOffset, m.userCursor, visible)
		}
	case " ", "x":
		if m.userCursor >= 0 && m.userCursor < len(m.availableUsers) {
			u := m.availableUsers[m.userCursor]
			m.userSelected[u] = !m.userSelected[u]
		}
	case "enter":
		// collect selected users
		var selected []string
		for _, u := range m.availableUsers {
			if m.userSelected[u] {
				selected = append(selected, u)
			}
		}
		if len(selected) == 0 {
			return m, nil
		}
		// filter hashes for selected users
		joined := strings.Join(selected, ",")
		m.hashes = approve.CollectHashesForUsers(joined, m.userHashPrMap)
		m.phase = 1
		m.updateStagedList()
		m.updateViewportContent()
	}
	return m, nil
}

// userSelectionVisibleLines returns the number of user lines visible in the selection panel.
func (m model) userSelectionVisibleLines() int {
	// Use most of the terminal height minus header/footer/borders
	h := m.termHeight - 8
	if h < 1 {
		h = 1
	}
	return h
}

// userPRCount returns the number of unique PRs for a given user.
func (m model) userPRCount(user string) int {
	hashMap, ok := m.userHashPrMap[user]
	if !ok {
		return 0
	}
	seen := map[string]struct{}{}
	for _, prs := range hashMap {
		for _, pr := range prs {
			seen[pr.GetHTMLURL()] = struct{}{}
		}
	}
	return len(seen)
}

// viewUserSelection renders the user selection panel.
func (m model) viewUserSelection() string {
	titleStyle := lipgloss.NewStyle().Bold(true)
	title := titleStyle.Render("Select users to review (space/x: toggle, enter: confirm, q: quit)")

	visible := m.userSelectionVisibleLines()

	var lines []string
	for i, u := range m.availableUsers {
		check := "[ ]"
		if m.userSelected[u] {
			check = "[x]"
		}
		prCount := m.userPRCount(u)
		label := fmt.Sprintf("%s %s (%d PRs)", check, u, prCount)
		if i == m.userCursor {
			label = lipgloss.NewStyle().Background(lipgloss.Color("62")).Render(label)
		}
		lines = append(lines, label)
	}

	// apply scrolling
	offset := m.userScrollOffset
	if offset < 0 {
		offset = 0
	}
	if offset > len(lines)-visible {
		offset = len(lines) - visible
	}
	if offset < 0 {
		offset = 0
	}
	window := sliceForWindow(lines, offset, visible)

	panelWidth := m.termWidth - 4
	if panelWidth < 30 {
		panelWidth = 30
	}
	panelHeight := visible + 2
	panel := lipgloss.NewStyle().
		Width(panelWidth).
		Height(panelHeight).
		Border(lipgloss.NormalBorder()).
		Padding(1).
		Render(strings.Join(window, "\n"))

	return lipgloss.JoinVertical(lipgloss.Left, title, panel)
}

// --- Settings panel (phase 2) ---

// updateSettings handles key input during the settings phase.
func (m model) updateSettings(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "tab":
		m.saveCurrentSettingsField()
		m.settingsField = (m.settingsField + 1) % 2
		m.loadCurrentSettingsField()
	case "enter":
		m.saveCurrentSettingsField()
		m.phase = 1
		// reset scroll offsets so the new settings (e.g. context lines) apply cleanly
		m.changeOffset = 0
		m.changeHOffset = 0
	case "backspace":
		if m.settingsCursor > 0 {
			r := []rune(m.settingsEdit)
			m.settingsEdit = string(r[:m.settingsCursor-1]) + string(r[m.settingsCursor:])
			m.settingsCursor--
		}
	case "left":
		if m.settingsCursor > 0 {
			m.settingsCursor--
		}
	case "right":
		if m.settingsCursor < len([]rune(m.settingsEdit)) {
			m.settingsCursor++
		}
	default:
		// only accept printable characters (single rune)
		if len(k) == 1 || (len(k) > 1 && !strings.HasPrefix(k, "alt+") && !strings.HasPrefix(k, "ctrl+")) {
			r := []rune(m.settingsEdit)
			newR := []rune(k)
			result := make([]rune, 0, len(r)+len(newR))
			result = append(result, r[:m.settingsCursor]...)
			result = append(result, newR...)
			result = append(result, r[m.settingsCursor:]...)
			m.settingsEdit = string(result)
			m.settingsCursor += len(newR)
		}
	}
	return m, nil
}

// loadCurrentSettingsField loads the current settings field value into the edit buffer.
func (m *model) loadCurrentSettingsField() {
	switch m.settingsField {
	case 0:
		m.settingsEdit = m.settings.reviewComment
	case 1:
		m.settingsEdit = fmt.Sprintf("%d", m.settings.contextLines)
	}
	m.settingsCursor = len([]rune(m.settingsEdit))
}

// saveCurrentSettingsField saves the edit buffer back to the appropriate settings field.
func (m *model) saveCurrentSettingsField() {
	switch m.settingsField {
	case 0:
		m.settings.reviewComment = m.settingsEdit
	case 1:
		n := 0
		for _, ch := range m.settingsEdit {
			if ch >= '0' && ch <= '9' {
				n = n*10 + int(ch-'0')
			}
		}
		m.settings.contextLines = n
	}
}

// viewSettings renders the settings panel.
func (m model) viewSettings() string {
	titleStyle := lipgloss.NewStyle().Bold(true)
	title := titleStyle.Render("Settings (tab: switch field, enter: save & close, q: quit)")

	fields := []struct {
		label string
		value string
	}{
		{"Review comment", m.settings.reviewComment},
		{"Context lines", fmt.Sprintf("%d", m.settings.contextLines)},
	}

	var lines []string
	for i, f := range fields {
		display := f.value
		if i == m.settingsField {
			// show the edit buffer with cursor
			r := []rune(m.settingsEdit)
			before := string(r[:m.settingsCursor])
			after := ""
			cursor := "█"
			if m.settingsCursor < len(r) {
				after = string(r[m.settingsCursor:])
			}
			display = before + cursor + after
		}
		label := fmt.Sprintf("  %s: %s", f.label, display)
		if i == m.settingsField {
			label = lipgloss.NewStyle().Background(lipgloss.Color("62")).Render(label)
		}
		lines = append(lines, label)
	}

	panelWidth := m.termWidth - 4
	if panelWidth < 40 {
		panelWidth = 40
	}
	panel := lipgloss.NewStyle().
		Width(panelWidth).
		Height(len(lines) + 4).
		Border(lipgloss.NormalBorder()).
		Padding(1).
		Render(strings.Join(lines, "\n"))

	return lipgloss.JoinVertical(lipgloss.Left, title, panel)
}

// --- Confirmation dialog ---

// buildFilteredPrMap returns a map of PRs that are eligible for approval.
func (m *model) buildFilteredPrMap() map[string][]string {
	filtered := make(map[string][]string)
	for prKey, phashes := range m.prMap {
		if m.prSkipped[prKey] {
			continue
		}
		allDeclined := true
		for _, ph := range phashes {
			if !m.declined[ph] {
				allDeclined = false
				break
			}
		}
		if allDeclined {
			continue
		}
		allApproved := true
		for _, ph := range phashes {
			if m.declined[ph] {
				allApproved = false
				break
			}
			if len(m.approved) > 0 {
				if !m.approved[ph] {
					allApproved = false
					break
				}
			} else {
				allApproved = false
				break
			}
		}
		if allApproved {
			filtered[prKey] = phashes
		}
	}
	return filtered
}

// updateConfirmation handles key input during the confirmation dialog.
func (m model) updateConfirmation(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "y":
		filtered := m.buildFilteredPrMap()
		var logs []string
		if len(filtered) > 0 {
			logs = approve.ProcessApprovals(filtered, m.approved, m.declined, m.prSkipped, m.hashPrMap, m.client, m.dryRun, m.settings.reviewComment)
			for _, phashes := range filtered {
				for _, ph := range phashes {
					m.committed[ph] = true
				}
			}
			m.reconcilePrSkipped()
			m.updateStagedList()
		}
		m.confirmCommit = false
		m.status = "committed approvals"
		m.viewport.GotoTop()
		m.updateViewportContent()
		// show commit log popup if there's anything to show
		if len(logs) > 0 {
			m.commitLog = logs
			m.commitLogOffset = 0
			m.showCommitLog = true
		}
	case "n":
		m.confirmCommit = false
		m.status = "commit cancelled"
	}
	return m, nil
}

// --- Commit log popup ---

// updateCommitLog handles key input while the commit log popup is shown.
func (m model) updateCommitLog(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "enter", "q", "esc":
		m.showCommitLog = false
	case "w", "up":
		if m.commitLogOffset > 0 {
			m.commitLogOffset--
		}
	case "s", "down":
		visible := m.commitLogVisibleLines()
		maxOff := len(m.commitLog) - visible
		if maxOff < 0 {
			maxOff = 0
		}
		if m.commitLogOffset < maxOff {
			m.commitLogOffset++
		}
	case "pgup":
		visible := m.commitLogVisibleLines()
		m.commitLogOffset -= visible
		if m.commitLogOffset < 0 {
			m.commitLogOffset = 0
		}
	case "pgdown":
		visible := m.commitLogVisibleLines()
		maxOff := len(m.commitLog) - visible
		if maxOff < 0 {
			maxOff = 0
		}
		m.commitLogOffset += visible
		if m.commitLogOffset > maxOff {
			m.commitLogOffset = maxOff
		}
	}
	return m, nil
}

// commitLogVisibleLines returns how many log lines fit inside the popup content area.
func (m model) commitLogVisibleLines() int {
	// popup uses most of the terminal height; subtract border (2), padding (2), title (2)
	h := m.termHeight - 8
	if h < 1 {
		h = 1
	}
	return h
}

// viewCommitLog renders the scrollable commit log popup.
func (m model) viewCommitLog() string {
	visible := m.commitLogVisibleLines()

	// clamp offset
	offset := m.commitLogOffset
	maxOff := len(m.commitLog) - visible
	if maxOff < 0 {
		maxOff = 0
	}
	if offset > maxOff {
		offset = maxOff
	}
	if offset < 0 {
		offset = 0
	}

	window := sliceForWindow(m.commitLog, offset, visible)

	// scrollbar
	sb := renderScrollbar(visible, len(m.commitLog), offset)
	withScroll := appendScrollbar(window, sb, m.termWidth-10)

	titleStyle := lipgloss.NewStyle().Bold(true)
	title := titleStyle.Render(fmt.Sprintf("Commit log (%d/%d lines) — w/s: scroll • enter/q: close",
		min(offset+visible, len(m.commitLog)), len(m.commitLog)))

	panelWidth := m.termWidth - 4
	if panelWidth < 40 {
		panelWidth = 40
	}
	panelH := visible + 2
	panel := lipgloss.NewStyle().
		Width(panelWidth).
		Height(panelH).
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("10")).
		Padding(1).
		Render(strings.Join(withScroll, "\n"))

	return lipgloss.JoinVertical(lipgloss.Left, title, panel)
}

// viewConfirmation renders the confirmation dialog overlay.
func (m model) viewConfirmation() string {
	filtered := m.buildFilteredPrMap()
	var prKeys []string
	for k := range filtered {
		prKeys = append(prKeys, k)
	}
	sort.Strings(prKeys)

	var lines []string
	lines = append(lines, lipgloss.NewStyle().Bold(true).Render("Confirm approval of the following PRs?"))
	lines = append(lines, "")
	for _, prKey := range prKeys {
		lines = append(lines, fmt.Sprintf("  %s %s", approve.VerifiedIcon(m.verifiedMap[prKey]), shortenPRURL(prKey)))
	}
	lines = append(lines, "")
	if m.settings.reviewComment != "" {
		lines = append(lines, fmt.Sprintf("  Review comment: %s", m.settings.reviewComment))
		lines = append(lines, "")
	}
	lines = append(lines, lipgloss.NewStyle().Bold(true).Render("  Press 'y' to confirm, 'n' to cancel"))

	content := strings.Join(lines, "\n")

	dialogWidth := 60
	if m.termWidth-10 > dialogWidth {
		dialogWidth = min(m.termWidth-10, 100)
	}
	dialogStyle := lipgloss.NewStyle().
		Width(dialogWidth).
		Border(lipgloss.DoubleBorder()).
		BorderForeground(lipgloss.Color("11")).
		Padding(1, 2)

	dialog := dialogStyle.Render(content)
	return lipgloss.Place(m.termWidth, m.termHeight, lipgloss.Center, lipgloss.Center, dialog)
}

// --- Scrollbar rendering ---

// renderScrollbar generates a vertical scrollbar as a slice of strings (one per visible line).
func renderScrollbar(visibleLines, totalLines, offset int) []string {
	if totalLines <= visibleLines || visibleLines <= 0 {
		result := make([]string, visibleLines)
		for i := range result {
			result[i] = " "
		}
		return result
	}
	thumbSize := max(1, visibleLines*visibleLines/totalLines)
	maxOffset := totalLines - visibleLines
	thumbStart := 0
	if maxOffset > 0 {
		thumbStart = offset * (visibleLines - thumbSize) / maxOffset
	}
	if thumbStart < 0 {
		thumbStart = 0
	}
	if thumbStart+thumbSize > visibleLines {
		thumbStart = visibleLines - thumbSize
	}

	result := make([]string, visibleLines)
	for i := range result {
		if i >= thumbStart && i < thumbStart+thumbSize {
			result[i] = "█"
		} else {
			result[i] = "░"
		}
	}
	return result
}

// appendScrollbar joins each content line with the corresponding scrollbar glyph.
// contentWidth is the column's usable content area (= columnWidth - padding).
// Each line is truncated to (contentWidth-2) visible chars so that appending " █"
// never exceeds the content area and wraps to the next terminal line.
func appendScrollbar(contentLines []string, scrollbar []string, contentWidth int) []string {
	maxLine := contentWidth - 2
	if maxLine < 1 {
		maxLine = 1
	}
	result := make([]string, len(contentLines))
	for i, line := range contentLines {
		sb := " "
		if i < len(scrollbar) {
			sb = scrollbar[i]
		}
		truncated := lipgloss.NewStyle().MaxWidth(maxLine).Render(line)
		result[i] = truncated + " " + sb
	}
	return result
}

// --- Context lines filtering ---

// filterContextLines keeps only N context lines around each change (+/-) line,
// inserting "..." gap markers where context is elided.
func filterContextLines(lines []string, n int) []string {
	if n <= 0 || len(lines) == 0 {
		// n==0 means show only +/- lines (no context)
		var result []string
		for _, l := range lines {
			if strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-") {
				result = append(result, l)
			}
		}
		return result
	}

	// mark which lines to keep (change lines + N context around them)
	keep := make([]bool, len(lines))
	for i, l := range lines {
		if strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-") {
			for j := max(0, i-n); j <= min(len(lines)-1, i+n); j++ {
				keep[j] = true
			}
		}
	}

	var result []string
	lastKept := -1
	for i, l := range lines {
		if keep[i] {
			if lastKept >= 0 && i-lastKept > 1 {
				result = append(result, "...")
			}
			result = append(result, l)
			lastKept = i
		}
	}
	return result
}
