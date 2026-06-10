package main

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var (
	// Palette uses ANSI 16-color indices so the user's terminal theme picks
	// the actual colors (Solarized, Dracula, Nord, etc.). Text foregrounds
	// without an explicit color fall back to the terminal default.
	colBrand   = lipgloss.Color("5")  // magenta (purple-ish in most themes)
	colAccent  = lipgloss.Color("13") // bright magenta
	colSuccess = lipgloss.Color("2")  // green
	colChipBG  = lipgloss.Color("8")  // bright black (dark gray)
	colMuted   = lipgloss.Color("8")  // bright black

	infoChip = lipgloss.NewStyle().
			Background(colChipBG).
			Padding(0, 1)

	// Bordered sections.
	// Bottom border only: side rules would be captured by the terminal's
	// native click-drag selection alongside log text.
	viewportBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder(), false, false, true, false).
			BorderForeground(colMuted)

	inputBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colMuted).
			Padding(0, 1)

	inputBoxFocused = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colBrand).
			Padding(0, 1)

	helpBox = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colMuted).
		Padding(0, 1)

	helpPopup = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colBrand).
			Padding(1, 3)

	toastBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colSuccess).
			Padding(0, 2)
)

// barLayout holds the outer (border-inclusive) widths of the bottom-bar
// boxes. It is the single source of truth for bottom-bar geometry, shared by
// relayout (which sizes the textinput) and View (which renders the boxes).
type barLayout struct {
	lines, input, matches, help int
}

// barLayout sizes the bottom bar from the window width. Chip widths are
// fixed upper bounds (max line count, max match counter) so the input box
// doesn't resize as counts grow.
func (m model) barLayout() barLayout {
	var bl barLayout
	bl.help = m.width / 2
	bl.lines = len(fmt.Sprintf("%d lines", maxLines)) + 4
	if m.query != "" {
		bl.matches = len(fmt.Sprintf("%d/%d", maxLines, maxLines)) + 4 // covers "no matches" too
	}
	bl.input = m.width - bl.help - bl.lines - bl.matches
	// Border (2) + padding (2) + prompt (2) + a few visible chars. On very
	// narrow windows the bar may overflow and wrap, but no width goes negative.
	const minInput = 14
	if bl.input < minInput {
		bl.input = minInput
	}
	return bl
}

// relayout recomputes child component sizes from the current window dims and
// help-expanded state. Called from WindowSizeMsg and when ? is toggled.
func (m *model) relayout() {
	if m.width == 0 || m.height == 0 {
		return
	}

	bl := m.barLayout()

	// Used by the bubbles help component (search-mode help only).
	m.help.SetWidth(bl.help - 4)

	// Bottom bar always shows short help; full help lives in the popup.
	helpInnerH := lipgloss.Height(m.renderShortHelp())
	if helpInnerH < 1 {
		helpInnerH = 1
	}
	bottomH := helpInnerH + 2 // top + bottom border

	titleH := 0
	if m.hasTitle() {
		titleH = 1
	}
	const vpBorders = 1 // bottom border only
	vpW := m.width
	vpH := m.height - titleH - vpBorders - bottomH
	if vpW < 1 {
		vpW = 1
	}
	if vpH < 1 {
		vpH = 1
	}
	m.viewport.SetWidth(vpW)
	m.viewport.SetHeight(vpH)

	m.search.SetWidth(bl.input - 6) // border (2) + padding (2) + prompt (2)
}

