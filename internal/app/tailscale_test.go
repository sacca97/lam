package app

import "testing"

const tailscaleJSON = `{"BackendState":"Running","Peer":{
 "k1":{"HostName":"buildbox","DNSName":"buildbox.tail1234.ts.net.","TailscaleIPs":["100.101.102.103","fd7a::1"],"Online":true},
 "k2":{"HostName":"nas","DNSName":"nas.tail1234.ts.net.","TailscaleIPs":["100.90.0.5"],"Online":false}}}`

func TestTailscaleStatusAndPeers(t *testing.T) {
	st, err := parseTailscaleStatus([]byte(tailscaleJSON))
	if err != nil || st.Backend != "Running" || len(st.Peers) != 2 {
		t.Fatalf("parse: %+v, %v", st, err)
	}
	for _, host := range []string{"buildbox", "buildbox.tail1234.ts.net", "100.101.102.103", "BuildBox"} {
		if p, ok := st.peerFor(host); !ok || !p.Online {
			t.Errorf("%q should be the online peer buildbox, got %+v %v", host, p, ok)
		}
	}
	if p, ok := st.peerFor("nas"); !ok || p.Online {
		t.Errorf("nas is a peer that is offline: %+v %v", p, ok)
	}
	if _, ok := st.peerFor("example.org"); ok {
		t.Error("an unrelated host is not a peer")
	}
}

func TestLooksTailnet(t *testing.T) {
	for host, want := range map[string]bool{
		"box.tail1234.ts.net": true, "100.64.0.1": true, "100.127.255.1": true,
		"100.128.0.1": false, "192.168.1.5": false, "example.org": false, "box": false,
	} {
		if got := looksTailnet(host); got != want {
			t.Errorf("looksTailnet(%q) = %v", host, got)
		}
	}
}
