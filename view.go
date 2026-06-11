package main

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

var (
	// Palette uses ANSI 16-color indices so the user's terminal theme picks
	// the actual colors (Solarized, Dracula, Nord, etc.). Text foregrounds
	// without an explicit color fall back to the terminal default.
	colBrand   = lipgloss.Color("5")  // magenta (purple-ish in most themes)
	colAccent  = lipgloss.Color("13") // bright magenta
	colSuccess = lipgloss.Color("2")  // green
	colMuted   = lipgloss.Color("8")  // bright black

	paddingWidth  = 1
	paddingHeight = 0
	infoBoxWidth  = 16

	// Bordered sections.
	// Bottom border only: side rules would be captured by the terminal's
	// native click-drag selection alongside log text.
	viewportBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder(), false, false, true, false).
			BorderForeground(colMuted)

	box = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colMuted).
		Padding(paddingHeight, paddingWidth)

	boxFocused = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colBrand).
			Padding(paddingHeight, paddingWidth)
)

// relayout recomputes child component sizes from the current window dims and
// help-expanded state. Called from WindowSizeMsg and when ? is toggled.
func (m *model) relayout() {
	if m.width == 0 || m.height == 0 {
		return
	}

	// Search-mode short help is truncated by the bubbles help component to its
	// width; size it to the help box's content area (full width minus padding).
	m.help.SetWidth(m.width - 2*paddingWidth)

	// The bottom bar is a help line stacked over the input row, both sized from
	// the rendered short-help height. Derive bottomH the same way so the space
	// reserved here stays in sync with what View actually renders.
	contentH := lipgloss.Height(m.renderShortHelp())
	if contentH < 1 {
		contentH = 1
	}
	bottomH := contentH*2 + 2 // help line + input row (content + top/bottom border)

	const vpBorders = 1 // bottom border only
	vpW := m.width
	vpH := m.height - vpBorders - bottomH
	if vpW < 1 {
		vpW = 1
	}
	if vpH < 1 {
		vpH = 1
	}
	m.viewport.SetWidth(vpW)
	m.viewport.SetHeight(vpH)

	// Match View's inputBoxWidth, less the box frame: border (2) + padding (2) +
	// prompt (2). Narrow windows clamp to a minimum rather than going negative.
	inputBoxWidth := m.width - (infoBoxWidth * 2)
	searchWidth := inputBoxWidth - 6
	if searchWidth < 1 {
		searchWidth = 1
	}
	m.search.SetWidth(searchWidth)
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
		popup := boxFocused.Padding(1, 3).Render(m.renderFullHelp())
		px := (vpW - lipgloss.Width(popup)) / 2
		py := (vpH - lipgloss.Height(popup)) / 2
		overlays = append(overlays, lipgloss.NewLayer(popup).X(px).Y(py).Z(1))
	}
	if m.toast != "" {
		t := box.BorderForeground(colSuccess).Padding(0, 2).Render(m.toast)
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

	inputStyle := box
	if m.mode == modeSearch {
		inputStyle = boxFocused
	}

	inputBoxWidth := m.width - (infoBoxWidth * 2)

	inputBox := inputStyle.Width(inputBoxWidth).Height(contentH).Render(m.search.View())
	linesBox := box.Width(infoBoxWidth).Height(contentH).Align(lipgloss.Right).Render(fmt.Sprintf("%d lines", len(m.lines)))
	matchesBox := box.Width(infoBoxWidth).Height(contentH).Align(lipgloss.Right).Render(m.matchesText())

	inputRow := lipgloss.JoinHorizontal(lipgloss.Top, linesBox, inputBox, matchesBox)
	helpBox := lipgloss.NewStyle().Padding(0, 1).Width(m.width).Align(lipgloss.Right).Render(helpContent)
	bottom := lipgloss.JoinVertical(lipgloss.Left, helpBox, inputRow)

	out := viewportView + "\n" + bottom

	v := tea.NewView(out)
	v.AltScreen = true
	return v
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

// renderShortHelp builds the inline short-help bar shown at the bottom. It
// degrades gracefully on narrow windows: the help and quit bindings always
// stay visible (help is the gateway to the full key list), and the middle
// bindings are dropped from the tail until the line fits on one row.
func (m model) renderShortHelp() string {
	if m.mode == modeSearch {
		return m.help.ShortHelpView(searchKeys(m.keys).ShortHelp())
	}
	styleFor := m.helpStyleFor()
	sep := lipgloss.NewStyle().Foreground(colMuted).Render(" · ")
	sepW := lipgloss.Width(sep)

	item := func(key, desc string) string {
		ks, ds := styleFor(key)
		return ks.Render(key) + " " + ds.Render(desc)
	}

	// Reserve room for the always-visible tail (help · quit) so they survive
	// even when the middle bindings don't fit.
	helpH, quitH := m.keys.Help.Help(), m.keys.Quit.Help()
	tail := []string{item(helpH.Key, helpH.Desc), item(quitH.Key, quitH.Desc)}
	reserve := 0
	for _, t := range tail {
		reserve += lipgloss.Width(t) + sepW
	}

	budget := m.width - 2*paddingWidth // help box has 1 cell of padding each side
	parts := []string{}
	width := 0
	for _, b := range normalKeys(m.keys).ShortHelp() {
		h := b.Help()
		if h.Key == helpH.Key || h.Key == quitH.Key {
			continue // rendered in the reserved tail
		}
		it := item(h.Key, h.Desc)
		next := lipgloss.Width(it)
		if len(parts) > 0 {
			next += sepW
		}
		if width+next+reserve > budget {
			break
		}
		parts = append(parts, it)
		width += next
	}
	parts = append(parts, tail...)
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
