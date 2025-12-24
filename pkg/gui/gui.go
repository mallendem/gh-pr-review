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
	hashes    []string
	changeMap gh.HashChangeMap
	hashPrMap gh.HashPrMap
	prMap     map[string][]string
	client    *gh.GhClient

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
}

// New creates and returns a Bubble Tea program configured for the user.
func New(user string, propagate bool, dryRun bool) (*tea.Program, error) {
	hashes, changeMap, hashPrMap, prMap, client, err := approve.PrepareManualApproval(user)
	if err != nil {
		return nil, err
	}
	m := model{
		hashes:       hashes,
		changeMap:    changeMap,
		hashPrMap:    hashPrMap,
		prMap:        prMap,
		client:       client,
		approved:     map[string]bool{},
		declined:     map[string]bool{},
		prSkipped:    map[string]bool{},
		committed:    map[string]bool{},
		propagate:    propagate,
		col:          0,
		dryRun:       dryRun,
		focusRow:     0,
		stagedOffset: 0,
		stagedPRList: nil,
	}
	// compute initial staged list so the UI shows consistent state immediately
	m.updateStagedList()
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
			// compute visible width for changes column (approx)
			termW := m.termWidth
			if termW == 0 {
				termW = 120
			}
			// compute left/mid/right widths same as in View to get midWidth
			leftMax := 0
			for _, h := range m.hashes {
				short := h
				if len(h) > 6 {
					short = h[:6]
				}
				if len(short) > leftMax {
					leftMax = len(short)
				}
			}
			leftWidth := leftMax + 4
			if leftWidth < 8 {
				leftWidth = 8
			}
			// compute right width from longest PR URL
			maxPR := 0
			for _, prs := range m.hashPrMap {
				for _, pr := range prs {
					l := len(pr.GetHTMLURL())
					if l > maxPR {
						maxPR = l
					}
				}
			}
			prWidth := maxPR + 6
			if prWidth < 20 {
				prWidth = 20
			}
			// Use a fixed-ish staged column width (wide enough for urls) and cap prWidth to avoid overflow
			stagedWidth := 36
			if stagedWidth > termW/4 {
				stagedWidth = termW / 4
			}
			// cap prWidth so we have room for mid and staged
			if prWidth > termW/3 {
				prWidth = termW / 3
			}
			// allocate midWidth as remaining space; keep conservative padding for borders
			midWidth := termW - leftWidth - prWidth - stagedWidth - 6
			if midWidth < 10 {
				// shrink prWidth if needed
				extra := 10 - midWidth
				if prWidth-extra > 10 {
					prWidth -= extra
				} else {
					prWidth = 10
				}
				midWidth = termW - leftWidth - prWidth - stagedWidth - 6
				if midWidth < 10 {
					midWidth = 10
					// if still too small, shrink stagedWidth
					if stagedWidth > 20 {
						stagedWidth = 20
					}
				}
			}
			contentW := midWidth - 4
			if contentW < 10 {
				contentW = 10
			}
			// determine maximum change line length for the selected hash
			sel := ""
			if len(m.hashes) > 0 && m.hashIndex < len(m.hashes) {
				sel = m.hashes[m.hashIndex]
			}
			maxLen := 0
			if sel != "" {
				if changes, ok := m.changeMap[sel]; ok {
					for _, cl := range changes {
						if len(cl) > maxLen {
							maxLen = len(cl)
						}
					}
				}
			}
			maxOff := 0
			if maxLen > contentW {
				maxOff = maxLen - contentW
			}
			if m.changeHOffset < maxOff {
				m.changeHOffset++
			}
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
			// column navigation
			if k == "a" { // left
				if m.col > 0 {
					m.col--
				}
				return m, nil
			}
			if k == "d" { // right
				if m.col < 3 {
					m.col++
				}
				return m, nil
			}
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
				// changes pane scroll
				if k == "w" {
					if m.changeOffset > 0 {
						m.changeOffset--
					}
					return m, nil
				}
				if k == "s" {
					// bound by number of change lines for selected hash
					sel := ""
					if len(m.hashes) > 0 && m.hashIndex < len(m.hashes) {
						sel = m.hashes[m.hashIndex]
					}
					if sel != "" {
						if changes, ok := m.changeMap[sel]; ok {
							visible := m.topVisibleLines() - 1 // minus title
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
						approve.AutoApproveLinkedHashesQuiet(h, m.approved, m.declined, m.hashPrMap, m.prMap)
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
					approve.AutoDeclineLinkedHashesQuiet(h, m.declined, m.prSkipped, m.hashPrMap, m.prMap)
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
					approve.RunProcessApprovals(filtered, m.approved, m.declined, m.prSkipped, m.hashPrMap, m.client, m.dryRun)
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
	selectedHash := ""
	if len(m.hashes) > 0 && m.hashIndex < len(m.hashes) {
		selectedHash = m.hashes[m.hashIndex]
	}
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
	// refresh cached staged PRs whenever the visible selection or approvals change
	m.updateStagedList()
}

// updateStagedList recomputes and stores the list of PR keys that would be approved
func (m *model) updateStagedList() {
	m.stagedPRList = m.stagedPrKeys()
}

// View implements tea.Model
func (m model) View() string {
	// compute dynamic widths based on terminal size and content
	termW := m.termWidth
	if termW == 0 {
		termW = 120
	}

	// compute left width from longest short hash representation
	leftMax := 0
	for _, h := range m.hashes {
		short := h
		if len(h) > 6 {
			short = h[:6]
		}
		if len(short) > leftMax {
			leftMax = len(short)
		}
	}
	leftWidth := leftMax + 4 // marker + space + padding
	if leftWidth < 8 {
		leftWidth = 8
	}

	// compute right width from longest PR URL
	maxPR := 0
	for _, prs := range m.hashPrMap {
		for _, pr := range prs {
			l := len(pr.GetHTMLURL())
			if l > maxPR {
				maxPR = l
			}
		}
	}
	prWidth := maxPR + 6
	if prWidth < 20 {
		prWidth = 20
	}
	// Use a fixed-ish staged column width (wide enough for urls) and cap prWidth to avoid overflow
	stagedWidth := 36
	if stagedWidth > termW/4 {
		stagedWidth = termW / 4
	}
	// cap prWidth so we have room for mid and staged
	if prWidth > termW/3 {
		prWidth = termW / 3
	}
	// allocate midWidth as remaining space; keep conservative padding for borders
	midWidth := termW - leftWidth - prWidth - stagedWidth - 6
	if midWidth < 10 {
		// shrink prWidth if needed
		extra := 10 - midWidth
		if prWidth-extra > 10 {
			prWidth -= extra
		} else {
			prWidth = 10
		}
		midWidth = termW - leftWidth - prWidth - stagedWidth - 6
		if midWidth < 10 {
			midWidth = 10
			// if still too small, shrink stagedWidth
			if stagedWidth > 20 {
				stagedWidth = 20
			}
		}
	}

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
	left := lipgloss.NewStyle().Width(leftWidth).Height(topBlockH).Border(lipgloss.NormalBorder()).Padding(1)
	mid := lipgloss.NewStyle().Width(midWidth).Height(topBlockH).Border(lipgloss.NormalBorder()).Padding(1)
	// right two columns: pr and staged (staged shows PRs that would be approved)
	prStyle := lipgloss.NewStyle().Width(prWidth).Height(topBlockH).Border(lipgloss.NormalBorder()).Padding(1)
	stagedStyle := lipgloss.NewStyle().Width(stagedWidth).Height(topBlockH).Border(lipgloss.NormalBorder()).Padding(1)

	// build column title row
	titleStyle := lipgloss.NewStyle().Bold(true)
	leftTitle := titleStyle.Render("Hashes")
	midTitle := titleStyle.Render("Changes")
	rightTitle := titleStyle.Render("Related PRs")
	stagedTitle := titleStyle.Render("Staged changes")

	selectedHash := ""
	if len(m.hashes) > 0 && m.hashIndex < len(m.hashes) {
		selectedHash = m.hashes[m.hashIndex]
	}

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

	// middle column: show changes for selected hash
	var midLines []string
	midLines = append(midLines, midTitle)
	if selectedHash != "" {
		if changes, ok := m.changeMap[selectedHash]; ok {
			// build a list of change lines
			fullChanges := changes
			visible := m.topVisibleLines()
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
				if m.col == 1 && m.focusRow == 0 {
					midLines = append(midLines, lipgloss.NewStyle().Background(lipgloss.Color("62")).Render(display))
				} else {
					midLines = append(midLines, display)
				}
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
	bodyView := m.viewport.View()
	if bodyView == "" {
		bodyView = "(no PR body)"
	}

	// footer with keybind hints (bottom-left)
	hint := "tab: switch row • a/d: left/right • w/s: up/down • x: approve • c: commit • q: quit • f: decline • alt+a/d: hscroll"
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

func (m *model) stagedPrKeys() []string {
	stagedPRs := []string{}
	for prKey, phashes := range m.prMap {
		// compute if all hashes for this PR were declined
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
		// determine if all non-declined hashes are approved
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
			// If this PR was previously skipped due to an earlier decline, unskip it now
			if m.prSkipped[prKey] {
				delete(m.prSkipped, prKey)
			}
			stagedPRs = append(stagedPRs, prKey)
		}
	}
	sort.Strings(stagedPRs)
	return stagedPRs
}

// isPRStaged returns true if the given PR meets the criteria to be staged:
// - not skipped
// - not all its hashes declined
// - all non-declined hashes are approved (and there is at least one approval recorded)
func (m *model) isPRStaged(prKey string) bool {
	phashes, ok := m.prMap[prKey]
	if !ok {
		return false
	}
	if m.prSkipped[prKey] {
		return false
	}
	allDeclined := true
	for _, ph := range phashes {
		if !m.declined[ph] {
			allDeclined = false
			break
		}
	}
	if allDeclined {
		return false
	}
	// require every non-declined hash to be approved and ensure we have approvals
	if len(m.approved) == 0 {
		return false
	}
	for _, ph := range phashes {
		if m.declined[ph] {
			return false
		}
		if !m.approved[ph] {
			return false
		}
	}
	return true
}

// prApprovalState returns (allApproved, anyDeclined, committed) for a PR based on current maps.
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

// renderPRLabel returns a label for a PR with consistent coloring based on approval state.
func (m *model) renderPRLabel(prKey string, idx int) string {
	label := fmt.Sprintf("[%d] %s", idx+1, prKey)
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

// reconcilePrSkipped checks PRs that were previously skipped and unskips any that
// now meet the staging criteria (i.e., not all-declined and all non-declined hashes approved).
func (m *model) reconcilePrSkipped() {
	for prKey, phashes := range m.prMap {
		if !m.prSkipped[prKey] {
			continue
		}
		// skip if all hashes for this PR are still declined
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
		// require every non-declined hash to be approved and ensure we have approvals
		if len(m.approved) == 0 {
			continue
		}
		allApproved := true
		for _, ph := range phashes {
			if m.declined[ph] {
				allApproved = false
				break
			}
			if !m.approved[ph] {
				allApproved = false
				break
			}
		}
		if allApproved {
			delete(m.prSkipped, prKey)
		}
	}
}
