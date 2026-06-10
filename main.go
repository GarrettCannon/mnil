package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"

	tea "charm.land/bubbletea/v2"
)

// stdinEOF is set when the piped stdin reader hits EOF. Read after the TUI
// exits to decide whether the upstream producer is still alive: if EOF fired,
// it's already gone (cat/ls/etc) and no kill is needed; if not, it's still
// running (ping/tail -F/etc) and we pgrp-kill it on shutdown. Atomic because
// EOF can race a quit keypress; routing it through a message instead would
// reintroduce that race (a quit processed before the EOF message lands).
var stdinEOF atomic.Bool

// version is overridable at build time via:
//
//	go build -ldflags "-X main.version=v0.1.0"
var version = "dev"

// maxLineBytes caps how much of a single newline-free input line accumulates
// before it's flushed as its own buffer line, so a pathological producer
// (minified JS, binary garbage) can't grow memory without bound.
const maxLineBytes = 1024 * 1024

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
  f            toggle filter (show only matching lines, requires a query)
  F            toggle follow (auto-scroll to new lines)
  g / G        jump to top / bottom
  w            toggle line wrap
  s            save buffer to a timestamped .log file in CWD
  ?            toggle full keybinding help
  q, ctrl+c    quit
`)
}

// readLines streams lines from r into the bubbletea program. Returns when r
// is exhausted. Lines longer than maxLineBytes are flushed in chunks instead
// of aborting the stream (bufio.Scanner would silently stop at ErrTooLong),
// and read errors are surfaced as a notice line rather than dropped.
func readLines(p *tea.Program, r io.Reader) {
	br := bufio.NewReaderSize(r, 64*1024)
	var line []byte
	for {
		chunk, isPrefix, err := br.ReadLine()
		line = append(line, chunk...)
		if err != nil {
			if len(line) > 0 {
				p.Send(logLineMsg(string(line)))
			}
			if err != io.EOF {
				p.Send(logLineMsg(fmt.Sprintf("[mnil] read error: %v", err)))
			}
			return
		}
		if isPrefix {
			if len(line) >= maxLineBytes {
				p.Send(logLineMsg(string(line)))
				line = line[:0]
			}
			continue
		}
		p.Send(logLineMsg(string(line)))
		line = line[:0]
	}
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
		// AltScreen is set on the View struct returned from View(). No mouse
		// capture so the terminal handles native click-drag text selection.
	}

	var (
		child     *exec.Cmd
		childRead io.Reader
		cmdString string
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
			p.Send(cmdExitedMsg{err: child.Wait()})
		}()
	case stdinPiped:
		go func() {
			readLines(p, os.Stdin)
			stdinEOF.Store(true)
			p.Send(logEOFMsg{})
		}()
	}

	exitCode := 0
	finalModel, err := p.Run()
	if err != nil {
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
	// shares our pgrp — closing the user's terminal. In spawn mode stdin is
	// never read, so the EOF flag means nothing there — skip entirely.
	if child == nil && stdinPiped && !stdinEOF.Load() {
		signal.Ignore(syscall.SIGTERM)
		_ = syscall.Kill(0, syscall.SIGTERM)
	}
	// Spawn mode: if the child already exited with a failure, pass its exit
	// code through so mnil is script-safe. The code arrives on the final
	// model via cmdExitedMsg; a message processed by the program
	// happened-before Run returned, so this read is race-free.
	if fm, ok := finalModel.(model); ok && exitCode == 0 {
		exitCode = fm.childExit
	}
	os.Exit(exitCode)
}
