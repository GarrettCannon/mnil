package main

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
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

// Term highlights applied to lines the source did NOT already color. Each
// matched substring is wrapped with the given SGR prefix and a full reset.
type termRule struct {
	re  *regexp.Regexp
	sgr string
}

var termRules = []termRule{
	{regexp.MustCompile(`(?i)\b(ERROR|ERR|FATAL|PANIC|FAIL(?:ED)?)\b`), "\x1b[1;31m"},  // bold red
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

// span is a half-open byte-offset range [start, end) into a plain string.
type span struct{ start, end int }

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// matchSpans returns the byte-offset spans in plain of non-overlapping
// case-insensitive occurrences of query. For ASCII text lowercasing preserves
// byte offsets, so the search runs directly on a lowered copy. Otherwise
// offsets are mapped back through a byte-offset table, since lowercasing can
// change a rune's byte length (İ→i, K→k).
func matchSpans(plain, query string) []span {
	if query == "" || plain == "" {
		return nil
	}
	lq := strings.ToLower(query)
	if isASCII(plain) {
		lp := strings.ToLower(plain)
		var spans []span
		i := 0
		for {
			idx := strings.Index(lp[i:], lq)
			if idx < 0 {
				break
			}
			start := i + idx
			spans = append(spans, span{start, start + len(lq)})
			i = start + len(lq)
		}
		return spans
	}
	return matchSpansMapped(plain, lq)
}

// matchSpansMapped is the non-ASCII slow path: lowercase plain rune by rune,
// recording each lowered byte's offset in the original string, find matches
// in the lowered text, then map the spans back to original byte offsets.
func matchSpansMapped(plain, lq string) []span {
	var b strings.Builder
	b.Grow(len(plain))
	offs := make([]int, 0, len(plain)+1)
	for i, r := range plain {
		lr := unicode.ToLower(r)
		for n := utf8.RuneLen(lr); n > 0; n-- {
			offs = append(offs, i)
		}
		b.WriteRune(lr)
	}
	offs = append(offs, len(plain))
	lp := b.String()

	var spans []span
	i := 0
	for {
		idx := strings.Index(lp[i:], lq)
		if idx < 0 {
			break
		}
		start := i + idx
		end := start + len(lq)
		spans = append(spans, span{offs[start], offs[end]})
		i = end
	}
	return spans
}

// highlightLine walks the display bytes ANSI-aware. Match offsets are
// computed in the stripped plaintext, then mapped back to display byte
// offsets so we can inject bg-only SGR codes without breaking any color
// sequences. Newlines in display (introduced by wrap) don't advance plain
// position and trigger a temporary close/reopen of the highlight bg so
// color doesn't bleed across the wrapped row break.
func highlightLine(display, plain, query string, isCurrent bool) string {
	matches := matchSpans(plain, query)
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
