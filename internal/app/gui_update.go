//go:build gui

package app

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// guiUpdate tracks the newest release and the installation of it. The window
// reads it every frame; the checks and the install write it from goroutines.
type guiUpdate struct {
	mu         sync.Mutex
	tag        string // a release newer than this build, once known
	checking   bool
	installing bool
	done       string // set when the install finished
	note       string // something to tell after it, such as a daemon that did not restart
	failed     string
}

// shownVersion is this build for people: v0.2.0, or the development build's name.
func shownVersion() string {
	if _, ok := parseRelease(version); ok && version != "" && version[0] != 'v' {
		return "v" + version
	}
	return version
}

func (u *guiUpdate) available() (string, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.tag, u.tag != "" && u.done == ""
}

// versionStatus is the settings row's right side.
func (u *guiUpdate) versionStatus() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	switch {
	case u.done != "":
		return "Restart to use " + u.tag
	case u.tag != "":
		return "Update to " + u.tag
	case u.checking:
		return "Checking…"
	}
	return "Check now"
}

// startUpdateChecks looks for a newer release a little after start and then
// once a day, remembering the answer between runs so a restart does not ask again.
func (g *guiApp) startUpdateChecks() {
	go func() {
		time.Sleep(10 * time.Second)
		for {
			if updateChecksOn(loadSettings()) {
				rec := readUpdateRecord()
				if time.Since(rec.CheckedAt) >= updateCheckEvery {
					g.checkUpdate()
				} else if order, ok := compareRelease(version, rec.Tag); ok && order < 0 {
					g.upd.mu.Lock()
					g.upd.tag = rec.Tag
					g.upd.mu.Unlock()
					if g.win != nil {
						g.win.Invalidate()
					}
				}
			}
			time.Sleep(time.Hour)
		}
	}()
}

// forget drops a newer release that was found but not installed.
func (u *guiUpdate) forget() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.done == "" {
		u.tag = ""
	}
}

// restore brings back the release a recent check found.
func (u *guiUpdate) restore(tag string) {
	if order, ok := compareRelease(version, tag); ok && order < 0 {
		u.mu.Lock()
		u.tag = tag
		u.mu.Unlock()
	}
}

func (g *guiApp) checkUpdateAsync() { go g.checkUpdate() }

func (g *guiApp) checkUpdate() {
	g.upd.mu.Lock()
	if g.upd.checking {
		g.upd.mu.Unlock()
		return
	}
	g.upd.checking = true
	g.upd.mu.Unlock()
	if g.win != nil {
		g.win.Invalidate()
	}
	r, newer, err := checkForUpdate()
	g.upd.mu.Lock()
	g.upd.checking = false
	if err == nil {
		writeUpdateRecord(updateRecord{CheckedAt: time.Now(), Tag: r.Tag})
		if newer {
			g.upd.tag = r.Tag
		} else {
			g.upd.tag = ""
		}
	}
	g.upd.mu.Unlock()
	if g.win != nil {
		g.win.Invalidate()
	}
}

func (g *guiApp) openUpdate() {
	g.upd.mu.Lock()
	g.upd.failed = ""
	g.upd.mu.Unlock()
	g.dlg = dialog{kind: dlgUpdate}
}

// startInstall downloads the release, checks it and swaps it in.
func (g *guiApp) startInstall() {
	g.upd.mu.Lock()
	if g.upd.installing || g.upd.done != "" || g.upd.tag == "" {
		g.upd.mu.Unlock()
		return
	}
	tag := g.upd.tag
	g.upd.installing, g.upd.failed = true, ""
	g.upd.mu.Unlock()
	go func() {
		r, err := fetchRelease(tag)
		if err == nil {
			err = installRelease(r)
		}
		note := ""
		if err == nil {
			note = restartDaemonAfterUpdate()
		}
		g.upd.mu.Lock()
		g.upd.note = note
		g.upd.installing = false
		if err != nil {
			g.upd.failed = err.Error()
		} else {
			g.upd.done = tag
		}
		g.upd.mu.Unlock()
		if g.win != nil {
			g.win.Invalidate()
		}
	}()
}

// updateDialogText is the title, buttons and text of the update dialog.
func (g *guiApp) updateDialogText() (title, ok, cancel string, body []string) {
	g.upd.mu.Lock()
	defer g.upd.mu.Unlock()
	title, ok, cancel = "Update lam", "Update", "Not now"
	body = []string{
		fmt.Sprintf("%s is available; this is %s.", g.upd.tag, shownVersion()),
		"It is downloaded and checked, then swapped in.",
		"Sessions keep running. Restart lam to use it.",
	}
	switch {
	case g.upd.tag == "" && g.upd.done == "":
		ok, cancel = "", "Close"
		body = []string{"lam is up to date."}
	case g.upd.installing:
		ok = "Updating…"
	case g.upd.done != "":
		ok, cancel = "", "Close"
		body = []string{"Updated to " + g.upd.done + ".", "Close and reopen lam to use it; sessions are not affected."}
		if g.upd.note != "" {
			body = append(body, wrapWords(g.upd.note, 56)...)
		}
	case g.upd.failed != "":
		body = append(body, wrapWords("Could not update: "+g.upd.failed, 56)...)
	}
	return title, ok, cancel, body
}

// wrapWords breaks text into lines of about width characters.
func wrapWords(text string, width int) []string {
	var lines []string
	line := ""
	for _, w := range strings.Fields(text) {
		if line != "" && len(line)+1+len(w) > width {
			lines = append(lines, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += w
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}
