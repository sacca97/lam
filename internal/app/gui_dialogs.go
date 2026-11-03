//go:build gui

package app

import (
	"fmt"
	"image"
	"image/color"
	"strings"
	"time"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
)

// The dialogs are the TUI's: a new session (directory, then provider),
// rename, stop, stop everything, restore after a reboot, settings and the
// shortcut list. Each takes Enter and Esc as well as clicks.

type dialogKind int

const (
	dlgNone dialogKind = iota
	dlgNewSession
	dlgRename
	dlgStop
	dlgQuitAll
	dlgRestore
	dlgSettings
	dlgHelp
	dlgConnect
	dlgAsk
	dlgUpdate
)

type dialog struct {
	kind     dialogKind
	field    textField
	err      string
	target   sessionInfo
	restore  []manifestEntry
	wizard   sessionWizard
	row      int // settings row
	scrim    bool
	cardTag  bool    // the card takes presses, so only the scrim around it dismisses
	busy     bool    // connecting
	remember bool    // keep the password in the keychain
	ask      *sshAsk // the SSH client's question
}

// textField is a one-line editor using the TUI's editing helpers.
type textField struct {
	buf    []byte
	cursor int
	secret bool // shown as dots
}

func (f *textField) set(s string)    { f.buf, f.cursor = []byte(s), len(s) }
func (f *textField) insert(s string) { f.buf, f.cursor = insertWizardText(f.buf, f.cursor, s) }
func (f *textField) String() string  { return string(f.buf) }

// shown is what the field displays: the text, or dots for a secret.
func (f *textField) shown() string {
	if f.secret {
		return strings.Repeat("•", len([]rune(string(f.buf))))
	}
	return string(f.buf)
}

// shownBefore is shown() up to the caret.
func (f *textField) shownBefore() string {
	before := string(f.buf[:max(0, min(f.cursor, len(f.buf)))])
	if f.secret {
		return strings.Repeat("•", len([]rune(before)))
	}
	return before
}

// key edits the field and reports whether the key was an editing key.
func (f *textField) key(ev key.Event) bool {
	ctrl := ev.Modifiers.Contain(key.ModCtrl)
	f.cursor = max(0, min(f.cursor, len(f.buf)))
	switch {
	case ev.Name == key.NameLeftArrow:
		f.cursor = len(popUTF8(f.buf[:f.cursor]))
	case ev.Name == key.NameRightArrow:
		f.cursor = nextUTF8(f.buf, f.cursor)
	case ev.Name == key.NameHome, ctrl && ev.Name == "A":
		f.cursor = 0
	case ev.Name == key.NameEnd, ctrl && ev.Name == "E":
		f.cursor = len(f.buf)
	case ev.Name == key.NameDeleteBackward && (ctrl || ev.Modifiers.Contain(key.ModAlt)), ctrl && ev.Name == "W":
		f.buf, f.cursor = deleteWizardWord(f.buf, f.cursor)
	case ev.Name == key.NameDeleteBackward:
		f.buf, f.cursor = backspaceWizardText(f.buf, f.cursor)
	case ev.Name == key.NameDeleteForward:
		f.buf, f.cursor = deleteWizardText(f.buf, f.cursor)
	case ctrl && ev.Name == "U":
		f.buf, f.cursor = nil, 0
	default:
		return false
	}
	return true
}

func (g *guiApp) openNewSession() {
	host := ""
	start := g.launchDir
	if s, ok := g.selectedInfo(); ok {
		host = s.Host
		if s.CWD != "" {
			start = s.CWD // most new sessions go next to the one being looked at
		}
	}
	if host != "" && start == g.launchDir {
		start = ""
	}
	g.openNewSessionIn(start, host)
}

// openNewSessionOn starts the dialog for a machine, in the folder of the
// session being looked at when that is on the same machine.
func (g *guiApp) openNewSessionOn(host string) {
	if s, ok := g.selectedInfo(); ok && s.Host == host && s.CWD != "" {
		g.openNewSessionIn(s.CWD, host)
		return
	}
	start := ""
	if host == "" {
		start = g.launchDir
	}
	g.openNewSessionIn(start, host)
}

// openNewSessionIn starts the new session dialog with start as the directory,
// for a session on host (empty for this machine).
func (g *guiApp) openNewSessionIn(start, host string) {
	src := g.sourceFor(host)
	if start == "" {
		if dir, err := src.WorkingDirectory(); err == nil {
			start = dir
		}
	}
	dir := normalizedSessionDir(start, src)
	g.dlg = dialog{kind: dlgNewSession, wizard: sessionWizard{
		stage:             wizardWorkdir,
		host:              host,
		src:               src,
		defaultDir:        dir,
		defaultDirDisplay: shortenHomePath(dir, src),
	}}
}

// sessionsOn are the names taken on one machine, as that machine knows them.
func (g *guiApp) sessionsOn(host string) []sessionInfo {
	var out []sessionInfo
	for _, s := range g.allSessions() {
		if s.Host == host {
			s.Name = shortName(s.Name)
			out = append(out, s)
		}
	}
	return out
}

