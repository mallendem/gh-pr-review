package gui

import (
	"fmt"
	"sort"
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

// model holds the GUI state.
type model struct {
	// phase: 0 = user selection, 1 = approval (current view)
	phase int

	hashes      []string
	changeMap   gh.HashChangeMap
	hashPrMap   gh.HashPrMap
	prMap       map[string][]string
	verifiedMap gh.PrVerifiedMap
	client      *gh.GhClient

	approved  map[string]bool
	declined  map[string]bool
	prSkipped map[string]bool
	committed map[string]bool
	propagate bool
	hashIndex int // which hash list item is selected
	col       int // 0-left(hash),1-middle(change),2-right(prs)

	status string
	dryRun bool

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
	changeFileTab int           // index of selected file tab

	// User selection phase (phase 0) fields
	availableUsers  []string
	userSelected    map[string]bool
	userCursor      int
	userHashPrMap   gh.GhPrHashMap
	userScrollOffset int
}

// New creates and returns a Bubble Tea program configured for the user.
func New(user string, propagate bool, dryRun bool) (*tea.Program, error) {
	hashes, availableUsers, userHashPrMap, changeMap, hashPrMap, prMap, verifiedMap, hashFileMap, client, err := approve.PrepareGUI(user)
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
		phase:           phase,
		hashes:          hashes,
		changeMap:       changeMap,
		hashPrMap:       hashPrMap,
		prMap:           prMap,
		verifiedMap:     verifiedMap,
		client:          client,
		approved:        map[string]bool{},
		declined:        map[string]bool{},
		prSkipped:       map[string]bool{},
		committed:       map[string]bool{},
		propagate:       propagate,
		col:             0,
		dryRun:          dryRun,
		focusRow:        0,
		stagedOffset:    0,
		stagedPRList:    nil,
		hashFileMap:     hashFileMap,
		availableUsers:  availableUsers,
		userSelected:    map[string]bool{},
		userCursor:      0,
		userHashPrMap:   userHashPrMap,
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
			if k == "c" { // commit changes
				// build a filtered map with only PRs that are eligible (not skipped, not declined, and all hashes approved)
				filtered := make(map[string][]string)
				for prKey, phashes := range m.prMap {
					if m.prSkipped[prKey] {
						continue
					}
					// skip PRs where all hashes were declined
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
					// ensure every non-declined hash is approved
					allApproved := true
					for _, ph := range phashes {
						if m.declined[ph] {
							allApproved = false
							break
						}
						// if the user supplied approvals, require that each hash be in approved map
						if len(m.approved) > 0 {
							if !m.approved[ph] {
								allApproved = false
								break
							}
						} else {
							// no approvals recorded -> do not consider as approved
							allApproved = false
							break
						}
					}
					if allApproved {
						filtered[prKey] = phashes
					}
				}
				// only call approval API for PRs that are fully approved
				if len(filtered) > 0 {
					approve.ProcessApprovals(filtered, m.approved, m.declined, m.prSkipped, m.hashPrMap, m.client, m.dryRun)
					// mark hashes associated with filtered PRs as committed
					for _, phashes := range filtered {
						for _, ph := range phashes {
							m.committed[ph] = true
						}
					}
					// recompute staged PRs after commit
					// reconcile skipped PRs and recompute staged list
					m.reconcilePrSkipped()
					m.updateStagedList()
				}
				m.status = "committed approvals"
				// refresh viewport to top so user sees PR body start
				m.viewport.GotoTop()
				m.updateViewportContent()
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
				midLines = append(midLines, display)
			}
		} else {
			midLines = append(midLines, "(no changes)")
		}
	}

	// PRs column: Related PRs for selected hash
	var prLines []string
	prLines = append(prLines, rightTitle)
	if selectedHash != "" {
		if prs, ok := m.hashPrMap[selectedHash]; ok {
			fullPRs := []string{}
			for i, pr := range prs {
				prKey := pr.GetHTMLURL()
				fullPRs = append(fullPRs, m.renderPRLabel(prKey, i))
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
	hint := "tab: switch row • a/d: left/right • w/s: up/down • e/r: file tabs • x: approve • c: commit • q: quit • f: decline • alt+a/d: hscroll"
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

// changeFilesForHash returns the sorted list of unique filenames for the
// selected hash's PR. Returns nil if no file info is available.
func (m model) changeFilesForHash() []string {
	sel := m.selectedHash()
	if sel == "" || len(m.hashFileMap) == 0 {
		return nil
	}
	// find the PR(s) for this hash
	prs, ok := m.hashPrMap[sel]
	if !ok || len(prs) == 0 {
		return nil
	}
	prKey := prs[0].GetHTMLURL()
	prHashes, ok := m.prMap[prKey]
	if !ok {
		return nil
	}
	seen := map[string]struct{}{}
	var files []string
	for _, h := range prHashes {
		if f, ok := m.hashFileMap[h]; ok && f != "" {
			if _, dup := seen[f]; !dup {
				seen[f] = struct{}{}
				files = append(files, f)
			}
		}
	}
	sort.Strings(files)
	return files
}

// changesForFileTab returns the change lines for the active file tab.
// It concatenates changes from all hashes in the PR that belong to the
// selected file, with "---" separators between hunks. Falls back to
// changeMap[selectedHash] if no file info is available.
func (m model) changesForFileTab() []string {
	sel := m.selectedHash()
	if sel == "" {
		return nil
	}
	files := m.changeFilesForHash()
	if len(files) == 0 {
		// no file info — fall back to raw change map
		return m.changeMap[sel]
	}
	tabIdx := m.changeFileTab
	if tabIdx < 0 || tabIdx >= len(files) {
		tabIdx = 0
	}
	activeFile := files[tabIdx]

	// find the PR key
	prs, ok := m.hashPrMap[sel]
	if !ok || len(prs) == 0 {
		return m.changeMap[sel]
	}
	prKey := prs[0].GetHTMLURL()
	prHashes, ok := m.prMap[prKey]
	if !ok {
		return m.changeMap[sel]
	}

	var result []string
	first := true
	for _, h := range prHashes {
		if m.hashFileMap[h] != activeFile {
			continue
		}
		changes, ok := m.changeMap[h]
		if !ok || len(changes) == 0 {
			continue
		}
		if !first {
			result = append(result, "---")
		}
		result = append(result, changes...)
		first = false
	}
	return result
}

// renderFileTabs renders the file tab bar for the changes panel.
func (m model) renderFileTabs(midWidth int) string {
	files := m.changeFilesForHash()
	if len(files) == 0 {
		return ""
	}
	tabIdx := m.changeFileTab
	if tabIdx < 0 || tabIdx >= len(files) {
		tabIdx = 0
	}
	var parts []string
	for i, f := range files {
		// use basename
		base := f
		if idx := strings.LastIndex(f, "/"); idx >= 0 {
			base = f[idx+1:]
		}
		if i == tabIdx {
			parts = append(parts, lipgloss.NewStyle().Bold(true).Background(lipgloss.Color("62")).Render(" "+base+" "))
		} else {
			parts = append(parts, " "+base+" ")
		}
	}
	line := strings.Join(parts, "│")
	// truncate if too wide
	contentW := midWidth - 4
	if contentW < 10 {
		contentW = 10
	}
	r := []rune(line)
	if len(r) > contentW {
		line = string(r[:contentW-1]) + "»"
	}
	return line
}

// updateChangeFileTab auto-selects the file tab matching the current hash's file.
func (m *model) updateChangeFileTab() {
	files := m.changeFilesForHash()
	if len(files) == 0 {
		m.changeFileTab = 0
		return
	}
	sel := m.selectedHash()
	if sel == "" {
		m.changeFileTab = 0
		return
	}
	currentFile := m.hashFileMap[sel]
	for i, f := range files {
		if f == currentFile {
			m.changeFileTab = i
			return
		}
	}
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
