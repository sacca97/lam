package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Hosts on a tailnet can only be reached while this machine's Tailscale is
// running. The GUI checks that before connecting: it can say a peer is offline
// at once instead of timing out, and can turn Tailscale on after asking.

// tailscaleState is what `tailscale status --json` says, reduced to what lam uses.
type tailscaleState struct {
	Backend string // Running, Stopped, NeedsLogin, ...
	Peers   []tailscalePeer
}

type tailscalePeer struct {
	Host   string // the machine's own name
	DNS    string // its MagicDNS name, without the trailing dot
	IPs    []netip.Addr
	Online bool
}

// tailscaleCLI is the command line client, empty when Tailscale is not installed.
func tailscaleCLI() string {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p
	}
	if runtime.GOOS == "darwin" {
		const app = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"
		if _, err := os.Stat(app); err == nil {
			return app
		}
	}
	return ""
}

// tailscaleInstalled says whether there is a client to ask; replaced in tests.
var tailscaleInstalled = func() bool { return tailscaleCLI() != "" }

// tailscaleRun runs the client; replaced in tests.
var tailscaleRun = func(timeout time.Duration, args ...string) ([]byte, error) {
	cli := tailscaleCLI()
	if cli == "" {
		return nil, fmt.Errorf("tailscale is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return exec.CommandContext(ctx, cli, args...).CombinedOutput()
}

// readTailscale asks the local client for its state.
func readTailscale() (tailscaleState, error) {
	out, err := tailscaleRun(4*time.Second, "status", "--json")
	if err != nil && len(out) == 0 {
		return tailscaleState{}, err
	}
	return parseTailscaleStatus(out)
}

func parseTailscaleStatus(data []byte) (tailscaleState, error) {
	var raw struct {
		BackendState string
		Peer         map[string]struct {
			HostName     string
			DNSName      string
			TailscaleIPs []string
			Online       bool
		}
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return tailscaleState{}, fmt.Errorf("read tailscale status: %w", err)
	}
	st := tailscaleState{Backend: raw.BackendState}
	for _, p := range raw.Peer {
		peer := tailscalePeer{Host: p.HostName, DNS: strings.TrimSuffix(p.DNSName, "."), Online: p.Online}
		for _, s := range p.TailscaleIPs {
			if a, err := netip.ParseAddr(s); err == nil {
				peer.IPs = append(peer.IPs, a)
			}
		}
		st.Peers = append(st.Peers, peer)
	}
	return st, nil
}

// tailnetRange is where Tailscale addresses its machines.
var tailnetRange = netip.MustParsePrefix("100.64.0.0/10")

// looksTailnet says a host name is on a tailnet by its spelling alone: a
// MagicDNS name or a Tailscale address.
func looksTailnet(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if strings.HasSuffix(host, ".ts.net") {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && tailnetRange.Contains(a)
}

// peerFor finds the tailnet machine a host name or address stands for.
func (s tailscaleState) peerFor(host string) (tailscalePeer, bool) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	addr, addrErr := netip.ParseAddr(host)
	for _, p := range s.Peers {
		switch {
		case strings.EqualFold(p.Host, host), strings.EqualFold(p.DNS, host),
			strings.EqualFold(strings.SplitN(p.DNS, ".", 2)[0], host):
			return p, true
		case addrErr == nil:
			for _, ip := range p.IPs {
				if ip == addr {
					return p, true
				}
			}
		}
	}
	return tailscalePeer{}, false
}

// tailscaleUp turns Tailscale on.
func tailscaleUp() error {
	out, err := tailscaleRun(30*time.Second, "up")
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("tailscale up: %s", msg)
	}
	return nil
}

// hostnameOf is the name ssh would connect to for a user's spelling of a host:
// without user and port, and after ~/.ssh/config's HostName.
func hostnameOf(spec string) string {
	if t, err := resolveSSHTarget(spec, SSHPaths{}); err == nil && t.hostname != "" {
		return t.hostname
	}
	if _, h, _, err := parseSSHHost(spec); err == nil {
		return h
	}
	return spec
}