func (g *guiApp) openRename(s sessionInfo) {
	g.dlg = dialog{kind: dlgRename, target: s}
	g.dlg.field.set(shortName(s.Name))
}

func (g *guiApp) openStop(s sessionInfo) { g.dlg = dialog{kind: dlgStop, target: s} }

func (g *guiApp) openQuitAll() {
	if len(g.allSessions()) == 0 {
		g.win.Perform(system.ActionClose)
		return
	}
	g.dlg = dialog{kind: dlgQuitAll}
}

func (g *guiApp) closeDialog() {
	if g.dlg.kind == dlgAsk {
		g.answerAsk(sshAnswer{cancel: true})
	}
	if g.dlg.kind == dlgConnect && g.dlg.busy {
		g.connID++ // the attempt under way no longer counts
	}
	g.dlg = dialog{}
}

func (g *guiApp) dialogText(s string) {
	switch g.dlg.kind {
	case dlgRename, dlgConnect:
		if g.dlg.busy {
			return
		}
		g.dlg.field.insert(s)
		g.dlg.err = ""
	case dlgAsk:
		if !g.dlg.ask.yesNo {
			g.dlg.field.insert(s)
		}
	case dlgNewSession:
		if g.dlg.wizard.stage == wizardWorkdir {
			g.dlg.field.insert(s)
			g.resetCompletion()
		}
	}
}

func (g *guiApp) resetCompletion() {
	w := &g.dlg.wizard
	w.error, w.completionMatches, w.lastCompletion, w.showCompletions = "", nil, "", false
}

func (g *guiApp) dialogKey(gtx layout.Context, ev key.Event) {
	d := &g.dlg
	enter := ev.Name == key.NameReturn || ev.Name == key.NameEnter
	esc := ev.Name == key.NameEscape
	switch d.kind {
	case dlgHelp:
		g.closeDialog()
	case dlgSettings:
		switch {
		case esc:
			g.closeDialog()
		case ev.Name == key.NameUpArrow:
			d.row = max(0, d.row-1)
		case ev.Name == key.NameDownArrow, ev.Name == key.NameTab:
			d.row = min(guiSettingsRows-1, d.row+1)
		case ev.Name == key.NameLeftArrow:
			g.changeSetting(d.row, -1)
		case ev.Name == key.NameRightArrow, enter, ev.Name == key.NameSpace:
			g.changeSetting(d.row, 1)
		}
	case dlgStop, dlgQuitAll:
		switch {
		case esc:
			g.closeDialog()
		case enter:
			g.confirmDialog()
		}
	case dlgUpdate:
		// Replacing the program takes a click, never a stray Enter.
		if esc {
			g.closeDialog()
		}
	case dlgRestore:
		switch {
		case esc:
			g.closeDialog() // later: offered again next start
		case enter:
			g.confirmDialog()
		}
	case dlgRename, dlgConnect:
		switch {
		case esc:
			g.closeDialog()
		case enter:
			g.confirmDialog()
		case d.busy:
		case d.field.key(ev):
			d.err = ""
		}
	case dlgAsk:
		switch {
		case esc:
			g.closeDialog()
		case enter:
			g.confirmDialog()
		case ev.Name == key.NameTab && d.ask.canRemember:
			d.remember = !d.remember
		case !d.ask.yesNo:
			d.field.key(ev)
		}
	case dlgNewSession:
		w := &d.wizard
		if w.stage == wizardProvider {
			switch {
			case esc:
				w.stage = wizardWorkdir
				d.field.set(string(w.pathInput))
				g.resetCompletion()
			case ev.Name == key.NameUpArrow && len(w.providers) > 0:
				w.selectedProvider = (w.selectedProvider + len(w.providers) - 1) % len(w.providers)
			case ev.Name == key.NameDownArrow && len(w.providers) > 0:
				w.selectedProvider = (w.selectedProvider + 1) % len(w.providers)
			case enter:
				g.confirmDialog()
			}
			return
		}
		switch {
		case esc:
			g.closeDialog()
		case enter:
			g.confirmDialog()
		case ev.Name == key.NameTab:
			g.completeWorkdir()
		case d.field.key(ev):
			g.resetCompletion()
		}
	}
	gtx.Execute(op.InvalidateCmd{})
}

// completeWorkdir completes the directory like a shell: the unique match, or
// the common prefix, or a list of the choices on a second Tab.
func (g *guiApp) completeWorkdir() {
	w := &g.dlg.wizard
	input := g.dlg.field.String()
	if w.lastCompletion == input && len(w.completionMatches) > 1 {
		w.showCompletions = true
		return
	}
	completer, ok := w.src.(sessionDirectoryCompleter)
	if !ok {
		w.error = "Directory completion is unavailable"
		return
	}
	matches, err := completer.CompleteSessionDir(input, w.defaultDir)
	switch {
	case err != nil:
		w.error, w.completionMatches, w.showCompletions = err.Error(), nil, false
		return
	case len(matches) == 0:
		w.error, w.completionMatches, w.showCompletions = "No matching directories", nil, false
		return
	}
	w.error, w.completionMatches = "", matches
	if len(matches) == 1 {
		g.dlg.field.set(matches[0])
		w.lastCompletion, w.showCompletions = "", false
		return
	}
	if prefix := commonCompletion(input, matches); prefix != input {
		g.dlg.field.set(prefix)
		w.lastCompletion, w.showCompletions = prefix, false
		return
	}
	w.lastCompletion, w.showCompletions = input, true
}

