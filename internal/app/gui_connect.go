//go:build gui

package app

import (
	"slices"
	"sync/atomic"
	"time"

	"fmt"
	"image"
	"image/color"
	"math"
	"strings"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"golang.org/x/crypto/ssh"
)

// Connecting to another machine: the host is reached over SSH with lam's own
// client, so what ssh would ask on a terminal (an unknown host key, a key's
// passphrase, a password) is asked in a dialog. The connection is made in the
// background; the window stays usable and the dialog shows the outcome.

// sshAsk is a question the SSH client has for the person.
type sshAsk struct {
	id          int // the connection attempt asking
	title       string
	text        string
	yesNo       bool   // a yes/no question; otherwise a secret to type
	yesLabel    string // the yes button's text; "Trust" when empty
	canRemember bool   // offer to keep the answer in the keychain
	reply       chan sshAnswer
	answer      bool // replied already
}

type sshAnswer struct {
	remember bool
	text     string
	yes      bool
	cancel   bool
}

// connectResult is a finished connection attempt, waiting for the UI goroutine.
type connectResult struct {
	id       int
	host     string
	b        *sshSessionBackend
	err      error
	tailnet  string // the host's name when it is a machine of the tailnet
	autoTSUp bool   // the person said to turn Tailscale on without asking
}

// wake redraws the window after something happened off the UI goroutine.
func (g *guiApp) wake() {
	if g.win != nil {
		g.win.Invalidate()
	}
}

func (g *guiApp) openConnect() {
	d := dialog{kind: dlgConnect}
	d.field.set(g.settings.GUILastHost)
	g.dlg = d
}

// startConnect connects to host from the connect dialog. An empty host, or
// "local", goes back to this machine's own sessions.
func (g *guiApp) startConnect(host string) {
	host = strings.TrimSpace(host)
	d := &g.dlg
	if host == "" || host == "local" {
		g.connID++
		g.closeDialog()
		g.switchBackend(nil)
		return
	}
	if err := validateSSHHost(host); err != nil {
		d.err = err.Error()
		return
	}
	g.connID++
	id := g.connID
	d.busy, d.err = true, ""
	builtinSSH = true
	usedStored := new(atomic.Value) // the keychain account tried
	guiSSHOptions = func(unattended bool) SSHOptions { return g.sshOptions(id, unattended, usedStored) }
	knownTailnet := append([]string(nil), g.settings.TailnetHosts...)
	autoUp := g.settings.TailscaleAutoUp
	go func() {
		tailnet, enableAuto, err := g.prepareTailnet(id, host, knownTailnet, autoUp)
		var b *sshSessionBackend
		if err == nil {
			b, err = newSSHSessionBackendKeysFirst(host)
		}
		if account, _ := usedStored.Load().(string); err != nil && account != "" && strings.Contains(err.Error(), "unable to authenticate") {
			vaultDelete(account) // the remembered password was refused: forget it
		}
		g.mu.Lock()
		g.connRes = &connectResult{id: id, host: host, b: b, err: err, tailnet: tailnet, autoTSUp: enableAuto}
		g.mu.Unlock()
		g.wake()
	}()
}

