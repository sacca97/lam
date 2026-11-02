//go:build gui

package app

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func fakeTailscale(t *testing.T, backend string, online bool) (calls *[]string) {
	oldRun, oldInstalled := tailscaleRun, tailscaleInstalled
	t.Cleanup(func() { tailscaleRun, tailscaleInstalled = oldRun, oldInstalled })
	tailscaleInstalled = func() bool { return true }
	calls = new([]string)
	tailscaleRun = func(_ time.Duration, args ...string) ([]byte, error) {
		*calls = append(*calls, args[0])
		switch args[0] {
		case "up":
			backend = "Running"
			return nil, nil
		case "status":
			return []byte(fmt.Sprintf(`{"BackendState":%q,"Peer":{"k":{"HostName":"nas","DNSName":"nas.t.ts.net.","TailscaleIPs":["100.90.0.5"],"Online":%v}}}`, backend, online)), nil
		}
		return nil, fmt.Errorf("unexpected %v", args)
	}
	return calls
}

func TestPrepareTailnetTurnsTailscaleOn(t *testing.T) {
	calls := fakeTailscale(t, "Stopped", true)
	g := newGUIState()
	name, enable, err := g.prepareTailnet(1, "me@100.90.0.5", nil, true)
	if err != nil || name != "100.90.0.5" || enable {
		t.Fatalf("name %q enable %v err %v", name, enable, err)
	}
	if !strings.Contains(strings.Join(*calls, " "), "up") {
		t.Fatalf("tailscale up was not run: %v", *calls)
	}
}

func TestPrepareTailnetLeavesOtherHostsAlone(t *testing.T) {
	calls := fakeTailscale(t, "Stopped", true)
	g := newGUIState()
	if name, _, err := g.prepareTailnet(1, "me@example.org", nil, true); err != nil || name != "" {
		t.Fatalf("name %q err %v", name, err)
	}
	for _, c := range *calls {
		if c == "up" {
			t.Fatal("Tailscale must not be turned on for a host that is not on the tailnet")
		}
	}
}

func TestPrepareTailnetReportsOfflinePeer(t *testing.T) {
	fakeTailscale(t, "Running", false)
	g := newGUIState()
	if _, _, err := g.prepareTailnet(1, "nas", nil, false); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("err = %v", err)
	}
}