// confirmDialog does what the dialog's primary button says.
func (g *guiApp) confirmDialog() {
	d := &g.dlg
	switch d.kind {
	case dlgConnect:
		if !d.busy {
			g.startConnect(d.field.String())
		}
	case dlgAsk:
		a := sshAnswer{yes: true, text: d.field.String(), remember: d.remember}
		g.answerAsk(a)
		g.dlg = g.askPrev
	case dlgNewSession:
		g.advanceNewSession()
	case dlgUpdate:
		g.startInstall()
	case dlgRename:
		newName := strings.TrimSpace(d.field.String())
		if err := validSessionName(newName); err != nil {
			d.err = err.Error()
			return
		}
		old := d.target.Name
		newName = hostPrefix(d.target.Host) + newName
		if newName != old {
			if err := renameSession(old, newName); err != nil {
				d.err = err.Error()
				return
			}
			// The terminal's name follows its owner's.
			if p, ok := g.terminals[old]; ok {
				if g.termView.pane == p {
					g.termView.attach(nil)
				}
				p.close()
				delete(g.terminals, old)
			}
			for _, t := range ownedTerminalsNow(old) {
				if err := renameSession(t.Name, renamedTerminal(t.Name, newName)); err != nil {
					g.say("Rename terminal of %s: %v", old, err)
				}
			}
			g.closeMain()
			g.refreshSessions()
			g.filter = ""
			g.selectSession(newName)
		}
		g.closeDialog()
	case dlgStop:
		t := d.target
		g.closeDialog()
		if err := killSessionIdentity(t); err != nil {
			g.say("Stop %s: %v", t.Name, err)
			return
		}
		if p, ok := g.terminals[t.Name]; ok {
			if g.termView.pane == p {
				g.termView.attach(nil)
			}
			delete(g.terminals, t.Name)
			go disposeFallbackShell(p)
		}
		go func() {
			for _, term := range ownedTerminalsNow(t.Name) {
				_ = killSession(term.Name)
			}
		}()
		if g.main != nil && g.main.name == t.Name {
			g.closeMain()
		}
		if t.Name == g.selected {
			g.termVisible = false
		}
		go clearUnread(t.Name)
		g.refreshSessions()
	case dlgQuitAll:
		g.closeDialog()
		var failed []string
		sessions := g.allSessions()
		for _, s := range sessions {
			if err := killSessionIdentity(s); err != nil {
				failed = append(failed, fmt.Sprintf("%s: %v", s.Name, err))
			}
		}
		if listed, err := backend().List(); err == nil {
			for _, owner := range sessions {
				for _, t := range ownedTerminals(listed, owner.Name) {
					if err := killSession(t.Name); err != nil {
						failed = append(failed, fmt.Sprintf("%s: %v", t.Name, err))
					}
				}
			}
		}
		if len(failed) > 0 {
			g.say("Stop %s", strings.Join(failed, "; "))
			g.refreshSessions()
			return
		}
		g.win.Perform(system.ActionClose)
	case dlgRestore:
		names := restoreEntryNames(d.restore)
		g.closeDialog()
		restored, failures, err := g.restorer.RestoreSessions(names)
		switch {
		case err != nil:
			g.say("Restore sessions: %v", err)
		case len(failures) > 0:
			g.say("Restored %d session(s) · %s", restored, strings.Join(failures, "; "))
		default:
			g.say("Restored %d session(s)", restored)
		}
		g.refreshSessions()
	}
}

func restoreEntryNames(entries []manifestEntry) []string {
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name
	}
	return names
}

func (g *guiApp) discardRestore() {
	names := restoreEntryNames(g.dlg.restore)
	g.closeDialog()
	if err := g.restorer.DiscardRestorable(names); err != nil {
		g.say("Discard restorable sessions: %v", err)
	}
}

func (g *guiApp) advanceNewSession() {
	d := &g.dlg
	w := &d.wizard
	switch w.stage {
	case wizardWorkdir:
		w.pathInput = append(w.pathInput[:0], d.field.buf...)
		input := strings.TrimSpace(d.field.String())
		if input == "" {
			input = w.defaultDir
		}
		dir, err := w.src.ResolveSessionDir(input, w.defaultDir)
		if err != nil {
			w.error = err.Error()
			return
		}
		available, err := availableSessionLaunchers()
		if err != nil {
			w.error = err.Error()
			return
		}
		w.workdir, w.workdirDisplay = dir, shortenHomePath(dir, w.src)
		w.providers, w.selectedProvider = sessionProviderChoices(available, getLauncher())
		w.sessions = g.sessionsOn(w.host)
		w.stage = wizardProvider
		g.resetCompletion()
	case wizardProvider:
		if w.selectedProvider < 0 || w.selectedProvider >= len(w.providers) {
			w.error = "No provider is available"
			return
		}
		launcher := w.providers[w.selectedProvider]
		command := launcherCmd(launcher, "")
		if launcher == "terminal" {
			command = fallbackShellCommand
		}
		w.sessions = g.sessionsOn(w.host)
		name := hostPrefix(w.host) + w.nameFor(w.selectedProvider)
		if err := newSession(name, w.workdir, command); err != nil {
			w.error = err.Error()
			return
		}
		if launcher != "terminal" {
			setLauncher(launcher)
		}
		g.closeDialog()
		g.refreshSessions()
		g.filter = ""
		g.selectSession(name)
	}
}