// prepareTailnet makes sure Tailscale can carry the connection when the host is
// on a tailnet: an offline peer is reported at once, and a stopped Tailscale is
// turned on, after asking unless the person allowed it before. It returns the
// host's name when it is a tailnet machine, so later connects know it even
// while Tailscale is off. Without Tailscale, or when its state cannot be read,
// it does nothing and the connection is tried as it is.
func (g *guiApp) prepareTailnet(id int, spec string, known []string, auto bool) (tailnet string, enableAuto bool, err error) {
	if !tailscaleInstalled() {
		return "", false, nil
	}
	name := hostnameOf(spec)
	isTailnet := looksTailnet(name) || slices.Contains(known, name)
	st, rerr := readTailscale()
	if rerr != nil {
		return "", false, nil
	}
	switch st.Backend {
	case "Running":
		if p, ok := st.peerFor(name); ok {
			if !p.Online {
				return name, false, fmt.Errorf("%s is offline in your tailnet", name)
			}
			return name, false, nil
		}
		return "", false, nil
	case "NeedsLogin", "NeedsMachineAuth":
		if isTailnet {
			return "", false, fmt.Errorf("Tailscale needs you to log in: open the Tailscale app, then connect again")
		}
		return "", false, nil
	}
	if !isTailnet {
		return "", false, nil
	}
	tailnet = name
	if !auto {
		a := g.ask(&sshAsk{
			id:       id,
			title:    "Tailscale is off",
			yesLabel: "Turn on",
			text:     name + " is on your tailnet, which needs Tailscale. Turn it on? lam will do this by itself next time.",
			yesNo:    true,
		})
		if a.cancel || !a.yes {
			return "", false, fmt.Errorf("Tailscale is off")
		}
		enableAuto = true
	}
	if err := tailscaleUp(); err != nil {
		return "", false, err
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s, err := readTailscale(); err == nil && s.Backend == "Running" {
			return tailnet, enableAuto, nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return "", false, fmt.Errorf("Tailscale did not come up: open the Tailscale app to see why")
}

// sshOptions are the client's questions, asked in the window. They are called
// from the connecting goroutine and block until answered.
func (g *guiApp) sshOptions(id int, unattended bool, usedStored *atomic.Value) SSHOptions {
	// A remembered password, when there is one; public keys come first anyway.
	stored := func(user, host string) (string, bool) {
		if !vaultAvailable() {
			return "", false
		}
		account := vaultAccount(user, host)
		pw, ok := vaultLookup(account)
		if ok {
			usedStored.Store(account)
		}
		return pw, ok
	}
	if unattended {
		return SSHOptions{Password: func(user, host string) (string, error) {
			if pw, ok := stored(user, host); ok {
				return pw, nil
			}
			return "", fmt.Errorf("no remembered password")
		}}
	}
	return SSHOptions{
		ConfirmHost: func(host string, key ssh.PublicKey) (bool, error) {
			a := g.ask(&sshAsk{
				id:    id,
				title: "Unknown host",
				text:  fmt.Sprintf("%s has not been seen before. Its %s key fingerprint is %s. Trust it?", host, key.Type(), ssh.FingerprintSHA256(key)),
				yesNo: true,
			})
			if a.cancel {
				return false, fmt.Errorf("host key not confirmed")
			}
			return a.yes, nil
		},
		Passphrase: func(path string) ([]byte, error) {
			a := g.ask(&sshAsk{id: id, title: "Key passphrase", text: "Passphrase for " + path})
			if a.cancel {
				return nil, fmt.Errorf("no passphrase given")
			}
			return []byte(a.text), nil
		},
		Password: func(user, host string) (string, error) {
			if pw, ok := stored(user, host); ok {
				return pw, nil
			}
			a := g.ask(&sshAsk{id: id, title: "Password", text: fmt.Sprintf("Password for %s@%s. Your keys were not accepted, or none was found.", user, host), canRemember: vaultAvailable()})
			if a.cancel {
				return "", fmt.Errorf("no password given")
			}
			if a.remember {
				_ = vaultStore(vaultAccount(user, host), a.text)
			}
			return a.text, nil
		},
	}
}

func (g *guiApp) ask(a *sshAsk) sshAnswer {
	a.reply = make(chan sshAnswer, 1)
	g.mu.Lock()
	g.askReq = a
	g.mu.Unlock()
	g.wake()
	return <-a.reply
}

// answerAsk replies to the open question, once.
func (g *guiApp) answerAsk(a sshAnswer) {
	q := g.dlg.ask
	if q == nil || q.answer {
		return
	}
	q.answer = true
	q.reply <- a
}

// pollConnect brings questions and results from the connecting goroutine into
// the interface. It runs at the start of each frame.
func (g *guiApp) pollConnect() {
	g.mu.Lock()
	q, res := g.askReq, g.connRes
	g.askReq, g.connRes = nil, nil
	g.mu.Unlock()
	if q != nil {
		if q.id != g.connID || g.dlg.kind != dlgConnect {
			// An abandoned attempt, or something else is open: say no.
			q.answer = true
			q.reply <- sshAnswer{cancel: true}
		} else {
			g.askPrev = g.dlg
			g.dlg = dialog{kind: dlgAsk, ask: q}
		}
	}
	if res == nil {
		return
	}
	if res.id != g.connID || res.err != nil && g.dlg.kind != dlgConnect && g.dlg.kind != dlgAsk {
		// Cancelled, or failed with the dialog already closed.
		if res.err == nil {
			go res.b.Close()
		} else if res.id == g.connID {
			g.say("Connect %s: %v", res.host, res.err)
		}
		return
	}
	if res.err != nil {
		if g.dlg.kind == dlgAsk {
			g.dlg = g.askPrev
		}
		g.dlg.busy, g.dlg.err = false, res.err.Error()
		return
	}
	g.closeDialog()
	g.settings.GUILastHost = res.host
	if res.tailnet != "" && !slices.Contains(g.settings.TailnetHosts, res.tailnet) {
		g.settings.TailnetHosts = append(g.settings.TailnetHosts, res.tailnet)
	}
	if res.autoTSUp {
		g.settings.TailscaleAutoUp = true
	}
	saveSettings(g.settings)
	g.switchBackend(res.b)
}

// switchBackend connects the window to b as its remote host (nil leaves the
// current one). This machine's sessions stay where they are. The sessions on a
// host being left keep running; only this window's views of them are dropped.
func (g *guiApp) switchBackend(b SessionBackend) {
	if cur, _ := g.mb.Remote(); cur == b {
		return
	}
	_, oldHost := g.mb.Remote()
	if oldHost != "" {
		if g.main != nil && strings.HasPrefix(g.main.name, hostPrefix(oldHost)) {
			g.closeMain()
		}
		for owner, p := range g.terminals {
			if strings.HasPrefix(owner, hostPrefix(oldHost)) {
				if g.termView.pane == p {
					g.termView.attach(nil)
				}
				delete(g.terminals, owner)
				go p.close()
			}
		}
		for n := range g.attachFail {
			if strings.HasPrefix(n, hostPrefix(oldHost)) {
				delete(g.attachFail, n)
			}
		}
		if strings.HasPrefix(g.selected, hostPrefix(oldHost)) {
			g.selected, g.termVisible = "", false
		}
	}
	g.gen.Add(1) // a refresh of the old host in flight must not land
	old := g.mb.SetRemote(b)
	g.gen.Add(1)
	if c, ok := old.(interface{ Close() }); ok {
		go c.Close()
	}
	g.remote = b != nil
	g.finishes.reset()
	g.mu.Lock()
	g.sessions = slices.DeleteFunc(slices.Clone(g.sessions), func(s sessionInfo) bool { return s.Host != "" })
	g.rowsVer.Add(1)
	g.mu.Unlock()
	if b != nil {
		if src, ok := b.(sessionDirectorySource); ok {
			go src.HomeDirectory()
		}
		g.say("Connected to %s", strings.TrimPrefix(b.Name(), "ssh:"))
	} else {
		g.say("Disconnected from %s", oldHost)
	}
	go g.refreshSessions()
	g.wake()
}

// iconServer is the connect icon: two stacked machines.
func iconServer(gtx layout.Context, c image.Point, r float32, col color.NRGBA) {
	x, y := float32(c.X), float32(c.Y)
	w := float32(math.Max(1.3, float64(r)/6))
	hw, hh := r, r*0.42
	for i := -1; i <= 1; i += 2 {
		cy := y + float32(i)*r*0.52
		rect := image.Rect(int(x-hw), int(cy-hh), int(x+hw), int(cy+hh))
		paint.FillShape(gtx.Ops, col, clip.Stroke{Path: clip.UniformRRect(rect, int(r*0.25)).Path(gtx.Ops), Width: w}.Op())
		dot := int(math.Max(1, float64(r)/6))
		dx := int(x - hw*0.5)
		paint.FillShape(gtx.Ops, col, clip.Ellipse{Min: image.Pt(dx-dot, int(cy)-dot), Max: image.Pt(dx+dot, int(cy)+dot)}.Op(gtx.Ops))
		strokeLine(gtx, f32.Pt(x+hw*0.1, cy), f32.Pt(x+hw*0.6, cy), w, col)
	}
}
