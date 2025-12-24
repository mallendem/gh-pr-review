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
	changeHOffset int // horizontal offset for changes column
}

// New creates and returns a Bubble Tea program configured for the user.
func New(user string, propagate bool, dryRun bool) (*tea.Program, error) {
	hashes, changeMap, hashPrMap, prMap, client, err := approve.PrepareManualApproval(user)
	if err != nil {
		return nil, err
	}
	m := model{
		hashes:    hashes,
		changeMap: changeMap,
		hashPrMap: hashPrMap,
		prMap:     prMap,
		client:    client,
		approved:  map[string]bool{},
		declined:  map[string]bool{},
		prSkipped: map[string]bool{},
		propagate: propagate,
		col:       0,
		dryRun:    dryRun,
		focusRow:  0,
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
			maxPR := 0
			for _, prs := range m.hashPrMap {
				for _, pr := range prs {
					l := len(pr.GetHTMLURL())
					if l > maxPR {
						maxPR = l
					}
				}
			}
			rightWidth := maxPR + 6
			if rightWidth < 20 {
				rightWidth = 20
			}
			if leftWidth+rightWidth+20 > termW {
				rightWidth = termW - leftWidth - 20
				if rightWidth < 10 {
					rightWidth = 10
				}
			}
			midWidth := termW - leftWidth - rightWidth - 4
			if midWidth < 10 {
				midWidth = 10
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
				if m.col < 2 {
					m.col++
				}
				return m, nil
			}
			// behavior depends on which top column is active:
			// - col 0 (hashes): w/s move the selection
			// - col 1 (changes): w/s scroll the changes pane
			// - col 2 (PRs): w/s scroll the PR pane
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
				// PR pane scroll
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
			}
			if k == "x" { // approve
				if m.hashIndex >= 0 && m.hashIndex < len(m.hashes) {
					h := m.hashes[m.hashIndex]
					m.approved[h] = true
					if m.propagate {
						// use quiet variant to avoid printing into TUI
						approve.AutoApproveLinkedHashesQuiet(h, m.approved, m.declined, m.hashPrMap, m.prMap)
					}
					m.status = fmt.Sprintf("approved %s", h[:6])
				}
				return m, nil
			}
			if k == "f" { // decline selected hash with 'f'
				if m.hashIndex >= 0 && m.hashIndex < len(m.hashes) {
					h := m.hashes[m.hashIndex]
					m.declined[h] = true
					// auto-decline linked hashes quietly to avoid printing
					approve.AutoDeclineLinkedHashesQuiet(h, m.declined, m.prSkipped, m.hashPrMap, m.prMap)
					m.status = fmt.Sprintf("declined %s", h[:6])
				}
				return m, nil
			}
			if k == "c" { // commit changes
				approve.RunProcessApprovals(m.prMap, m.approved, m.declined, m.prSkipped, m.hashPrMap, m.client, m.dryRun)
				// mark hashes as approved for UI where appropriate (non-declined hashes in non-skipped PRs)
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
					for _, ph := range phashes {
						if !m.declined[ph] {
							m.approved[ph] = true
						}
					}
				}
				m.status = "committed approvals"
				// refresh viewport to top so user sees PR body start
				m.viewport.GotoTop()
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
	rightWidth := maxPR + 6
	// ensure some reasonable min/max
	if rightWidth < 20 {
		rightWidth = 20
	}
	// leave at least 20 for middle
	if leftWidth+rightWidth+20 > termW {
		// shrink rightWidth first to preserve middle space
		rightWidth = termW - leftWidth - 20
		if rightWidth < 10 {
			rightWidth = 10
		}
	}
	midWidth := termW - leftWidth - rightWidth - 4
	if midWidth < 10 {
		midWidth = 10
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
	right := lipgloss.NewStyle().Width(rightWidth).Height(topBlockH).Border(lipgloss.NormalBorder()).Padding(1)

	// build column title row
	titleStyle := lipgloss.NewStyle().Bold(true)
	leftTitle := titleStyle.Render("Hashes")
	midTitle := titleStyle.Render("Changes")
	rightTitle := titleStyle.Render("Related PRs")

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

	// right column: show PRs for selected hash, preserve URL no wrap by allocating width
	var rightLines []string
	rightLines = append(rightLines, rightTitle)
	if selectedHash != "" {
		if prs, ok := m.hashPrMap[selectedHash]; ok {
			fullPRs := []string{}
			for i, pr := range prs {
				prKey := pr.GetHTMLURL()
				label := fmt.Sprintf("[%d] %s", i+1, prKey)
				// decide color based on hashes associated with this PR
				if hashes, ok := m.prMap[prKey]; ok && len(hashes) > 0 {
					allApproved := true
					anyDeclined := false
					for _, h := range hashes {
						if m.declined[h] {
							anyDeclined = true
							allApproved = false
							break
						}
						if !m.approved[h] {
							allApproved = false
						}
					}
					if anyDeclined {
						label = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render(label)
					} else if allApproved {
						label = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render(label)
					}
				}
				fullPRs = append(fullPRs, label)
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
					rightLines = append(rightLines, lipgloss.NewStyle().Background(lipgloss.Color("62")).Render(line))
				} else {
					rightLines = append(rightLines, line)
				}
			}
		} else {
			rightLines = append(rightLines, "(no PRs)")
		}
	}

	leftBox := left.Render(strings.Join(leftLines, "\n"))
	midBox := mid.Render(strings.Join(midLines, "\n"))
	rightBox := right.Render(strings.Join(rightLines, "\n"))

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

	top := lipgloss.JoinHorizontal(lipgloss.Top, leftBox, midBox, rightBox)
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
