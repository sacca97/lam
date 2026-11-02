//go:build gui

package app

import (
	"testing"
	"time"
)

// lam ssh HOST opens the window and connects from inside it: an unknown host
// key is asked about there, since the process has no terminal.
func TestGUIAutoConnectAsksInTheWindow(t *testing.T) {
	useTestBackend(t)
	env := newSSHTestEnv(t)
	hostKey, _ := newTestSigner(t)
	srv := startTestSSHServer(t, hostKey, env.signer.PublicKey())
	t.Cleanup(func() { builtinSSH, guiSSHOptions = false, nil })

	g := newGUIState()
	g.settings = defaultSettings()
	g.windowFocused = true
	g.autoConnect = "tester@" + srv.addr
	d := &guiDriver{t: t, g: g}
	d.frame()
	deadline := time.Now().Add(5 * time.Second)
	for g.dlg.kind != dlgAsk && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		d.frame()
	}
	if g.dlg.kind != dlgAsk || !g.dlg.ask.yesNo {
		t.Fatalf("no host key question: dialog %d", g.dlg.kind)
	}
	if g.autoConnect != "" {
		t.Error("the connection must be started only once")
	}
}