const (
	guiSettingsRows = 8
	settingUpdates  = 6 // look for a newer release
	settingVersion  = 7 // the version, and the update when there is one
)

func (g *guiApp) changeSetting(row, step int) {
	s := &g.settings
	switch row {
	case 0:
		if s.TerminalPlacement == terminalRight {
			s.TerminalPlacement = terminalBelow
		} else {
			s.TerminalPlacement = terminalRight
		}
	case 1:
		s.TerminalPercent = cyclePercent(s.TerminalPercent, step)
	case 2:
		s.MuteNotifications = !s.MuteNotifications
		g.mu.Lock()
		g.mute = s.MuteNotifications
		g.mu.Unlock()
	case 3:
		s.GUIFontSize = min(max(g.fontSize()+float32(step)*0.5, minGUIFontSize), maxGUIFontSize)
	case 4:
		if g.th.Name == "light" {
			s.GUITheme = "dark"
		} else {
			s.GUITheme = "light"
		}
		g.setTheme(s.GUITheme)
	case 5:
		s.TailscaleAutoUp = !s.TailscaleAutoUp
	case settingUpdates:
		s.NoUpdateCheck = !s.NoUpdateCheck
		switch {
		case s.NoUpdateCheck:
			g.upd.forget() // an update it found no longer shows
		case time.Since(readUpdateRecord().CheckedAt) >= updateCheckEvery:
			g.checkUpdateAsync()
		default:
			g.upd.restore(readUpdateRecord().Tag)
		}
	case settingVersion:
		if step < 0 {
			break
		}
		if _, ok := g.upd.available(); ok {
			g.openUpdate()
		} else {
			g.checkUpdateAsync()
		}
	}
	saveSettings(*s)
}

func (g *guiApp) settingValues() [guiSettingsRows][2]string {
	s := g.settings
	placement := "Below"
	if s.TerminalPlacement == terminalRight {
		placement = "Right"
	}
	notify := "On"
	if s.MuteNotifications {
		notify = "Off"
	}
	theme := "Dark"
	if g.th.Name == "light" {
		theme = "Light"
	}
	tailscale := "Ask first"
	if s.TailscaleAutoUp {
		tailscale = "Turn on"
	}
	return [guiSettingsRows][2]string{
		{"Terminal position", placement},
		{"Terminal size", fmt.Sprintf("%d%%", s.TerminalPercent)},
		{"Notify when done", notify},
		{"Text size", textSizeLabel(g.fontSize(), s.GUIFontSize == 0)},
		{"Theme", theme},
		{"Tailscale if off", tailscale},
		{"Look for updates", map[bool]string{true: "On", false: "Off"}[updateChecksOn(s)]},
		{"lam " + shownVersion(), g.upd.versionStatus()},
	}
}

// guiShortcuts lists lam's keys, with Cmd on a Mac.
func guiShortcuts() [][2]string {
	if guiMac {
		return [][2]string{
			{"Click a pane", "Type into it"},
			{"⌘N", "New session"},
			{"⌘T", "Toggle the session's terminal"},
			{"⌘R", "Rename session"},
			{"⌘W", "Stop session"},
			{"⌘↑/↓, ⌘⇧[ / ]", "Previous/next session"},
			{"⌘F", "Filter sessions"},
			{"⌘O", "Connect to a host over SSH"},
			{"⌘C / ⌘V", "Copy / paste"},
			{"Right click", "Copy selection, or paste"},
			{"⇧PgUp/PgDn, wheel", "Scroll history"},
			{"⇧drag", "Select in programs that use the mouse"},
			{"⌘= / ⌘- / ⌘0", "Text size"},
			{"⌘B, F2", "Toggle sidebar"},
			{"⌘,", "Settings"},
			{"⌘⇧Q", "Stop all sessions and quit"},
			{"⌘Q, close the window", "Quit; sessions keep running"},
		}
	}
	return [][2]string{
		{"Click a pane", "Type into it"},
		{"Ctrl+Shift+N", "New session"},
		{"Ctrl+Shift+T", "Toggle the session's terminal"},
		{"Ctrl+Shift+R", "Rename session"},
		{"Ctrl+Shift+W", "Stop session"},
		{"Ctrl+Shift+↑/↓, Ctrl+PgUp/PgDn", "Previous/next session"},
		{"Ctrl+Shift+F", "Filter sessions"},
		{"Ctrl+Shift+O", "Connect to a host over SSH"},
		{"Ctrl+Shift+C / V", "Copy / paste"},
		{"Right click", "Copy selection, or paste"},
		{"Shift+PgUp/PgDn, wheel", "Scroll history"},
		{"Shift+drag", "Select in programs that use the mouse"},
		{"Ctrl+Shift+= / - / 0", "Text size"},
		{"Ctrl+Shift+B, F2", "Toggle sidebar"},
		{"Ctrl+Shift+,", "Settings"},
		{"Ctrl+Shift+Q", "Stop all sessions and quit"},
		{"Close the window", "Quit; sessions keep running"},
	}
}

