package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type logLine struct {
	raw     string // decorated bytes, ANSI codes preserved, never wrapped
	plain   string // ANSI-stripped, used for search and save
	lower   string // lowercased plain, cached so live search doesn't re-lower every line per keystroke
	display string // raw with current wrap applied (== raw when wrap is off)
}

type logLineMsg string
type logEOFMsg struct{}
type tickMsg time.Time
type cmdExitedMsg struct{ err error }

const renderInterval = 33 * time.Millisecond // ~30Hz

const toastDuration = 2500 * time.Millisecond

// Buffer cap. When len(lines) exceeds maxLines, drop the oldest dropChunk
// lines in one batch so the trim cost amortizes across many incoming lines.
const (
	maxLines  = 100_000
	dropChunk = 10_000
)

type mode int

const (
	modeNormal mode = iota
	modeSearch
)

type model struct {
	viewport     viewport.Model
	search       textinput.Model
	help         help.Model
	keys         keyMap
	lines        []logLine
	rows         []string // viewport content rows: display rows with filter + highlight applied
	rowOffsets   []int    // viewport row index of each logical line's first row
	mode         mode
	query        string
	queryLower   string
	matches      []int // indices into lines
	currentMatch int
	follow       bool
	wrap         bool
	filter       bool // when true + query set, render only matching lines
	ready        bool
	dirty        bool   // ingest accumulated, waiting for next tick to push rows
	ticking      bool   // a render/toast tick is scheduled; don't schedule another
	childExit    int    // spawned child's exit code (set via cmdExitedMsg; read by main after Run)
	cmd          string // command string when mnil is running a child process
	width        int
	height       int
	toast        string    // ephemeral overlay text (empty == no toast)
	toastExpiry  time.Time // when the toast should disappear (checked in tickMsg)
}

func initialModel(cmd string) model {
	ti := textinput.New()
	ti.Placeholder = "search..."
	ti.Prompt = "  "
	ti.CharLimit = 256
	tiStyles := ti.Styles()
	promptStyle := lipgloss.NewStyle().Foreground(colBrand).Bold(true)
	placeholderStyle := lipgloss.NewStyle().Foreground(colMuted).Italic(true)
	tiStyles.Focused.Prompt = promptStyle
	tiStyles.Focused.Placeholder = placeholderStyle
	tiStyles.Blurred.Prompt = promptStyle
	tiStyles.Blurred.Placeholder = placeholderStyle
	tiStyles.Cursor.Color = colAccent
	ti.SetStyles(tiStyles)

	h := help.New()
	h.Styles.ShortKey = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	h.Styles.ShortDesc = lipgloss.NewStyle().Foreground(colMuted)
	h.Styles.ShortSeparator = lipgloss.NewStyle().Foreground(colMuted)
	h.Styles.FullKey = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	h.Styles.FullDesc = lipgloss.NewStyle().Foreground(colMuted)
	h.Styles.FullSeparator = lipgloss.NewStyle().Foreground(colMuted)
	h.ShortSeparator = " · "
	h.FullSeparator = "   "

	return model{
		search:       ti,
		help:         h,
		keys:         newKeyMap(),
		follow:       true,
		mode:         modeNormal,
		currentMatch: -1,
		cmd:          cmd,
	}
}

func (m model) Init() tea.Cmd {
	return textinput.Blink
}

