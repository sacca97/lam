# Architecture

LAM is a single Go module with a small executable entry point and an internal
application package:

```text
cmd/lam/       executable entry point
internal/app/  command handling, session management, terminal UI, and tests
site/          static project page
scripts/       pinned native dependency build
```

The CLI starts one host process per session. Each host owns the command's PTY,
libghostty-vt screen state, scrollback, and local Unix socket. The UI attaches to those
hosts, so sessions continue after the UI exits. Remote UI connections (`lam ssh`) run `lam remote` on the host over SSH and speak a framed
protocol on its stdio: requests, plus any number of attached session streams multiplexed on
the one connection. SSH provides authentication and encryption.

The `internal/app` package is intentionally private to this module. Keep the public
executable interface in `cmd/lam`; add a package boundary only when it gives a subsystem
an independently useful API or ownership model.
