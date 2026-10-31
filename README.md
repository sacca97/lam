# LAM — Little Agent Manager

LAM is a terminal session manager for Claude Code, Codex, OpenCode, Pi, and ordinary shell
commands. Sessions keep running when you leave the interface, and the session list is shared
across directories on the same host.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/sacca97/lam/main/install.sh | bash
```

This downloads the latest [GitHub release](https://github.com/sacca97/lam/releases) for your
OS and architecture (Linux x86_64 and macOS arm64), verifies its SHA-256 digest, and installs it to
`~/.local/bin` without `sudo`. Set `LAM_VERSION=v0.2.0` to pick a release or `PREFIX=/usr/local`
to install elsewhere, for example `curl -fsSL … | PREFIX=/usr/local bash`. Later, `lam update`
upgrades it. If you would rather read the script first, download `install.sh` and run it.

### Build from source

LAM builds the libghostty-vt terminal backend on Linux x86_64 and macOS arm64. It handles
terminal emulation, screen restore, and scrollback. The first build downloads a
checksum-verified Zig toolchain and a pinned Ghostty revision, then caches the backend under
`~/.cache/lam`. Other platforms need a manual libghostty-vt build and `PKG_CONFIG_PATH` pointing
to it.

Requirements: Go 1.26+, a C compiler, `pkg-config`, `make`, `git`, `curl`, `tar`, `xz`, and
`sha256sum` (or macOS `shasum`).

```sh
git clone https://github.com/sacca97/lam.git
cd lam
make install
lam
```

The binary installs to `~/.local/bin` without requiring `sudo`; set `PREFIX=/usr/local` for a
system-wide install when you have permission to write there. `./install.sh` checks the build
dependencies and runs `make install`.

Other useful targets: `make build`, `make check` (format check, vet, and tests),
`make test-race`, `make clean`, and `make help`.

## Development

The executable entry point is `cmd/lam`; implementation and tests are in `internal/app`.
See [CONTRIBUTING.md](CONTRIBUTING.md) for the local checks and
[docs/architecture.md](docs/architecture.md) for the process and package layout.

## Start LAM

```sh
lam                                      # open the shared session list
lam ~/Projects/api                       # set the default directory for new sessions
lam start                                # create a shell session and open it
lam start codex ~/Projects/api           # start Codex in that directory
lam start claude ../repo --cmd --resume  # pass extra arguments to Claude
lam start ~/repo --cmd make test         # run a generic command
lam ssh device2                          # use the UI with sessions on an SSH host
```

`lam` opens the shared session list, so you do not need to name or attach to an existing session
to get back to it. `lam attach <session>` remains available when you want to connect directly to
one session without opening the interface. `lam new` is an alias for `lam start`.

For `lam start`, the provider and working directory are optional; the directory defaults to the
current one, and omitting the provider starts a shell. Sessions are named like `claude-myproject` unless you give `--name NAME`; use it to set a name and
`--cmd ARG...` to pass additional arguments to the provider or run a generic command. `--cmd`
consumes all remaining arguments, so put `--name` before it.

When there are no sessions, LAM opens a shell in its starting directory. In SSH mode, the shell
and sessions run on the remote host; install `lam` there with `make install` or put it on `PATH`.
SSH mode prefers `~/.local/bin/lam`, then checks `PATH`. Remote mode currently
supports sessions, attach, rename, stop, and restore.

For direct TCP over a VPN, enable the listener on the remote device and connect from the local
device:

```sh
# Remote device
lam remote --listen