func scheduleTick() tea.Cmd {
	return tea.Tick(renderInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// ensureTick starts the render/toast tick loop if it isn't already running.
// The loop stops itself (tickMsg below) once there's nothing left to do, so
// an idle mnil schedules no wakeups.
func (m *model) ensureTick() tea.Cmd {
	if m.ticking {
		return nil
	}
	m.ticking = true
	return scheduleTick()
}

// setQuery updates the query and its cached lowered form together.
func (m *model) setQuery(q string) {
	m.query = q
	m.queryLower = strings.ToLower(q)
}

// computeDisplay populates l.display from l.raw under current wrap settings.
// When wrap is off, display aliases raw (free — Go strings are immutable).
func (m *model) computeDisplay(l *logLine) {
	if m.wrap && m.viewport.Width() > 0 {
		l.display = ansi.Hardwrap(l.raw, m.viewport.Width(), true)
	} else {
		l.display = l.raw
	}
}

// rebuildAllDisplays recomputes every line's wrapped display. Called on
// wrap toggle and on viewport-width change while wrap is on.
func (m *model) rebuildAllDisplays() {
	for i := range m.lines {
		m.computeDisplay(&m.lines[i])
	}
}

// appendRows appends text's viewport rows (split on wrap-introduced
// newlines) to dst. The single-row case avoids strings.Split's allocation.
func appendRows(dst []string, text string) []string {
	if !strings.Contains(text, "\n") {
		return append(dst, text)
	}
	return append(dst, strings.Split(text, "\n")...)
}

// currentLineIdx returns the logical line index of the current match, or -1.
func (m *model) currentLineIdx() int {
	if m.currentMatch >= 0 && m.currentMatch < len(m.matches) {
		return m.matches[m.currentMatch]
	}
	return -1
}

// ingest appends one raw input line to the buffer, updating search match
// state and viewport rows incrementally. Returns a cmd that keeps the render
// tick alive.
func (m *model) ingest(raw string) tea.Cmd {
	plain := ansi.Strip(raw)
	l := logLine{raw: decorate(raw), plain: plain, lower: strings.ToLower(plain)}
	m.computeDisplay(&l)
	m.lines = append(m.lines, l)

	i := len(m.lines) - 1
	matched := false
	if m.query != "" && strings.Contains(l.lower, m.queryLower) {
		m.matches = append(m.matches, i)
		if m.currentMatch < 0 {
			m.currentMatch = 0
		}
		matched = true
	}
	m.appendLineRows(i, matched)
	m.trimIfNeeded()
	m.dirty = true
	return m.ensureTick()
}

// appendLineRows appends viewport rows for m.lines[i] (which must be the
// last line), mirroring what a full rebuildRows would emit for it.
func (m *model) appendLineRows(i int, matched bool) {
	m.rowOffsets = append(m.rowOffsets, len(m.rows))
	// In filter mode, non-matching lines emit no rows; their rowOffsets entry
	// points at the next visible row so line→row mapping stays on-screen.
	if m.filter && m.query != "" && !matched {
		return
	}
	text := m.lines[i].display
	if matched {
		text = highlightLine(text, m.lines[i].plain, m.query, i == m.currentLineIdx())
	}
	m.rows = appendRows(m.rows, text)
}

// rebuildRows recomputes all viewport rows + rowOffsets from scratch. Called
// when something invalidates every row at once: query/filter/wrap changes,
// width changes while wrapped, and search clears. Steady-state ingest never
// goes through here — new lines append via appendLineRows.
func (m *model) rebuildRows() {
	m.rows = m.rows[:0]
	m.rowOffsets = m.rowOffsets[:0]

	currentLine := m.currentLineIdx()
	// `filter` is only active when there's a query to filter against;
	// otherwise toggling f with no query would blank the buffer.
	filterActive := m.filter && m.query != ""
	var matchSet map[int]struct{}
	if filterActive {
		matchSet = make(map[int]struct{}, len(m.matches))
		for _, idx := range m.matches {
			matchSet[idx] = struct{}{}
		}
	}

	for i := range m.lines {
		m.rowOffsets = append(m.rowOffsets, len(m.rows))
		if filterActive {
			if _, ok := matchSet[i]; !ok {
				continue
			}
		}
		text := m.lines[i].display
		if m.query != "" {
			text = highlightLine(text, m.lines[i].plain, m.query, i == currentLine)
		}
		m.rows = appendRows(m.rows, text)
	}
}

// restyleLine re-renders line i's rows in place. Only valid for changes that
// can't alter the row count — highlighting injects SGR codes only, so the
// current-match hop (tab/shift+tab) qualifies and stays O(1) on huge buffers.
func (m *model) restyleLine(i int, isCurrent bool) {
	if i < 0 || i >= len(m.lines) {
		return
	}
	text := highlightLine(m.lines[i].display, m.lines[i].plain, m.query, isCurrent)
	rows := strings.Split(text, "\n")
	start := m.rowOffsets[i]
	if start+len(rows) > len(m.rows) {
		return
	}
	copy(m.rows[start:], rows)
}

// syncViewport pushes the current rows into the viewport. Interactive code
// paths call this directly; ingest defers to the next tick via the dirty flag.
func (m *model) syncViewport() {
	if !m.ready {
		return
	}
	m.viewport.SetContentLines(m.rows)
	m.dirty = false
}

func (m *model) recomputeMatches() {
	m.matches = m.matches[:0]
	if m.query == "" {
		m.currentMatch = -1
		return
	}
	for i := range m.lines {
		if strings.Contains(m.lines[i].lower, m.queryLower) {
			m.matches = append(m.matches, i)
		}
	}
	if len(m.matches) == 0 {
		m.currentMatch = -1
	} else {
		m.currentMatch = 0
	}
}

// stepMatch moves the current match by delta (wrapping), restyles the two
// affected lines, and scrolls the new current match into view.
func (m *model) stepMatch(delta int) {
	if len(m.matches) == 0 {
		return
	}
	prev := m.matches[m.currentMatch]
	m.currentMatch = (m.currentMatch + delta + len(m.matches)) % len(m.matches)
	cur := m.matches[m.currentMatch]
	if prev != cur {
		m.restyleLine(prev, false)
		m.restyleLine(cur, true)
	}
	m.syncViewport()
	m.scrollToCurrentMatch()
	m.follow = false
}

// scrollToCurrentMatch positions the viewport so the current match line is visible.
func (m *model) scrollToCurrentMatch() {
	line := m.currentLineIdx()
	if line < 0 {
		return
	}
	row := line
	if line < len(m.rowOffsets) {
		row = m.rowOffsets[line]
	}
	h := m.viewport.Height()
	target := row - h/3
	if target < 0 {
		target = 0
	}
	m.viewport.SetYOffset(target)
}

// clearSearch resets all query state, including the input box's text — esc
// from either mode must leave nothing stale behind.
func (m *model) clearSearch() {
	m.search.SetValue("")
	m.setQuery("")
	m.matches = m.matches[:0]
	m.currentMatch = -1
	m.relayout()
	m.rebuildRows()
	m.syncViewport()
}

// showToast sets the ephemeral overlay text. tickMsg clears it after expiry.
func (m *model) showToast(text string) {
	m.toast = text
	m.toastExpiry = time.Now().Add(toastDuration)
}

// trimIfNeeded enforces the buffer cap by dropping the oldest dropChunk
// lines when len(lines) exceeds maxLines. Viewport rows, match indices, the
// current-match pointer, and the scroll position are all shifted to compensate.
func (m *model) trimIfNeeded() {
	if len(m.lines) <= maxLines {
		return
	}
	drop := dropChunk
	if drop > len(m.lines) {
		drop = len(m.lines)
	}
	rowDrop := len(m.rows)
	if drop < len(m.rowOffsets) {
		rowDrop = m.rowOffsets[drop]
	}

	// Reslice — backing arrays stay allocated until append outgrows them;
	// the old entries become unreferenced and collectable after that.
	m.lines = m.lines[drop:]
	m.rows = m.rows[rowDrop:]
	m.rowOffsets = m.rowOffsets[drop:]
	for i := range m.rowOffsets {
		m.rowOffsets[i] -= rowDrop
	}

	if len(m.matches) > 0 {
		droppedMatches := 0
		kept := m.matches[:0]
		for _, idx := range m.matches {
			if idx < drop {
				droppedMatches++
				continue
			}
			kept = append(kept, idx-drop)
		}
		m.matches = kept

		if m.currentMatch >= 0 {
			m.currentMatch -= droppedMatches
			if m.currentMatch < 0 {
				m.currentMatch = 0
			}
			if m.currentMatch >= len(m.matches) {
				m.currentMatch = len(m.matches) - 1
			}
		}
		if len(m.matches) == 0 {
			m.currentMatch = -1
		}
	}

	// Preserve the scroll position for a user who isn't following. rowDrop is
	// exact in viewport rows, so this holds under wrap too.
	if !m.follow && m.ready {
		newOffset := m.viewport.YOffset() - rowDrop
		if newOffset < 0 {
			newOffset = 0
		}
		m.viewport.SetYOffset(newOffset)
	}
}

// saveBuffer writes the current plain-text buffer to a timestamped file in
// the CWD and shows a toast announcing the result.
func (m *model) saveBuffer() tea.Cmd {
	name := fmt.Sprintf("mnil-%s.log", time.Now().Format("2006-01-02-150405"))
	f, err := os.Create(name)
	if err != nil {
		m.showToast(fmt.Sprintf("save failed: %v", err))
		return m.ensureTick()
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	count := len(m.lines)
	for _, l := range m.lines {
		w.WriteString(l.plain)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		m.showToast(fmt.Sprintf("save failed: %v", err))
		return m.ensureTick()
	}
	m.showToast(fmt.Sprintf("saved %d lines → %s", count, name))
	return m.ensureTick()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		oldVpW := 0
		if m.ready {
			oldVpW = m.viewport.Width()
		} else {
			m.viewport = viewport.New(viewport.WithWidth(1), viewport.WithHeight(1))
			m.ready = true
		}
		m.relayout()
		if m.wrap && m.viewport.Width() != oldVpW {
			m.rebuildAllDisplays()
			m.rebuildRows()
		}
		m.syncViewport()
		if m.follow {
			m.viewport.GotoBottom()
		}

	case tickMsg:
		if m.dirty && m.ready {
			m.syncViewport()
			if m.follow {
				m.viewport.GotoBottom()
			}
		}
		if m.toast != "" && time.Now().After(m.toastExpiry) {
			m.toast = ""
		}
		// Keep ticking only while there's pending work; ingest and showToast
		// restart the loop via ensureTick.
		if m.dirty || m.toast != "" {
			return m, scheduleTick()
		}
		m.ticking = false
		return m, nil

	case logLineMsg:
		cmds = append(cmds, m.ingest(string(msg)))

	case logEOFMsg:
		cmds = append(cmds, m.ingest("[mnil] input closed"))

	case cmdExitedMsg:
		note := "[mnil] process exited"
		if msg.err != nil {
			note = fmt.Sprintf("[mnil] process exited: %v", msg.err)
			var ee *exec.ExitError
			if errors.As(msg.err, &ee) && ee.ExitCode() > 0 {
				m.childExit = ee.ExitCode()
			} else {
				m.childExit = 1
			}
		}
		cmds = append(cmds, m.ingest(note))

	case tea.KeyMsg:
		// Esc/backspace dismiss the help popup before any other handling so
		// they don't fall through to clearing the search query.
		if m.help.ShowAll {
			switch msg.String() {
			case "esc", "backspace", "?":
				m.help.ShowAll = false
				return m, nil
			}
		}
		if m.mode == modeSearch {
			switch {
			// ctrl+c must quit here too; in raw mode it arrives as a plain
			// key event, and the textinput below would swallow it. `q` stays
			// typeable, so this checks the literal key rather than keys.Quit.
			case msg.String() == "ctrl+c":
				return m, tea.Quit
			case key.Matches(msg, m.keys.Close):
				m.mode = modeNormal
				m.search.Blur()
				return m, nil
			case key.Matches(msg, m.keys.Clear):
				m.mode = modeNormal
				m.search.Blur()
				m.clearSearch()
				return m, nil
			case key.Matches(msg, m.keys.Next):
				m.stepMatch(1)
				return m, nil
			case key.Matches(msg, m.keys.Prev):
				m.stepMatch(-1)
				return m, nil
			}
			// Note: Help (?) is intentionally not handled here so it can be
			// typed into the search query; the binding is dropped from the
			// search-mode help below.

			prev := m.search.Value()
			var cmd tea.Cmd
			m.search, cmd = m.search.Update(msg)
			if v := m.search.Value(); v != prev {
				prevEmpty := m.query == ""
				m.setQuery(v)
				if prevEmpty != (v == "") {
					m.relayout()
				}
				m.recomputeMatches()
				if v != "" {
					m.follow = false
				}
				m.rebuildRows()
				m.syncViewport()
				m.scrollToCurrentMatch()
			}
			cmds = append(cmds, cmd)
			return m, tea.Batch(cmds...)
		}

		switch {
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, m.keys.Search):
			m.mode = modeSearch
			m.search.Focus()
			return m, textinput.Blink
		case key.Matches(msg, m.keys.Next):
			m.stepMatch(1)
			return m, nil
		case key.Matches(msg, m.keys.Prev):
			m.stepMatch(-1)
			return m, nil
		case key.Matches(msg, m.keys.Bottom):
			m.follow = true
			m.viewport.GotoBottom()
			return m, nil
		case key.Matches(msg, m.keys.Top):
			m.follow = false
			m.viewport.GotoTop()
			return m, nil
		case key.Matches(msg, m.keys.Follow):
			m.follow = !m.follow
			if m.follow {
				m.viewport.GotoBottom()
			}
			return m, nil
		case key.Matches(msg, m.keys.Filter):
			m.filter = !m.filter
			m.rebuildRows()
			m.syncViewport()
			if m.follow {
				m.viewport.GotoBottom()
			} else {
				m.scrollToCurrentMatch()
			}
			return m, nil
		case key.Matches(msg, m.keys.Wrap):
			m.wrap = !m.wrap
			m.relayout()
			m.rebuildAllDisplays()
			m.rebuildRows()
			m.syncViewport()
			if m.follow {
				m.viewport.GotoBottom()
			} else {
				m.scrollToCurrentMatch()
			}
			return m, nil
		case key.Matches(msg, m.keys.Save):
			return m, m.saveBuffer()
		case key.Matches(msg, m.keys.Help):
			m.help.ShowAll = !m.help.ShowAll
			m.relayout()
			return m, nil
		case key.Matches(msg, m.keys.Clear):
			if m.query != "" {
				m.clearSearch()
			}
			return m, nil
		}
	}

	if m.ready {
		var cmd tea.Cmd
		prevOffset := m.viewport.YOffset()
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
		// If the user scrolled up manually, disable follow.
		if m.viewport.YOffset() != prevOffset && m.viewport.YOffset() < m.viewport.TotalLineCount()-m.viewport.Height() {
			m.follow = false
		}
	}

	return m, tea.Batch(cmds...)
}
