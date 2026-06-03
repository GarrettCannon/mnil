# mnil

**M**y **N**ame **I**s **L**og — a small TUI log viewer for piped or spawned
processes.

Pipe a command in, or hand mnil the command to run. Search live as you type,
jump between matches, toggle follow, wrap long lines, save the buffer to a
file. ANSI colors from the producing tool pass through; plain text gets common
log tokens (`ERROR`/`WARN`/`INFO`/URLs/IPs) auto-colored.

```
mnil "npm run dev"             # spawn — captures stdout + stderr together
mnil "ping 8.8.8.8"
ping 8.8.8.8 | mnil             # pipe — stdout only
find / 2>&1   | mnil
tail -F /var/log/system.log | mnil
```

Use the `mnil "<cmd>"` form when you want stderr captured alongside stdout
without the `2>&1` dance — mnil owns both ends.

![mnil demo](docs/demo.gif)

Built with [Bubble Tea](https://github.com/charmbracelet/bubbletea),
[Bubbles](https://github.com/charmbracelet/bubbles), and
[Lip Gloss](https://github.com/charmbracelet/lipgloss).

## Install

### Homebrew

```sh
brew tap garrettcannon/tap
brew install mnil
```

### From source

Requires Go 1.21 or newer.

```sh
go install github.com/garrettcannon/mnil@latest
```

## Usage

```
mnil <command>       # spawn — captures stdout + stderr
<command> | mnil     # pipe — captures stdout only
```

mnil renders incoming lines in a scrollable viewport that follows the tail by
default. Keystrokes:

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
- **stderr capture** — in `mnil "<cmd>"` mode the child's stdout and stderr
  share the same pipe, so error output lands in the buffer without `2>&1`.
- **Color hinting** — in `mnil "<cmd>"` mode mnil sets `FORCE_COLOR=1`,
  `CLICOLOR_FORCE=1`, and `PYTHONUNBUFFERED=1` in the child's environment so
  most tools voluntarily keep color and flush per line.

In pipe mode, you may still need to coax color out of tools that detect
non-TTY stdout:

```sh
FORCE_COLOR=1 npx next dev | mnil          # most JS tooling
CLICOLOR_FORCE=1 ls -laG    | mnil          # BSD ls (macOS)
ls --color=always           | mnil          # GNU ls
```

`mnil "npx next dev"` handles all of these automatically.

## License

MIT — see [LICENSE](LICENSE).
