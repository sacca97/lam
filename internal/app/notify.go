package app

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"
)

// An agent counts as having finished something worth a notification only when
// it worked for at least this long. Attaching or resizing makes a session
// redraw, which reads as a short burst of activity; that must not notify.
const notifyMinWork = 15 * time.Second

// The agent must also stay quiet this long. Between two steps of one task the
// output or CPU often drops for a few seconds; that is a pause, not the end.
const notifyQuietFor = 25 * time.Second

// A gap this long between two updates means the machine slept or the window
// stalled; what was recorded before it says nothing about now.
const notifyMaxGap = 45 * time.Second

// finishWatcher turns the activity reported in each session list into "this
// agent just finished" events. It lives in the interface, so a session on an
// SSH host notifies on the machine the person is sitting at.
type finishWatcher struct {
	mu          sync.Mutex // refreshes can overlap
	activeSince map[string]time.Time
	quietSince  map[string]time.Time
	last        time.Time // the previous update
}

// reset forgets every session, as when the window moves to another host.
func (w *finishWatcher) reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	clear(w.activeSince)
	clear(w.quietSince)
	w.last = time.Time{}
}

func newFinishWatcher() *finishWatcher {
	return &finishWatcher{activeSince: map[string]time.Time{}, quietSince: map[string]time.Time{}}
}

// update records the new list and returns the agent sessions that went from
// working to quiet. Shells and terminals are left out: they have no task that
// ends, only commands.
func (w *finishWatcher) update(sessions []sessionInfo, now time.Time) []sessionInfo {
	w.mu.Lock()
	defer w.mu.Unlock()
	var finished []sessionInfo
	if !w.last.IsZero() && now.Sub(w.last) > notifyMaxGap {
		clear(w.activeSince)
		clear(w.quietSince)
	}
	w.last = now
	seen := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		seen[s.Name] = true
		if s.Shell {
			continue
		}
		since, wasActive := w.activeSince[s.Name]
		switch s.Activity {
		case "active":
			delete(w.quietSince, s.Name)
			if !wasActive {
				w.activeSince[s.Name] = now
			}
		case "quiet":
			if !wasActive {
				break
			}
			quiet, ok := w.quietSince[s.Name]
			if !ok {
				quiet = now
				w.quietSince[s.Name] = now
			}
			if now.Sub(quiet) < notifyQuietFor {
				break
			}
			delete(w.activeSince, s.Name)
			delete(w.quietSince, s.Name)
			// The work ran from the first activity until it went quiet.
			if quiet.Sub(since) >= notifyMinWork {
				finished = append(finished, s)
			}
		default:
			// Unknown (a host that did not answer): keep the work, but the
			// quiet period starts over.
			delete(w.quietSince, s.Name)
		}
	}
	for name := range w.activeSince {
		if !seen[name] {
			delete(w.activeSince, name)
			delete(w.quietSince, name)
		}
	}
	return finished
}

// finishNotification is the title and text for an agent that finished: the
// session's name, then its folder, and the host when it runs over SSH.
func finishNotification(s sessionInfo, dir, backendName string) (title, body string) {
	title = s.Name + " finished"
	body = dir
	if host, ok := strings.CutPrefix(backendName, "ssh:"); ok {
		if body == "" {
			body = "on " + host
		} else {
			body += " on " + host
		}
	}
	if body == "" {
		body = "Waiting for you"
	}
	return title, body
}

