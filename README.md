# mnil

**M**y **N**ame **I**s **L**og — a small TUI log viewer that tails piped stdin.

Pipe anything into it. Search live as you type, jump between matches, toggle
follow, wrap long lines, save the buffer to a file. ANSI colors from the
producing tool pass through; plain text gets common log tokens
(`ERROR`/`WARN`/`INFO`/URLs/IPs) auto-colored.

```
ping 8.8.8.8 | mnil
find / 2>&1   | mnil
tail -F /var/log/system.log | mnil
```

Built with [Bubble Tea](https://github.com/charmbracelet/bubbletea),
[Bubbles](https://github.com/charmbracelet/bubbles), and
[Lip Gloss](https://github.com/charmbracelet/lipgloss).

## Install

### Homebrew

```sh
brew tap GarrettCannon/mnil
brew install mnil
```

### From source

Requires Go 1.21 or newer.

```sh
go install github.com/GarrettCannon/mnil@latest
```

## Usage

```
<command> | mnil
```

mnil reads lines from stdin and renders them in a scrollable viewport that
follows the tail by default. Keystrokes:

| Key            | Action                                       |
| -------------- | -------------------------------------------- |
| `/`            | open search (matches highlight as you type)  |
| `tab`          | jump to next match                           |
| `shift+tab`    | jump to previous match                       |
| `esc`          | clear search                                 |
| `enter`        | close the search box (matches stay applied)  |
| `f`            | toggle follow (auto-scroll to newest line)   |
| `g` / `G`      | jump to top / bottom                         |
| `w`            | toggle line wrap                             |
| `s`            | save buffer to `mnil-<timestamp>.log` in CWD |
| `?`            | toggle full keybinding help                  |
| `q`, `ctrl+c`  | quit                                         |

Mouse wheel scrolls the viewport (and disables follow).

## Features

- **Live search** — highlights and counts update on every keystroke.
- **ANSI-aware** — source colors are preserved; search highlights overlay
  without breaking surrounding colors.
- **Term highlighting** — plain log lines get `ERROR`/`WARN`/`INFO`/`DEBUG`,
  HTTP methods, URLs, and IPv4 auto-colored. Lines that already carry their
  own ANSI codes are passed through untouched.
- **Theme-integrated** — chips and highlights use the ANSI 16-color palette,
  so the UI inherits whatever your terminal theme defines (Dracula, Solarized,
  Nord, etc.).
- **Bounded buffer** — 100k-line cap with batched trimming so a long-running
  source can't exhaust memory.
- **Throttled rendering** — incoming lines accumulate and the viewport
  refreshes at ~30 Hz, decoupling ingest rate from render cost.
- **Per-line render cache** — wrap output is cached per line and only
  rebuilt on wrap toggle or width change.
- **Save** — `s` writes the current buffer (ANSI-stripped) to a timestamped
  file in the working directory.

## Try it

A quick fish-shell loop that produces varied log-like output for testing:

```fish
while true
    set ts (date +%H:%M:%S)
    set lvl (random choice INFO WARN ERROR DEBUG)
    set msg (random choice \
        "request handled in 42ms" \
        "cache miss for key=user:1234" \
        "ERROR: connection refused to db:5432" \
        "GET /api/users 200" \
        "compiled 1245 modules" \
        "hot reload completed")
    echo "$ts [$lvl] $msg"
    sleep 0.1
end | mnil
```

For tools that disable color when stdout isn't a TTY:

```sh
FORCE_COLOR=1 npx next dev | mnil          # most JS tooling
CLICOLOR_FORCE=1 ls -laG    | mnil          # BSD ls (macOS)
ls --color=always           | mnil          # GNU ls
```

## Development

```sh
go build -o mnil .
./mnil --version
```

To build with an injected version string (the Homebrew formula does this):

```sh
go build -ldflags "-X main.version=v0.1.0" -o mnil .
```

## License

MIT — see [LICENSE](LICENSE).