// layoutDialog draws the open dialog over a dimmed window.
func (g *guiApp) layoutDialog(gtx layout.Context) {
	size := gtx.Constraints.Max
	d := &g.dlg
	// The scrim takes the pointer; clicking it cancels a dialog that asks nothing.
	scrim := g.th.Scrim
	paint.FillShape(gtx.Ops, scrim, clip.Rect{Max: size}.Op())
	st := clip.Rect{Max: size}.Push(gtx.Ops)
	event.Op(gtx.Ops, &d.scrim)
	st.Pop()
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &d.scrim, Kinds: pointer.Press})
		if !ok {
			break
		}
		if _, ok := ev.(pointer.Event); ok && (d.kind == dlgHelp || d.kind == dlgSettings) {
			g.closeDialog()
			return
		}
	}

	pad := gtx.Dp(20)
	w := min(size.X-gtx.Dp(32), gtx.Dp(560))
	lineH := gtx.Dp(26)
	var title string
	var body []string
	var ok, alt, cancel string
	danger := false
	switch d.kind {
	case dlgNewSession:
		title, ok, cancel = "New session", "Next", "Cancel"
		if d.wizard.stage == wizardProvider {
			ok, cancel = "Start", "Back"
		}
	case dlgRename:
		title, ok, cancel = "Rename session", "Rename", "Cancel"
	case dlgStop:
		title, ok, cancel, danger = "Stop session", "Stop", "Cancel", true
		body = []string{fmt.Sprintf("Stop %s?", d.target.Name), "Its running program will be terminated."}
	case dlgQuitAll:
		title, ok, cancel, danger = "Stop everything", "Stop all", "Cancel", true
		body = quitAllBody(g.allSessions(), false)
		if g.remote {
			body[0] = strings.Replace(body[0], "session(s)", "session(s) here and on the remote host", 1)
		}
		body[0] = strings.Replace(body[0], " and quit?", " and close lam?", 1)
	case dlgRestore:
		title, ok, alt, cancel = "Restore sessions", "Restore", "Discard", "Later"
		body = restoreDialogBody(d.restore)
	case dlgConnect:
		title, ok, cancel = "Connect to a host", "Connect", "Cancel"
		if d.busy {
			ok = ""
		}
		if g.remote {
			_, host := g.mb.Remote()
			body = []string{"Connected to " + host + ". Leave it empty to disconnect."}
		}
	case dlgAsk:
		title, ok, cancel = d.ask.title, "OK", "Cancel"
		body = []string{d.ask.text}
		if d.ask.yesNo {
			ok = "Trust"
			if d.ask.yesLabel != "" {
				ok = d.ask.yesLabel
			}
		}
	case dlgUpdate:
		title, ok, cancel, body = g.updateDialogText()
	case dlgSettings:
		title, cancel = "Settings", "Done"
	case dlgHelp:
		title, cancel = "Shortcuts", "Close"
	}

	// Content height, to centre the card.
	contentH := len(body) * lineH
	switch d.kind {
	case dlgRename, dlgConnect:
		contentH += gtx.Dp(44)
	case dlgAsk:
		if !d.ask.yesNo {
			contentH += gtx.Dp(44)
		}
		if d.ask.canRemember {
			contentH += gtx.Dp(34)
		}
	case dlgNewSession:
		if d.wizard.stage == wizardWorkdir {
			contentH += gtx.Dp(70)
			if d.wizard.showCompletions {
				contentH += min(len(d.wizard.completionMatches), 8) * lineH
			}
		} else {
			contentH += gtx.Dp(30) + len(d.wizard.providers)*gtx.Dp(42)
		}
	case dlgSettings:
		contentH += guiSettingsRows * gtx.Dp(44)
	case dlgHelp:
		contentH += len(guiShortcuts()) * lineH
	}
	errText := d.err
	if d.kind == dlgNewSession {
		errText = d.wizard.error
	}
	if errText != "" {
		contentH += lineH
	}
	h := pad + gtx.Dp(34) + contentH + gtx.Dp(58)
	top := max(gtx.Dp(16), (size.Y-h)/2)
	card := image.Rect((size.X-w)/2, top, (size.X+w)/2, top+h)
	cardArea := clip.Rect(card).Push(gtx.Ops)
	event.Op(gtx.Ops, &d.cardTag)
	cardArea.Pop()
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: &d.cardTag, Kinds: pointer.Press}); !ok {
			break
		}
	}
	// A soft shadow, then the card with a hairline border.
	for i := 6; i >= 1; i-- {
		sh := color.NRGBA{A: uint8(float32(14+4*(6-i)) * g.th.Shadow)}
		r := card.Inset(-gtx.Dp(unit.Dp(i * 2))).Add(image.Pt(0, gtx.Dp(unit.Dp(i))))
		paint.FillShape(gtx.Ops, sh, clip.UniformRRect(r, gtx.Dp(12)+gtx.Dp(unit.Dp(i*2))).Op(gtx.Ops))
	}
	paint.FillShape(gtx.Ops, g.th.Sidebar, clip.UniformRRect(card, gtx.Dp(12)).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, g.th.Border, clip.Stroke{Path: clip.UniformRRect(card, gtx.Dp(12)).Path(gtx.Ops), Width: float32(max(1, gtx.Dp(1)))}.Op())

	defer op.Offset(card.Min).Push(gtx.Ops).Pop()
	inner := w - 2*pad
	y := pad
	g.drawText(gtx, image.Pt(pad, y), g.th.TitleSize, font.SemiBold, g.th.Text, title, inner)
	y += gtx.Dp(34)
	for _, line := range body {
		g.drawText(gtx, image.Pt(pad, y), g.th.UISize, font.Normal, g.th.TextDim, line, inner)
		y += lineH
	}
	switch d.kind {
	case dlgRename:
		g.drawField(gtx, image.Rect(pad, y, pad+inner, y+gtx.Dp(36)), &d.field, "")
		y += gtx.Dp(44)
	case dlgConnect:
		hint := "user@host, or a name from ~/.ssh/config"
		if d.busy {
			hint = "Connecting…"
		}
		g.drawField(gtx, image.Rect(pad, y, pad+inner, y+gtx.Dp(36)), &d.field, hint)
		y += gtx.Dp(44)
	case dlgAsk:
		if !d.ask.yesNo {
			d.field.secret = true
			g.drawField(gtx, image.Rect(pad, y, pad+inner, y+gtx.Dp(36)), &d.field, "")
			y += gtx.Dp(44)
		}
		if d.ask.canRemember {
			c := g.clickable("remember")
			if c.Clicked(gtx) {
				d.remember = !d.remember
			}
			r := image.Rect(pad, y, pad+inner, y+gtx.Dp(28))
			func() {
				st := op.Offset(r.Min).Push(gtx.Ops)
				defer st.Pop()
				cg := gtx
				cg.Constraints = layout.Exact(r.Size())
				c.Layout(cg, func(gtx layout.Context) layout.Dimensions {
					pointer.CursorPointer.Add(gtx.Ops)
					return layout.Dimensions{Size: r.Size()}
				})
				bx := image.Rect(gtx.Dp(2), r.Dy()/2-gtx.Dp(8), gtx.Dp(18), r.Dy()/2+gtx.Dp(8))
				rr := clip.UniformRRect(bx, gtx.Dp(4))
				if d.remember {
					paint.FillShape(gtx.Ops, g.th.Accent, rr.Op(gtx.Ops))
					w := float32(max(1.5, float64(gtx.Dp(1.6))))
					strokeLine(gtx, f32.Pt(float32(bx.Min.X+gtx.Dp(4)), float32(bx.Min.Y+gtx.Dp(8))), f32.Pt(float32(bx.Min.X+gtx.Dp(7)), float32(bx.Min.Y+gtx.Dp(11))), w, g.th.AccentText)
					strokeLine(gtx, f32.Pt(float32(bx.Min.X+gtx.Dp(7)), float32(bx.Min.Y+gtx.Dp(11))), f32.Pt(float32(bx.Min.X+gtx.Dp(12)), float32(bx.Min.Y+gtx.Dp(5))), w, g.th.AccentText)
				} else {
					paint.FillShape(gtx.Ops, g.th.Border, clip.Stroke{Path: rr.Path(gtx.Ops), Width: float32(max(1, gtx.Dp(1)))}.Op())
				}
				g.drawTextMid(gtx, image.Pt(gtx.Dp(28), r.Dy()/2), g.th.SmallSize, font.Normal, g.th.TextDim, "Remember in the keychain  (Tab)", inner-gtx.Dp(28))
			}()
			y += gtx.Dp(34)
		}
	case dlgNewSession:
		wz := &d.wizard
		if wz.stage == wizardWorkdir {
			g.drawText(gtx, image.Pt(pad, y), g.th.SmallSize, font.Medium, g.th.TextDim, "Directory · Tab completes", inner)
			y += gtx.Dp(22)
			g.drawField(gtx, image.Rect(pad, y, pad+inner, y+gtx.Dp(36)), &d.field, wz.defaultDirDisplay)
			y += gtx.Dp(48)
			if wz.showCompletions {
				for i, label := range directoryCompletionLabels(d.field.String(), wz.completionMatches) {
					if i == 8 {
						break
					}
					g.drawText(gtx, image.Pt(pad+gtx.Dp(10), y), g.th.SmallSize, font.Normal, g.th.TextFaint, label, inner)
					y += lineH
				}
			}
		} else {
			g.drawText(gtx, image.Pt(pad, y), g.th.SmallSize, font.Normal, g.th.TextDim, "in "+wz.workdirDisplay, inner)
			y += gtx.Dp(30)
			for i, p := range wz.providers {
				id := fmt.Sprintf("prov-%d", i)
				r := image.Rect(pad, y, pad+inner, y+gtx.Dp(38))
				c := g.clickable(id)
				for {
					ev, ok := c.Update(gtx)
					if !ok {
						break
					}
					if ev.NumClicks > 0 {
						wz.selectedProvider = i
						if ev.NumClicks == 2 {
							g.confirmDialog()
							return
						}
					}
				}
				selected := i == wz.selectedProvider
				func() {
					st := op.Offset(r.Min).Push(gtx.Ops)
					defer st.Pop()
					cg := gtx
					cg.Constraints = layout.Exact(r.Size())
					c.Layout(cg, func(gtx layout.Context) layout.Dimensions {
						rr := clip.UniformRRect(image.Rectangle{Max: r.Size()}, gtx.Dp(8))
						switch {
						case selected:
							paint.FillShape(gtx.Ops, g.th.Selected, rr.Op(gtx.Ops))
						case c.Hovered():
							paint.FillShape(gtx.Ops, g.th.Hover, rr.Op(gtx.Ops))
						}
						pointer.CursorPointer.Add(gtx.Ops)
						return layout.Dimensions{Size: r.Size()}
					})
					label := p
					if p == "terminal" {
						label = "Terminal"
					}
					col := g.th.TextDim
					if selected {
						col = g.th.Text
					}
					lw := g.drawTextMid(gtx, image.Pt(gtx.Dp(12), r.Dy()/2), g.th.UISize, font.Medium, col, label, inner/2)
					wz.sessions = g.sessionsOn(wz.host)
					g.drawTextMid(gtx, image.Pt(gtx.Dp(24)+lw, r.Dy()/2), g.th.SmallSize, font.Normal, g.th.TextFaint, wz.nameFor(i), inner-lw-gtx.Dp(36))
				}()
				y += gtx.Dp(42)
			}
		}
	case dlgSettings:
		values := g.settingValues()
		for i, row := range values {
			r := image.Rect(pad, y, pad+inner, y+gtx.Dp(40))
			id := fmt.Sprintf("set-%d", i)
			c := g.clickable(id)
			if c.Clicked(gtx) {
				d.row = i
				g.changeSetting(i, 1)
			}
			func() {
				st := op.Offset(r.Min).Push(gtx.Ops)
				defer st.Pop()
				cg := gtx
				cg.Constraints = layout.Exact(r.Size())
				c.Layout(cg, func(gtx layout.Context) layout.Dimensions {
					rr := clip.UniformRRect(image.Rectangle{Max: r.Size()}, gtx.Dp(8))
					if i == d.row || c.Hovered() {
						paint.FillShape(gtx.Ops, g.th.Hover, rr.Op(gtx.Ops))
					}
					pointer.CursorPointer.Add(gtx.Ops)
					return layout.Dimensions{Size: r.Size()}
				})
				g.drawTextMid(gtx, image.Pt(gtx.Dp(12), r.Dy()/2), g.th.UISize, font.Normal, g.th.Text, row[0], inner/2)
				value := "‹ " + row[1] + " ›"
				if i == settingVersion {
					value = row[1]
				}
				g.drawTextRightMid(gtx, image.Pt(r.Dx()-gtx.Dp(12), r.Dy()/2), g.th.UISize, g.th.Accent, value)
			}()
			y += gtx.Dp(44)
		}
	case dlgHelp:
		for _, b := range guiShortcuts() {
			g.drawText(gtx, image.Pt(pad, y), g.th.SmallSize, font.Medium, g.th.Text, b[0], inner/2)
			g.drawText(gtx, image.Pt(pad+inner/2, y), g.th.SmallSize, font.Normal, g.th.TextDim, b[1], inner/2)
			y += lineH
		}
	}
	if errText != "" {
		g.drawText(gtx, image.Pt(pad, y), g.th.SmallSize, font.Normal, g.th.Unread, errText, inner)
	}

	// Buttons, right aligned.
	by := h - pad - gtx.Dp(13)
	x := w - pad
	if ok != "" {
		x -= g.buttonStyled(gtx, "dlg-ok", ok, image.Pt(x, by), true, danger, true) + gtx.Dp(8)
	}
	if alt != "" {
		x -= g.button(gtx, "dlg-alt", alt, image.Pt(x, by), true, true) + gtx.Dp(8)
	}
	g.button(gtx, "dlg-cancel", cancel, image.Pt(x, by), true, false)
	switch {
	case ok != "" && g.clicked(gtx, "dlg-ok"):
		g.confirmDialog()
	case alt != "" && g.clicked(gtx, "dlg-alt"):
		g.discardRestore()
	case g.clicked(gtx, "dlg-cancel"):
		if d.kind == dlgNewSession && d.wizard.stage == wizardProvider {
			d.wizard.stage = wizardWorkdir
			d.field.set(string(d.wizard.pathInput))
			g.resetCompletion()
		} else {
			g.closeDialog()
		}
	}
}

