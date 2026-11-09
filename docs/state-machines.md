# State machines

Most of lam's behavior is a state machine, and most of them were never written
down: they live as a few booleans and strings that are only meaningful together.
This page lists them all. **Explicit** ones have a typed state, one place that
changes it and a test of the allowed moves; **implicit** ones are still flags,
with what to know before touching them. Rule for new code: if two fields can
disagree, make them one typed state.

## Explicit

### Session phase (`session_state.go`)

What a person sees of a session. `sessionInfo.Phase()` is the only reading of
`Activity` (the host's judgment, wire values `"active"`/`"quiet"`/`""`) and
`Unread`; the sidebar groups, the row dot, the tooltip, the "working · 4m"
timing and `lam ls --json` go through it. The window title counts `Unread`, and
the notifier and the footer count read `Activity` through the same named
constants (`activityActive`, `activityQuiet`) rather than `Phase`.

```
Working ──finishes, nobody looking──▶ Unread ──selected / attached──▶ Idle
Idle ──works again──▶ Working            Unread wins over Working
```

`lam ls --json` and `lam ps --json` carry it as `"state": "working" | "unread" | "idle"`
next to the older `activity` and `unread` fields, which are unchanged. Agents
should read `state`.

### Document tab (`doc_state.go`)

`Loading → Ready | Failed`, `Ready | Failed → Loading` (reload), any `→ Closed`
(final, so a late answer from the loader cannot revive a closed tab). Changed
only by `docView.transition`, under `guiApp.mu`.

### New-session form (`session_wizard.go`)

`wizardStage`: `workdir → provider` with Enter, back with Esc (`prompt` is the
terminal interface's extra step). `busy` is set while the folder is being
resolved or the session created in the background; Enter is ignored then, and
the answer is dropped if the dialog moved on.

### Dialog kind, focus, document kind

`dialogKind` (`gui_dialogs.go`), `guiFocus` (`gui.go`), `docKind` are plain
enums: one value at a time, no flags beside them.

## Implicit (still flags)

| Machine | Where | States hidden in | What to know |
|---|---|---|---|
| Host lifecycle | `host.go` | `closing`, `exited`, `signaled`, `termClosed`, `ptyDone` | start → running → command exited → drain pty (≤1 s) → `closing` (listener closed, files removed) → pty closed → emulator freed under `mu` (`termClosed`). Nothing may touch `h.term` after `termClosed`; `signaled` only decides whether the session is offered for restore. |
| Attached pane | `ide.go` `embedded` | `done`, `started`, `restartHeld`, `linkGen` | running → done; `paneRestartHold` stops a dying session being restarted in a loop; `linkGen` ties a remote pane to one connection. |
| Remote link | `remote.go` `linkStatus` | `Up`, `Attempt`, `RetryAt`, `Err` | up → down (retrying, with backoff) → up; `Gen` changes with every new connection, and panes compare it. |
| Connect dialog | `gui_connect.go` | `dlg.busy`, `connID`, `askReq`, `connRes` | idle → connecting → (asking a host-key/password question → connecting) → connected or failed; `connID` cancels an attempt by changing it. |
| Mux stream | `mux.go` | `once`, `done`, `closed` | open → closed once; `Close` sends the close frame in the background. |
| Terminal start | `gui.go` | `termPending`, `termVisible`, `terminals` | closed → preparing (folder and leftover shells asked off the UI goroutine) → open; the answer is dropped if the user moved on. |
| Sidebar slide, toasts, dialog fade | `gui.go` | `sideAnim/sideAt/sideInit`, `toasts`, `opened` | pure animation timers, driven from the frame clock. |
| Document selection | `gui_doc.go` | `selecting`, `hasSel`, `selA/selB`, `stale` | none → dragging → selected; cleared whenever the layout is rebuilt. |
| Keep-awake helper | `keepawake.go` | `starting`, `busy` | one helper at a time. |
| Updates | `update_check.go`, `gui` | record file, chip | checked daily → newer found → downloaded → swapped. |

Why these were left alone: each is correct and covered by tests today, and
turning them into typed states touches concurrent code (host, mux, link) where a
refactor can break behavior without a test noticing. Convert one when you next
have to change it, and add its allowed-moves test with it.
