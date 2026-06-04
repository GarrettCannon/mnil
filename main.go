package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type logLine struct {
	raw     string // decorated bytes, ANSI codes preserved, never wrapped
	plain   string // ANSI-stripped, used for search
	display string // raw with current wrap applied (== raw when wrap is off)
}

type logLineMsg string
type logEOFMsg struct{}
type tickMsg time.Time
type cmdExitedMsg struct{ err error }

// stdinDone is set when the piped stdin reader hits EOF. Read after the TUI
// exits to decide whether the upstream producer is still alive: if EOF fired,
// it's already gone (cat/ls/etc) and no kill is needed; if not, it's still
// running (ping/tail -F/etc) and we pgrp-kill it on shutdown. Written from
// the reader goroutine, read after p.Run() returns — no race.
var stdinDone bool

const renderInterval = 33 * time.Millisecond // ~30Hz

type mode int

const (
	modeNormal mode = iota
	modeSearch
)

type keyMap struct {
	Search key.Binding
	Next   key.Binding
	Prev   key.Binding
	Follow key.Binding
	Top    key.Binding
	Bottom key.Binding
	Wrap   key.Binding
	Save   key.Binding
	Clear  key.Binding
	Close  key.Binding
	Help   key.Binding
	Quit   key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		Search: key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "search")),
		Next:   key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next match")),
		Prev:   key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("⇧tab", "prev match")),
		Follow: key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "follow")),
		Top:    key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "top")),
		Bottom: key.NewBinding(key.WithKeys("G"), key.WithHelp("G", "bottom")),
		Wrap:   key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "wrap")),
		Save:   key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "save")),
		Clear:  key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "clear")),
		Close:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("↵", "close search")),
		Help:   key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:   key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

// Mode-specific help projections of the same keymap.
type normalKeys keyMap

func (k normalKeys) ShortHelp() []key.Binding {
	return []key.Binding{k.Search, k.Next, k.Follow, k.Wrap, k.Save, k.Help, k.Quit}
}
func (k normalKeys) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Search, k.Next, k.Prev},
		{k.Follow, k.Top, k.Bottom},
		{k.Wrap, k.Save, k.Help, k.Quit},
	}
}

type searchKeys keyMap

func (k searchKeys) ShortHelp() []key.Binding {
	return []key.Binding{k.Next, k.Prev, k.Clear, k.Close, k.Help}
}
func (k searchKeys) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Next, k.Prev},
		{k.Clear, k.Close, k.Help},
	}
}

type model struct {
	viewport     viewport.Model
	search       textinput.Model
	help         help.Model
	keys         keyMap
	lines        []logLine
	mode         mode
	query        string
	matches      []int // indices into lines
	currentMatch int
	follow       bool
	wrap         bool
	rowOffsets   []int // viewport row index of each logical line's first wrapped row
	ready        bool
	dirty        bool // ingest accumulated, waiting for next tick to render
	cmd          string // command string when mnil is running a child process
	width        int
	height       int
}

var (
	// Palette uses ANSI 16-color indices so the user's terminal theme picks
	// the actual colors (Solarized, Dracula, Nord, etc.). Text foregrounds
	// without an explicit color fall back to the terminal default.
	colBrand   = lipgloss.Color("5")  // magenta (purple-ish in most themes)
	colAccent  = lipgloss.Color("13") // bright magenta
	colSuccess = lipgloss.Color("2")  // green
	colChipBG  = lipgloss.Color("8")  // bright black (dark gray)
	colMuted   = lipgloss.Color("8")  // bright black
	colOnChip  = lipgloss.Color("0")  // black, for text on a colored chip

	brandChip = lipgloss.NewStyle().
			Background(colBrand).
			Foreground(colOnChip).
			Bold(true).
			Padding(0, 1)

	infoChip = lipgloss.NewStyle().
			Background(colChipBG).
			Padding(0, 1)

	matchChip = lipgloss.NewStyle().
			Background(colAccent).
			Foreground(colOnChip).
			Bold(true).
			Padding(0, 1)

	followChip = lipgloss.NewStyle().
			Background(colSuccess).
			Foreground(colOnChip).
			Bold(true).
			Padding(0, 1)

	// Bordered sections.
	viewportBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
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
)