# Local device
lam remote fedora
```

`lam remote --listen` configures the daemon to listen on all interfaces at port `42000`; use
`lam remote --disable` to turn it off. Install the daemon with `lam install-service` if it is not
already running. The client authenticates to the remote host over SSH once, requests a
short-lived, single-use TCP ticket, and opens one TCP connection. Requests and every attached
session share that connection (compressed, with flow control), so switching sessions costs a single
round trip and no further logins. No key is copied between devices. The TCP stream is unencrypted,
so firewall port `42000` to your VPN network. `lam ssh` uses the same protocol over a single SSH
connection. Both ends must run the same version of `lam`; an older remote host is refused with a
message asking you to update it.

## Keyboard controls

In the Sessions view:

- `↑` / `↓` moves the selection; `Enter` focuses the selected session and `1`–`9` selects one directly.
- `/` filters by session name or directory. `n` creates a session, `r` renames it, and `k` stops it after confirmation.
- New sessions are named after the tool and the last folder of their directory, such as `claude-os-findmy`, with `-1`, `-2`, … added when that name is taken. Rename with `r`.
- The new-session choices include `Terminal`, which opens a shell instead of an agent.
- `t` (or `Alt+T` from any focus) toggles a terminal for the selected session. It starts in that session's working directory and is placed to the right of the session pane (40% of its width) by default. Click either pane or use `Ctrl+Space` to switch focus. Press `s` for settings: the terminal can sit on the right or below the session pane, at 30%, 40% or 50% of the width or height. The choice is saved. On narrow windows the terminal falls back to below.
- `Ctrl+Space` or `Ctrl+\` switches focus between the sidebar and main pane. When the integrated terminal is open, the shortcuts cycle through the sidebar, session, and terminal. Clicking a pane also focuses it.
- Sessions survive closing LAM. If their hosts were ended by a reboot, crash or logout, LAM offers to restore them on the next start (`y` restore, `n` discard), reopening each agent in its directory and resuming its last conversation (`claude --continue`, `codex resume --last`, `pi`/`opencode --continue`). This also works over `lam ssh` and `lam remote`: the remote host keeps the list and restores on its own side. `lam restore` does the same locally from the command line and `lam restore --list` shows what it would do. Sessions you stop yourself or whose command exits are not offered.
- `F2` collapses or expands the sidebar. Hold `Shift` while dragging across the integrated terminal to select and copy text.
- `Ctrl+↑` / `Ctrl+↓` switch to the previous or next session from anywhere. `Ctrl+D` or `q` detaches: LAM closes and every session, including each session's terminal, keeps running. `Ctrl+Q` quits from anywhere and, after confirmation, stops all sessions. Both are intercepted before the focused program sees them.
- Dragging the edge of the sidebar or of the terminal resizes it, and double-clicking the sidebar edge restores its default width; project headings in the sidebar fold with `←` or `Space`.
- `?` opens the shortcut help.

New sessions use a guided form in the main pane. Choose the working directory before the
provider; the last-used provider starts selected. Leave the directory empty to use LAM's
starting directory. `Tab` completes directory names, and `Esc` goes back or cancels. A
session's integrated terminal keeps running when LAM closes and is attached again the next time
you open it. If a command is still running in it when you close it with `t`, that shell remains
available in the session list.

Set `LAM_FOCUS_CHORD=off` to pass both focus shortcuts to the session, or set it to
`ctrl-space` or `ctrl-backslash` to reserve only one.

## Sessions

LAM runs a small host process for each session. It starts the command in a PTY, keeps terminal
screen state and scrollback, and exposes a local Unix socket for the interface. Sessions remain
available after LAM exits and can be reattached:

```sh
lam attach <session>
```

While attached, `Ctrl+Space` or `Ctrl+\` detaches and leaves the session running. A session
ends when its command exits or you stop it from LAM. A red `●` marks an agent session that has
finished since you last attached.

## Updating and releases

`lam update` installs the latest GitHub release for your OS and architecture; it checks the
download against its published SHA-256 and swaps the binary in place. `lam update --check`
only reports. When `lam ssh` finds the two ends on different releases, it says which one is
older and offers to update it before connecting; nothing is changed without a `y`.

Releases are built on your machine, because lam links libghostty through cgo, and published
by pushing a tag, with no `gh` login:

```sh
make release TAG=v0.2.0     # builds Linux x86_64 and cross-builds macOS arm64 with Zig
git push origin v0.2.0
```

`make release` (from a clean tree) commits `release/lam-linux-amd64` and
`release/lam-darwin-arm64` to a commit that only the tag points at, so `main` never carries
binaries and the source tarball leaves them out (`.gitattributes`). The `release` workflow
(`.github/workflows/release.yml`) creates the GitHub release with those two executables; GitHub
adds the source tarball. `lam update` and `install.sh` download the `lam-<os>-<arch>` that matches
the system they run on and check it against the SHA-256 GitHub records for the asset. Nothing is
compiled in CI. Development builds (not an exact tag) are never compared by version.

## Uninstall

If the system service is installed, remove it first, then remove the binary:

```sh
lam uninstall-service
make uninstall
```

## License

MIT. See [LICENSE](LICENSE).