// drawField draws a text field with its caret; placeholder shows while it is empty.
func (g *guiApp) drawField(gtx layout.Context, r image.Rectangle, f *textField, placeholder string) {
	rr := clip.UniformRRect(r, gtx.Dp(8))
	paint.FillShape(gtx.Ops, g.th.Surface, rr.Op(gtx.Ops))
	paint.FillShape(gtx.Ops, g.th.Accent, clip.Stroke{Path: rr.Path(gtx.Ops), Width: float32(max(1, gtx.Dp(1)))}.Op())
	tx, cy := r.Min.X+gtx.Dp(10), r.Min.Y+r.Dy()/2
	maxW := r.Dx() - gtx.Dp(20)
	if len(f.buf) == 0 && placeholder != "" {
		g.drawTextMid(gtx, image.Pt(tx, cy), g.th.UISize, font.Normal, g.th.TextFaint, placeholder, maxW)
	} else {
		g.drawTextMid(gtx, image.Pt(tx, cy), g.th.UISize, font.Normal, g.th.Text, f.shown(), maxW)
	}
	_, cw, _ := g.shape(gtx, g.th.UISize, font.Normal, f.shownBefore(), 1<<20)
	cx := tx + min(cw, maxW)
	paint.FillShape(gtx.Ops, g.th.Text, clip.Rect{Min: image.Pt(cx, cy-gtx.Dp(9)), Max: image.Pt(cx+max(1, gtx.Dp(1)), cy+gtx.Dp(9))}.Op())
}