// Background-only SGR codes. Using only bg/bg-reset means foreground colors
// emitted by the source process survive across the highlighted span.
// Bg colors picked from ANSI indices that aren't used as a foreground by any
// termRule — keeps highlights distinct even when the matched text is already
// colored (e.g. searching POST, which decorate() renders as magenta fg).
const (
	matchBG   = "\x1b[44m" // blue bg (ANSI 4)
	currentBG = "\x1b[42m" // green bg (ANSI 2)
	bgReset   = "\x1b[49m"
)

// Buffer cap. When len(lines) exceeds maxLines, drop the oldest dropChunk
// lines in one batch so the trim cost amortizes across many incoming lines.
const (
	maxLines  = 100_000
	dropChunk = 10_000
)

// Term highlights applied to lines the source did NOT already color. Each
// matched substring is wrapped with the given SGR prefix and a full reset.
type termRule struct {
	re  *regexp.Regexp
	sgr string
}

var termRules = []termRule{
	{regexp.MustCompile(`(?i)\b(ERROR|ERR|FATAL|PANIC|FAIL(?:ED)?)\b`), "\x1b[1;31m"}, // bold red
	{regexp.MustCompile(`(?i)\b(WARN(?:ING)?)\b`), "\x1b[1;33m"},                       // bold yellow
	{regexp.MustCompile(`(?i)\b(INFO|NOTICE)\b`), "\x1b[1;36m"},                        // bold cyan
	{regexp.MustCompile(`(?i)\b(DEBUG|TRACE)\b`), "\x1b[2;37m"},                        // dim gray
	{regexp.MustCompile(`\b(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\b`), "\x1b[1;35m"}, // bold magenta
	{regexp.MustCompile(`\bhttps?://[^\s'"]+`), "\x1b[4;36m"},                          // underline cyan URLs
	{regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}\b`), "\x1b[35m"},                      // magenta IPv4
}

// decorate adds term-based color to plain log lines. If the source already
// emitted ANSI codes (next dev, ls --color, etc.) we leave the line alone so
// our resets don't clobber the source's foreground state.
func decorate(raw string) string {
	if strings.Contains(raw, "\x1b[") {
		return raw
	}
	out := raw
	for _, r := range termRules {
		out = r.re.ReplaceAllStringFunc(out, func(s string) string {
			return r.sgr + s + "\x1b[0m"
		})
	}
	return out
}

func initialModel(cmd string) model {
	ti := textinput.New()
	ti.Placeholder = "search..."
	ti.Prompt = "  "
	ti.CharLimit = 256
	ti.PromptStyle = lipgloss.NewStyle().Foreground(colBrand).Bold(true)
	ti.PlaceholderStyle = lipgloss.NewStyle().Foreground(colMuted).Italic(true)
	ti.Cursor.Style = lipgloss.NewStyle().Foreground(colAccent)

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
		search: ti,
		help:   h,
		keys:   newKeyMap(),
		follow: true,
		mode:   modeNormal,
		cmd:    cmd,
	}
}

// activeKeyMap picks the per-mode key projection for help rendering.
func (m model) activeKeyMap() help.KeyMap {
	if m.mode == modeSearch {
		return searchKeys(m.keys)
	}
	return normalKeys(m.keys)
}

// readLines streams lines from r into the bubbletea program. Returns when r
// hits EOF.
func readLines(p *tea.Program, r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		p.Send(logLineMsg(scanner.Text()))
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, scheduleTick())
}

func scheduleTick() tea.Cmd {
	return tea.Tick(renderInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *model) renderLines() string {
	m.rowOffsets = m.rowOffsets[:0]
	if len(m.lines) == 0 {
		return ""
	}

	var b strings.Builder
	row := 0

	if m.query == "" {
		for i, l := range m.lines {
			m.rowOffsets = append(m.rowOffsets, row)
			if i > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(l.display)
			row += strings.Count(l.display, "\n") + 1
		}
		return b.String()
	}

	currentLine := -1
	if m.currentMatch >= 0 && m.currentMatch < len(m.matches) {
		currentLine = m.matches[m.currentMatch]
	}
	for i, l := range m.lines {
		m.rowOffsets = append(m.rowOffsets, row)
		text := highlightLine(l.display, l.plain, m.query, i == currentLine)
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(text)
		row += strings.Count(text, "\n") + 1
	}
	return b.String()
}

// highlightLine walks the display bytes ANSI-aware. Match offsets are
// computed in the stripped plaintext, then mapped back to display byte
// offsets so we can inject bg-only SGR codes without breaking any color
// sequences. Newlines in display (introduced by wrap) don't advance plain
// position and trigger a temporary close/reopen of the highlight bg so
// color doesn't bleed across the wrapped row break.
func highlightLine(display, plain, query string, isCurrent bool) string {
	if query == "" {
		return display
	}
	lq := strings.ToLower(query)
	lp := strings.ToLower(plain)
	type span struct{ start, end int }
	var matches []span
	i := 0
	for i <= len(lp) {
		idx := strings.Index(lp[i:], lq)
		if idx < 0 {
			break
		}
		matches = append(matches, span{i + idx, i + idx + len(query)})
		i += idx + len(query)
	}
	if len(matches) == 0 {
		return display
	}

	bg := matchBG
	if isCurrent {
		bg = currentBG
	}

	var b strings.Builder
	raw := display
	plainPos := 0
	matchIdx := 0
	inHL := false

	openIfNeeded := func() {
		if matchIdx < len(matches) && !inHL && plainPos == matches[matchIdx].start {
			b.WriteString(bg)
			inHL = true
		}
	}
	closeIfNeeded := func() {
		if inHL && matchIdx < len(matches) && plainPos == matches[matchIdx].end {
			b.WriteString(bgReset)
			inHL = false
			matchIdx++
		}
	}

	j := 0
	for j < len(raw) {
		// ANSI escape sequence: pass through verbatim, then if it was a
		// full reset and we're inside a highlight, re-apply our bg.
		if raw[j] == 0x1b && j+1 < len(raw) {
			k := ansiSeqEnd(raw, j)
			seq := raw[j:k]
			b.WriteString(seq)
			if inHL && isResetSGR(seq) {
				b.WriteString(bg)
			}
			j = k
			continue
		}

		// Wrap-introduced newline: doesn't advance plain text position.
		// Briefly close the highlight bg so it doesn't bleed across the
		// row break, then reopen on the next row if we're still mid-match.
		if raw[j] == '\n' {
			if inHL {
				b.WriteString(bgReset)
				b.WriteByte('\n')
				b.WriteString(bg)
			} else {
				b.WriteByte('\n')
			}
			j++
			continue
		}

		// Close before writing the byte at the end-of-match boundary.
		closeIfNeeded()
		openIfNeeded()

		b.WriteByte(raw[j])
		plainPos++
		j++

		// Handle zero-width transitions at end-of-line boundary (match
		// ending exactly at len(plain)).
		closeIfNeeded()
	}
	if inHL {
		b.WriteString(bgReset)
	}
	return b.String()
}

// ansiSeqEnd returns the byte index one past the end of the ANSI escape
// sequence starting at i. Handles CSI (ESC [), OSC (ESC ]), and simple
// two-byte ESC sequences.
func ansiSeqEnd(s string, i int) int {
	if i+1 >= len(s) {
		return i + 1
	}
	switch s[i+1] {
	case '[': // CSI: terminated by a byte in 0x40-0x7e
		k := i + 2
		for k < len(s) {
			c := s[k]
			k++
			if c >= 0x40 && c <= 0x7e {
				return k
			}
		}
		return k
	case ']': // OSC: terminated by BEL or ST (ESC \)
		k := i + 2
		for k < len(s) {
			if s[k] == 0x07 {
				return k + 1
			}
			if s[k] == 0x1b && k+1 < len(s) && s[k+1] == '\\' {
				return k + 2
			}
			k++
		}
		return k
	default:
		return i + 2
	}
}

// isResetSGR reports whether seq is an SGR (ends with 'm') containing a 0
// parameter, which resets all attributes including foreground/background.
func isResetSGR(seq string) bool {
	if len(seq) < 3 || seq[len(seq)-1] != 'm' || seq[1] != '[' {
		return false
	}
	params := seq[2 : len(seq)-1]
	if params == "" {
		return true
	}
	for _, p := range strings.Split(params, ";") {
		if p == "" || p == "0" || p == "00" {
			return true
		}
	}
	return false
}

func (m *model) recomputeMatches() {
	m.matches = m.matches[:0]
	if m.query == "" {
		m.currentMatch = -1
		return
	}
	lq := strings.ToLower(m.query)
	for i, l := range m.lines {
		if strings.Contains(strings.ToLower(l.plain), lq) {
			m.matches = append(m.matches, i)
		}
	}
	if len(m.matches) == 0 {
		m.currentMatch = -1
	} else {
		m.currentMatch = 0
	}
}

func (m *model) gotoNextMatch() {
	if len(m.matches) == 0 {
		return
	}
	m.currentMatch = (m.currentMatch + 1) % len(m.matches)
	m.renderNow()
	m.scrollToCurrentMatch()
	m.follow = false
}

func (m *model) gotoPrevMatch() {
	if len(m.matches) == 0 {
		return
	}
	m.currentMatch = (m.currentMatch - 1 + len(m.matches)) % len(m.matches)
	m.renderNow()
	m.scrollToCurrentMatch()
	m.follow = false
}

// scrollToCurrentMatch positions the viewport so the current match line is visible.
func (m *model) scrollToCurrentMatch() {
	if m.currentMatch < 0 || m.currentMatch >= len(m.matches) {
		return
	}
	line := m.matches[m.currentMatch]
	row := line
	if line < len(m.rowOffsets) {
		row = m.rowOffsets[line]
	}
	h := m.viewport.Height
	target := row - h/3
	if target < 0 {
		target = 0
	}
	m.viewport.SetYOffset(target)
}

// computeDisplay populates l.display from l.raw under current wrap settings.
// When wrap is off, display aliases raw (free — Go strings are immutable).
func (m *model) computeDisplay(l *logLine) {
	if m.wrap && m.viewport.Width > 0 {
		l.display = ansi.Hardwrap(l.raw, m.viewport.Width, true)
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

// renderNow synchronously rebuilds viewport content from the cache.
// Used by interactive code paths; ingest defers via the dirty flag.
func (m *model) renderNow() {
	if !m.ready {
		return
	}
	m.viewport.SetContent(m.renderLines())
	m.dirty = false
}

// appendNotice adds an mnil-generated status line to the buffer and updates
// search match state incrementally (mirroring the logLineMsg ingest path).
func (m *model) appendNotice(text string) {
	l := logLine{raw: decorate(text), plain: text}
	m.computeDisplay(&l)
	m.lines = append(m.lines, l)
	if m.query != "" && strings.Contains(strings.ToLower(text), strings.ToLower(m.query)) {
		m.matches = append(m.matches, len(m.lines)-1)
		if m.currentMatch < 0 {
			m.currentMatch = 0
		}
	}
	m.trimIfNeeded()
	m.renderNow()
}

// trimIfNeeded enforces the buffer cap by dropping the oldest dropChunk
// lines when len(lines) exceeds maxLines. Match indices, the current-match
// pointer, and the viewport scroll position are all shifted to compensate.
func (m *model) trimIfNeeded() {
	if len(m.lines) <= maxLines {
		return
	}
	drop := dropChunk
	if drop > len(m.lines) {
		drop = len(m.lines)
	}

	// Reslice — backing array stays allocated until GC; the old logLines
	// become unreferenced and collectable.
	m.lines = m.lines[drop:]

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

	// Best-effort scroll preservation for a user who isn't following.
	// In wrap mode this approximates (drop is in logical lines, YOffset is
	// in viewport rows), but the next render fixes the visible content.
	if !m.follow && m.ready {
		newOffset := m.viewport.YOffset - drop
		if newOffset < 0 {
			newOffset = 0
		}
		m.viewport.SetYOffset(newOffset)
	}
}

// saveBuffer writes the current plain-text buffer to a timestamped file in
// the CWD and adds a notice line announcing the result.
func (m *model) saveBuffer() {
	name := fmt.Sprintf("mnil-%s.log", time.Now().Format("2006-01-02-150405"))
	f, err := os.Create(name)
	if err != nil {
		m.appendNotice(fmt.Sprintf("[mnil] save failed: %v", err))
		return
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	count := len(m.lines)
	for _, l := range m.lines {
		w.WriteString(l.plain)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		m.appendNotice(fmt.Sprintf("[mnil] save failed: %v", err))
		return
	}
	m.appendNotice(fmt.Sprintf("[mnil] saved %d lines to %s", count, name))
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		oldVpW := 0
		if m.ready {
			oldVpW = m.viewport.Width
		} else {
			m.viewport = viewport.New(1, 1)
			m.ready = true
		}
		m.relayout()
		if m.wrap && m.viewport.Width != oldVpW {
			m.rebuildAllDisplays()
		}
		m.renderNow()
		if m.follow {
			m.viewport.GotoBottom()
		}

	case tickMsg:
		if m.dirty && m.ready {
			m.renderNow()
			if m.follow {
				m.viewport.GotoBottom()
			}
		}
		return m, scheduleTick()

	case logLineMsg:
		raw := string(msg)
		l := logLine{raw: decorate(raw), plain: ansi.Strip(raw)}
		m.computeDisplay(&l)
		m.lines = append(m.lines, l)
		if m.query != "" {
			if strings.Contains(strings.ToLower(l.plain), strings.ToLower(m.query)) {
				m.matches = append(m.matches, len(m.lines)-1)
				if m.currentMatch < 0 {
					m.currentMatch = 0
				}
			}
		}
		m.trimIfNeeded()
		m.dirty = true

	case logEOFMsg:
		// stdin closed; nothing special for now

	case cmdExitedMsg:
		note := "[mnil] process exited"
		if msg.err != nil {
			note = fmt.Sprintf("[mnil] process exited: %v", msg.err)
		}
		m.appendNotice(note)

	case tea.KeyMsg:
		if m.mode == modeSearch {
			switch {
			case key.Matches(msg, m.keys.Close):
				m.mode = modeNormal
				m.search.Blur()
				return m, nil
			case key.Matches(msg, m.keys.Clear):
				m.mode = modeNormal
				m.search.Blur()
				m.search.SetValue("")
				m.query = ""
				m.matches = nil
				m.currentMatch = -1
				m.renderNow()
				return m, nil
			case key.Matches(msg, m.keys.Next):
				m.gotoNextMatch()
				return m, nil
			case key.Matches(msg, m.keys.Prev):
				m.gotoPrevMatch()
				return m, nil
			case key.Matches(msg, m.keys.Help):
				m.help.ShowAll = !m.help.ShowAll
				m.relayout()
				m.renderNow()
				return m, nil
			}

			prev := m.search.Value()
			var cmd tea.Cmd
			m.search, cmd = m.search.Update(msg)
			if m.search.Value() != prev {
				m.query = m.search.Value()
				m.recomputeMatches()
				if m.query != "" {
					m.follow = false
				}
				m.renderNow()
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
			m.gotoNextMatch()
			return m, nil
		case key.Matches(msg, m.keys.Prev):
			m.gotoPrevMatch()
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
		case key.Matches(msg, m.keys.Wrap):
			m.wrap = !m.wrap
			m.rebuildAllDisplays()
			wasFollowing := m.follow
			m.renderNow()
			if wasFollowing {
				m.viewport.GotoBottom()
			} else {
				m.scrollToCurrentMatch()
			}
			return m, nil
		case key.Matches(msg, m.keys.Save):
			m.saveBuffer()
			if m.follow {
				m.viewport.GotoBottom()
			}
			return m, nil
		case key.Matches(msg, m.keys.Help):
			m.help.ShowAll = !m.help.ShowAll
			m.relayout()
			return m, nil
		case key.Matches(msg, m.keys.Clear):
			if m.query != "" {
				m.query = ""
				m.matches = nil
				m.currentMatch = -1
				m.renderNow()
			}
			return m, nil
		}
	}

	if m.ready {
		var cmd tea.Cmd
		prevOffset := m.viewport.YOffset
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
		// If the user scrolled up manually, disable follow.
		if m.viewport.YOffset != prevOffset && m.viewport.YOffset < m.viewport.TotalLineCount()-m.viewport.Height {
			m.follow = false
		}
	}

	return m, tea.Batch(cmds...)
}

// relayout recomputes child component sizes from the current window dims and
// help-expanded state. Called from WindowSizeMsg and when ? is toggled.
func (m *model) relayout() {
	if m.width == 0 || m.height == 0 {
		return
	}

	inputOuterW := m.width / 2
	helpOuterW := m.width - inputOuterW

	// Help content width drives word-wrap in full mode.
	helpInnerW := helpOuterW - 4 // 2 borders + 2 padding
	if helpInnerW < 10 {
		helpInnerW = 10
	}
	m.help.Width = helpInnerW

	// Measure the help view (1 line in short mode, multi-line in full mode).
	helpInnerH := lipgloss.Height(m.help.View(m.activeKeyMap()))
	if helpInnerH < 1 {
		helpInnerH = 1
	}
	bottomH := helpInnerH + 2 // top + bottom border

	const titleH, vpBorders = 1, 2
	vpW := m.width - 2
	vpH := m.height - titleH - vpBorders - bottomH
	if vpW < 1 {
		vpW = 1
	}
	if vpH < 1 {
		vpH = 1
	}
	m.viewport.Width = vpW
	m.viewport.Height = vpH

	inputInnerW := inputOuterW - 4
	if inputInnerW < 10 {
		inputInnerW = 10
	}
	m.search.Width = inputInnerW - 2
}

func (m model) View() string {
	if !m.ready {
		return "loading..."
	}

	title := m.titleBar()
	viewportView := viewportBox.Render(m.viewport.View())

	inputOuterW := m.width / 2
	helpOuterW := m.width - inputOuterW

	helpContent := m.help.View(m.activeKeyMap())
	contentH := lipgloss.Height(helpContent)

	style := inputBox
	if m.mode == modeSearch {
		style = inputBoxFocused
	}
	inputView := style.Width(inputOuterW - 2).Height(contentH).Render(m.search.View())
	helpView := helpBox.Width(helpOuterW - 2).Render(helpContent)
	bottom := lipgloss.JoinHorizontal(lipgloss.Top, inputView, helpView)

	return title + "\n" + viewportView + "\n" + bottom
}

func truncate(s string, max int) string {
	if max <= 1 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

func (m model) titleBar() string {
	left := brandChip.Render("mnil")
	if m.cmd != "" {
		maxCmd := m.width / 3
		if maxCmd > 60 {
			maxCmd = 60
		}
		if maxCmd < 12 {
			maxCmd = 12
		}
		left += infoChip.Render(truncate(m.cmd, maxCmd))
	}
	left += infoChip.Render(fmt.Sprintf("%d lines", len(m.lines)))
	if m.query != "" {
		if len(m.matches) == 0 {
			left += matchChip.Render(fmt.Sprintf("/%s · no matches", m.query))
		} else {
			left += matchChip.Render(fmt.Sprintf("/%s · %d/%d", m.query, m.currentMatch+1, len(m.matches)))
		}
	}

	var right string
	if m.wrap {
		right += infoChip.Render("WRAP")
	}
	if m.follow {
		right += followChip.Render("● FOLLOW")
	}

	pad := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 0 {
		pad = 0
	}
	return left + strings.Repeat(" ", pad) + right
}


// version is overridable at build time via:
//   go build -ldflags "-X main.version=v0.1.0"
var version = "dev"

func usage() {
	fmt.Fprint(os.Stderr, `mnil — a TUI log viewer.

Usage:
  mnil <command>       run command and tail its output (stdout + stderr)
  <command> | mnil     read piped output (stdout only)

Flags:
  -version    print version and exit
  -h, -help   print this help

Examples:
  mnil "npm run dev"
  mnil "ping 8.8.8.8"
  find / 2>&1 | mnil
  tail -F /var/log/system.log | mnil

Keys (in-app):
  /            open search (live: matches highlight as you type)
  tab / S+tab  next / previous match
  esc          clear search
  f            toggle follow (auto-scroll to new lines)
  g / G        jump to top / bottom
  w            toggle line wrap
  s            save buffer to a timestamped .log file in CWD
  ?            toggle full keybinding help
  q, ctrl+c    quit
`)
}

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Println("mnil", version)
		return
	}

	// Determine input source: spawned command or piped stdin.
	stdinPiped := false
	if fi, err := os.Stdin.Stat(); err == nil && (fi.Mode()&os.ModeCharDevice) == 0 {
		stdinPiped = true
	}
	if flag.NArg() == 0 && !stdinPiped {
		fmt.Fprint(os.Stderr, "mnil: no input. Run a command or pipe something in:\n  mnil \"npm run dev\"\n  ping 8.8.8.8 | mnil\nRun `mnil -h` for help.\n")
		os.Exit(2)
	}

	tty, err := os.Open("/dev/tty")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mnil: cannot open /dev/tty:", err)
		os.Exit(1)
	}
	opts := []tea.ProgramOption{
		tea.WithInput(tty),
		tea.WithAltScreen(),
		// No mouse capture: lets the terminal handle native click-drag
		// selection of log text. Scroll/nav uses the keyboard (arrows,
		// pgup/pgdn, g/G).
	}

	var (
		child      *exec.Cmd
		childRead  io.Reader
		cmdString  string
	)

	if flag.NArg() > 0 {
		cmdString = strings.Join(flag.Args(), " ")
		c := exec.Command("sh", "-c", cmdString)
		// Hint colorized output even though stdout isn't a TTY. Most tools
		// honor these; the ones that don't fall back gracefully to plain text.
		c.Env = append(os.Environ(),
			"FORCE_COLOR=1",
			"CLICOLOR_FORCE=1",
			"PYTHONUNBUFFERED=1",
		)
		pr, pw, err := os.Pipe()
		if err != nil {
			fmt.Fprintln(os.Stderr, "mnil: pipe:", err)
			os.Exit(1)
		}
		c.Stdout = pw
		c.Stderr = pw
		// Put the child in its own pgrp so we can SIGTERM the whole tree
		// (npm → node → next workers) on quit, not just `sh -c`.
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := c.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "mnil: failed to start command:", err)
			os.Exit(1)
		}
		// Parent closes write end so EOF propagates to the reader after the
		// child exits.
		pw.Close()
		child = c
		childRead = pr
	}

	p := tea.NewProgram(initialModel(cmdString), opts...)

	switch {
	case child != nil:
		go func() {
			readLines(p, childRead)
			err := child.Wait()
			p.Send(cmdExitedMsg{err: err})
		}()
	case stdinPiped:
		go func() {
			readLines(p, os.Stdin)
			stdinDone = true
			p.Send(logEOFMsg{})
		}()
	}

	exitCode := 0
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "mnil error:", err)
		exitCode = 1
	}
	if child != nil && child.Process != nil {
		// Negative pid signals the whole pgrp we created with Setpgid above.
		_ = syscall.Kill(-child.Process.Pid, syscall.SIGTERM)
	}
	// Piped stdin: only pgrp-kill if the upstream is still alive. If the
	// reader saw EOF, the producer has already exited (cat/ls/find) and a
	// pgrp blast risks taking down the parent shell in setups where it
	// shares our pgrp — closing the user's terminal.
	if stdinPiped && !stdinDone {
		signal.Ignore(syscall.SIGTERM)
		_ = syscall.Kill(0, syscall.SIGTERM)
	}
	os.Exit(exitCode)
}
