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
hosts, so sessions continue after the UI exits. Remote UI connections share the same
newline-delimited JSON request protocol over either SSH stdio or direct TCP. The daemon can
listen on TCP port 42000 on all interfaces. A client first authenticates to the remote host over
SSH and asks it to mint a short-lived, single-use ticket; the listener validates the signed
ticket before accepting the TCP connection. The signing key stays on the remote host. TCP
attachment switches to the host's framed terminal stream after the attach handshake. Session
data remains unencrypted, so the VPN and firewall must restrict port 42000 to trusted devices.

The `internal/app` package is intentionally private to this module. Keep the public
executable interface in `cmd/lam`; add a package boundary only when it gives a subsystem
an independently useful API or ownership model.
