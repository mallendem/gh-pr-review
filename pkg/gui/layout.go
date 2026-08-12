package gui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Rendering geometry, in terminal cells:
//
//	outer width  = content width + 2 (left/right border)
//	content area = content width - 2 (left/right padding)
//	text area    = content area - 2 (scrollbar gutter " █")
//
// Every top-row column must respect this or its lines wrap and the whole grid
// shifts by a row.
const (
	borderCells    = 2 // left + right border
	paddingCells   = 2 // left + right padding
	scrollbarCells = 2 // separator space + glyph
	chromeCells    = borderCells + paddingCells + scrollbarCells
)

var (
	titleStyle    = lipgloss.NewStyle().Bold(true)
	selectedStyle = lipgloss.NewStyle().Background(lipgloss.Color("62"))
	focusBorder   = lipgloss.Color("11")
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	addStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	delStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	committedStyl = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
)

// styledLine is a line of pane content plus the style to draw it with. Keeping
// them apart lets the selection highlight wrap the whole line without an inner
// reset sequence knocking the background out halfway through.
type styledLine struct {
	text  string
	style lipgloss.Style
}

func plain(text string) styledLine { return styledLine{text: text} }

func styled(text string, style lipgloss.Style) styledLine {
	return styledLine{text: text, style: style}
}

// pane is one bordered column of the top row.
type pane struct {
	title    string
	header   []string // non-scrolling lines under the title, e.g. a tab bar
	lines    []styledLine
	offset   int // first visible line index
	selected int // index to highlight, or -1
	width    int // content width, borders excluded
	focused  bool
}

// visibleLines is how many scrolling content lines the pane can show.
func (p pane) visibleLines(contentHeight int) int {
	return max(contentHeight-1-len(p.header), 1)
}

// render draws the pane at the given content height (borders excluded).
func (p pane) render(contentHeight int) string {
	textW := max(p.width-paddingCells-scrollbarCells, 4)
	visible := p.visibleLines(contentHeight)
	offset := clampOffset(p.offset, len(p.lines), visible)

	rows := make([]string, 0, contentHeight)
	rows = append(rows, fit(titleStyle.Render(p.title), textW+scrollbarCells))
	for _, h := range p.header {
		rows = append(rows, fit(h, textW+scrollbarCells))
	}

	bar := scrollbar(visible, len(p.lines), offset)
	for i := range visible {
		var line styledLine
		if idx := offset + i; idx < len(p.lines) {
			line = p.lines[idx]
		}
		style := line.style
		if p.selected >= 0 && offset+i == p.selected {
			style = style.Background(selectedStyle.GetBackground())
		}
		rows = append(rows, style.Render(fit(line.text, textW))+" "+bar[i])
	}

	box := lipgloss.NewStyle().
		Width(p.width).
		Height(contentHeight).
		Border(lipgloss.NormalBorder()).
		PaddingLeft(1).
		PaddingRight(1)
	if p.focused {
		box = box.BorderForeground(focusBorder)
	}
	return box.Render(strings.Join(rows, "\n"))
}

// fit truncates or space-pads a line to exactly w display cells. It never wraps
// — lipgloss Width() would, which is what made columns spill onto the next row.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = lipgloss.NewStyle().MaxWidth(w).Render(s)
	if pad := w - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// clampOffset keeps a scroll offset inside [0, total-visible].
func clampOffset(offset, total, visible int) int {
	return min(max(offset, 0), max(total-visible, 0))
}

// scrollbar builds the vertical gutter, one glyph per visible line. It returns
// blanks when everything already fits.
func scrollbar(visible, total, offset int) []string {
	result := make([]string, visible)
	if visible <= 0 {
		return result
	}
	if total <= visible {
		for i := range result {
			result[i] = " "
		}
		return result
	}
	thumb := max(1, visible*visible/total)
	start := offset * (visible - thumb) / (total - visible)
	start = min(max(start, 0), visible-thumb)
	for i := range result {
		if i >= start && i < start+thumb {
			result[i] = "█"
		} else {
			result[i] = "░"
		}
	}
	return result
}

// window returns the slice of items visible at the given offset.
func window[T any](items []T, offset, visible int) []T {
	if offset >= len(items) || visible <= 0 {
		return nil
	}
	return items[offset:min(offset+visible, len(items))]
}

// ensureVisible scrolls offset just far enough to keep index in view.
func ensureVisible(offset *int, index, visible int) {
	if index < *offset {
		*offset = index
	}
	if index >= *offset+visible {
		*offset = index - visible + 1
	}
}