// rowMenu is the context menu of a session in the sidebar.
type rowMenu struct {
	s    sessionInfo
	at   image.Point     // window position
	rect image.Rectangle // relative to at, set when drawn
}

func (g *guiApp) layoutMenu(gtx layout.Context) {
	m := g.menu
	termLabel := "Open terminal"
	if g.termVisible && m.s.Name == g.selected {
		termLabel = "Close terminal"
	}
	items := []struct {
		label  string
		danger bool
		run    func()
	}{
		{termLabel, false, g.toggleTerminal},
		{"Rename…", false, func() { g.openRename(m.s) }},
		{"New session here…", false, g.openNewSession},
		{"Stop…", true, func() { g.openStop(m.s) }},
	}
	itemH, w, pad := gtx.Dp(36), gtx.Dp(230), gtx.Dp(5)
	h := len(items)*itemH + 2*pad
	size := gtx.Constraints.Max
	at := image.Pt(min(m.at.X, size.X-w-gtx.Dp(4)), min(m.at.Y, size.Y-h-gtx.Dp(4)))
	m.at = at
	m.rect = image.Rect(0, 0, w, h)
	defer op.Offset(at).Push(gtx.Ops).Pop()
	for i := 4; i >= 1; i-- {
		sh := color.NRGBA{A: uint8(float32(16+6*(4-i)) * g.th.Shadow)}
		r := m.rect.Inset(-gtx.Dp(unit.Dp(i * 2))).Add(image.Pt(0, gtx.Dp(unit.Dp(i))))
		paint.FillShape(gtx.Ops, sh, clip.UniformRRect(r, gtx.Dp(8)+gtx.Dp(unit.Dp(i*2))).Op(gtx.Ops))
	}
	paint.FillShape(gtx.Ops, g.th.Surface, clip.UniformRRect(m.rect, gtx.Dp(8)).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, g.th.Border, clip.Stroke{Path: clip.UniformRRect(m.rect, gtx.Dp(8)).Path(gtx.Ops), Width: float32(max(1, gtx.Dp(1)))}.Op())
	for i, it := range items {
		id := fmt.Sprintf("menu-%d", i)
		c := g.clickable(id)
		if c.Clicked(gtx) {
			g.menu = nil
			it.run()
			return
		}
		r := image.Rect(pad, pad+i*itemH, w-pad, pad+(i+1)*itemH)
		func() {
			st := op.Offset(r.Min).Push(gtx.Ops)
			defer st.Pop()
			cg := gtx
			cg.Constraints = layout.Exact(r.Size())
			c.Layout(cg, func(gtx layout.Context) layout.Dimensions {
				if c.Hovered() {
					paint.FillShape(gtx.Ops, g.th.Hover, clip.UniformRRect(image.Rectangle{Max: r.Size()}, gtx.Dp(6)).Op(gtx.Ops))
				}
				pointer.CursorPointer.Add(gtx.Ops)
				return layout.Dimensions{Size: r.Size()}
			})
			col := g.th.Text
			if it.danger {
				col = g.th.Unread
			}
			g.drawTextMid(gtx, image.Pt(gtx.Dp(10), r.Dy()/2), g.th.UISize, font.Normal, col, it.label, r.Dx()-gtx.Dp(20))
		}()
	}
}

func textSizeLabel(size float32, desktop bool) string {
	label := strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.1f", size), "0"), ".")
	if desktop {
		label += " (default)"
	}
	return label
}