// notifyCommand builds the command that shows a desktop notification through
// the system's own mechanism, or nil when there is none. When clickable is
// true, the command waits and prints "default" if the notification is clicked.
var notifyCommand = func(title, body string) (cmd *exec.Cmd, clickable bool) {
	switch runtime.GOOS {
	case "darwin":
		if path, err := exec.LookPath("osascript"); err == nil {
			script := "display notification " + appleScriptString(body) + " with title " + appleScriptString(title)
			return exec.Command(path, "-e", script), false
		}
	case "linux":
		if wsl() {
			if path, err := exec.LookPath("powershell.exe"); err == nil {
				return exec.Command(path, "-NoProfile", "-NonInteractive", "-Command", windowsToastScript(title, body)), false
			}
		}
		if path, err := exec.LookPath("notify-send"); err == nil {
			// "default" is the action a click on the notification itself
			// invokes; notify-send prints its name and exits.
			return exec.Command(path, "--app-name=lam", "--action=default=Open session", title, body), true
		}
		if path, err := exec.LookPath("gdbus"); err == nil {
			return exec.Command(path, "call", "--session",
				"--dest=org.freedesktop.Notifications",
				"--object-path=/org/freedesktop/Notifications",
				"--method=org.freedesktop.Notifications.Notify",
				"lam", "0", "", title, body, "[]", "{}", "-1"), false
		}
	case "windows":
		if path, err := exec.LookPath("powershell.exe"); err == nil {
			return exec.Command(path, "-NoProfile", "-NonInteractive", "-Command", windowsToastScript(title, body)), false
		}
	}
	return nil, false
}

// notifyNative shows a notification through the app's own connection to the
// system and reports whether it did; the macOS window installs it, so that a
// click reaches the app. Without it the helper command below is used.
var notifyNative func(title, body string, onClick func()) bool

// notifyWaitLimit is how long a click on a notification can still be reported.
const notifyWaitLimit = 10 * time.Minute

// notify shows a notification without waiting for it: the helper runs on its
// own and is reaped in the background. onClick runs, on that background
// goroutine, if the person clicks the notification and the system reports it.
func notify(title, body string, onClick func()) {
	if notifyNative != nil && notifyNative(notifyText(title), notifyText(body), onClick) {
		return
	}
	cmd, clickable := notifyCommand(notifyText(title), notifyText(body))
	if cmd == nil {
		return
	}
	var out strings.Builder
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if clickable {
		cmd.Stdout = &out
	}
	if err := cmd.Start(); err != nil {
		logEvent("notify: %v", err)
		return
	}
	go func() {
		// With an action, notify-send stays until the notification is closed, and
		// some shells never report an expired one: it would linger for good.
		stuck := time.AfterFunc(notifyWaitLimit, func() { cmd.Process.Kill() })
		defer stuck.Stop()
		if cmd.Wait() == nil && clickable && onClick != nil && strings.TrimSpace(out.String()) == "default" {
			onClick()
		}
	}()
}

// notifyText keeps a session name or directory from carrying control
// characters into a notification.
func notifyText(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	if len(s) > 200 {
		s = strings.ToValidUTF8(s[:200], "")
	}
	return s
}

func appleScriptString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func powershellString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// windowsToastScript shows a toast through the Windows notification API, which
// every Windows 10/11 PowerShell can reach without installing a module. The
// texts are set as nodes, never parsed as XML.
func windowsToastScript(title, body string) string {
	return `$ErrorActionPreference='Stop';` +
		`[void][Windows.UI.Notifications.ToastNotificationManager,Windows.UI.Notifications,ContentType=WindowsRuntime];` +
		`$x=[Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02);` +
		`$t=$x.GetElementsByTagName('text');` +
		`[void]$t.Item(0).AppendChild($x.CreateTextNode(` + powershellString(title) + `));` +
		`[void]$t.Item(1).AppendChild($x.CreateTextNode(` + powershellString(body) + `));` +
		// PowerShell's own app id: an unregistered id shows nothing.
		`$id='{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe';` +
		`[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($id).Show([Windows.UI.Notifications.ToastNotification]::new($x))`
}

// wsl reports whether this Linux runs under Windows, where the notification
// belongs on the Windows desktop.
func wsl() bool {
	if os.Getenv("WSL_DISTRO_NAME") != "" {
		return true
	}
	_, err := os.Stat("/proc/sys/fs/binfmt_misc/WSLInterop")
	return err == nil
}