func (m model) View() tea.View {
	if !m.ready {
		v := tea.NewView("loading...")
		v.AltScreen = true
		return v
	}

	vpInner := m.viewport.View()
	vpW, vpH := m.viewport.Width(), m.viewport.Height()
	var overlays []*lipgloss.Layer
	if m.help.ShowAll {
		popup := helpPopup.Render(m.renderFullHelp())
		px := (vpW - lipgloss.Width(popup)) / 2
		py := (vpH - lipgloss.Height(popup)) / 2
		overlays = append(overlays, lipgloss.NewLayer(popup).X(px).Y(py).Z(1))
	}
	if m.toast != "" {
		t := toastBox.Render(m.toast)
		tx := vpW - lipgloss.Width(t) - 1
		if tx < 0 {
			tx = 0
		}
		overlays = append(overlays, lipgloss.NewLayer(t).X(tx).Y(0).Z(2))
	}
	if len(overlays) > 0 {
		// Pad the bg to full viewport dims so the compositor canvas covers
		// the whole region (viewport.View lines aren't right-padded, which
		// would otherwise truncate the composed output to the longest line).
		bgPadded := lipgloss.Place(vpW, vpH, lipgloss.Left, lipgloss.Top, vpInner)
		layers := append([]*lipgloss.Layer{lipgloss.NewLayer(bgPadded)}, overlays...)
		vpInner = lipgloss.NewCompositor(layers...).Render()
	}
	// Pin the box width to vpW so the bottom rule spans the full viewport
	// regardless of how wide the inner content's longest line happens to be.
	viewportView := viewportBox.Width(vpW).Render(vpInner)

	helpContent := m.renderShortHelp()
	contentH := lipgloss.Height(helpContent)

	style := inputBox
	if m.mode == modeSearch {
		style = inputBoxFocused
	}

	bl := m.barLayout()
	linesBox := inputBox.Width(bl.lines - 2).Height(contentH).Render(fmt.Sprintf("%d lines", len(m.lines)))
	inputView := style.Width(bl.input - 2).Height(contentH).Render(m.search.View())

	row := []string{linesBox, inputView}
	if m.query != "" {
		row = append(row, inputBox.Width(bl.matches-2).Height(contentH).Render(m.matchesText()))
	}
	row = append(row, helpBox.Width(bl.help-2).Render(helpContent))
	bottom := lipgloss.JoinHorizontal(lipgloss.Top, row...)

	out := viewportView + "\n" + bottom
	if m.hasTitle() {
		out += "\n" + m.titleBar()
	}
	v := tea.NewView(out)
	v.AltScreen = true
	return v
}

func (m model) hasTitle() bool {
	return m.cmd != "" || m.wrap
}

func (m model) matchesText() string {
	if len(m.matches) == 0 {
		return "no matches"
	}
	return fmt.Sprintf("%d/%d", m.currentMatch+1, len(m.matches))
}

// helpStyleFor returns the per-binding key/desc styles, with Follow and
// Filter flipped to green when active.
func (m model) helpStyleFor() func(string) (lipgloss.Style, lipgloss.Style) {
	keyStyle := lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	keyOnStyle := lipgloss.NewStyle().Foreground(colSuccess).Bold(true)
	descStyle := lipgloss.NewStyle().Foreground(colMuted)
	descOnStyle := lipgloss.NewStyle().Foreground(colSuccess)
	return func(k string) (lipgloss.Style, lipgloss.Style) {
		if (k == "F" && m.follow) || (k == "f" && m.filter) {
			return keyOnStyle, descOnStyle
		}
		return keyStyle, descStyle
	}
}

// renderShortHelp builds the inline short-help bar shown at the bottom.
func (m model) renderShortHelp() string {
	if m.mode == modeSearch {
		return m.help.ShortHelpView(searchKeys(m.keys).ShortHelp())
	}
	styleFor := m.helpStyleFor()
	sep := lipgloss.NewStyle().Foreground(colMuted).Render(" · ")
	parts := []string{}
	for _, b := range normalKeys(m.keys).ShortHelp() {
		h := b.Help()
		ks, ds := styleFor(h.Key)
		parts = append(parts, ks.Render(h.Key)+" "+ds.Render(h.Desc))
	}
	return strings.Join(parts, sep)
}

// renderFullHelp builds the multi-column full-help grid used inside the popup.
func (m model) renderFullHelp() string {
	if m.mode == modeSearch {
		return m.help.FullHelpView(searchKeys(m.keys).FullHelp())
	}
	styleFor := m.helpStyleFor()
	groups := normalKeys(m.keys).FullHelp()
	cols := make([]string, len(groups))
	for ci, group := range groups {
		maxKey := 0
		for _, b := range group {
			if w := lipgloss.Width(b.Help().Key); w > maxKey {
				maxKey = w
			}
		}
		rows := make([]string, len(group))
		for ri, b := range group {
			h := b.Help()
			ks, ds := styleFor(h.Key)
			pad := strings.Repeat(" ", maxKey-lipgloss.Width(h.Key))
			rows[ri] = ks.Render(h.Key) + pad + "  " + ds.Render(h.Desc)
		}
		cols[ci] = strings.Join(rows, "\n")
	}
	blocks := make([]string, 0, 2*len(cols)-1)
	for i, c := range cols {
		if i > 0 {
			blocks = append(blocks, "   ")
		}
		blocks = append(blocks, c)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, blocks...)
}

func (m model) titleBar() string {
	var left string
	if m.cmd != "" {
		maxCmd := m.width / 3
		if maxCmd > 60 {
			maxCmd = 60
		}
		if maxCmd < 12 {
			maxCmd = 12
		}
		left += infoChip.Render(ansi.Truncate(m.cmd, maxCmd, "…"))
	}
	var right string
	if m.wrap {
		right += infoChip.Render("WRAP")
	}

	pad := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 0 {
		pad = 0
	}
	return left + strings.Repeat(" ", pad) + right
}
