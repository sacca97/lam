package app

import (
	"os"
	"os/exec"
	"testing"
)

type shellLifecycleBackend struct {
	busy      bool
	clients   int
	statusErr error
	kills     int
}

func (*shellLifecycleBackend) Name() string                        { return "test" }
func (*shellLifecycleBackend) Create(string, string, string) error { return nil }
func (*shellLifecycleBackend) AttachCmd(string) *exec.Cmd          { return exec.Command("true") }
func (*shellLifecycleBackend) List() ([]sessionInfo, error)        { return nil, nil }
func (backend *shellLifecycleBackend) Kill(string) error {
	backend.kills++
	return nil
}
func (*shellLifecycleBackend) History(string) (string, error)                 { return "", nil }
func (*shellLifecycleBackend) Send(string, string) error                      { return nil }
func (*shellLifecycleBackend) SessionWorkingDirectory(string) (string, error) { return "", nil }
func (backend *shellLifecycleBackend) SessionStatus(string) (bool, int, error) {
	return backend.busy, backend.clients, backend.statusErr
}

func TestCleanupFallbackSession(t *testing.T) {
	tests := []struct {
		name         string
		busy         bool
		clients      int
		ownedClients int
		statusErr    error
		wantPreserve bool
		wantKills    int
	}{
		{name: "idle shell closes", clients: 1, ownedClients: 1, wantKills: 1},
		{name: "running command stays", busy: true, clients: 1, ownedClients: 1, wantPreserve: true},
		{name: "other terminal stays attached", clients: 2, ownedClients: 1, wantPreserve: true},
		{name: "unknown state is preserved", statusErr: errSessionStateUnavailable, wantPreserve: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			previous := selectedBackend
			backend := &shellLifecycleBackend{
				busy: test.busy, clients: test.clients, statusErr: test.statusErr,
			}
			setBackend(backend)
			t.Cleanup(func() { setBackend(previous) })

			preserved, _ := cleanupFallbackSession("shell", test.ownedClients)
			if preserved != test.wantPreserve {
				t.Fatalf("cleanupFallbackSession() preserved = %t, want %t", preserved, test.wantPreserve)
			}
			if backend.kills != test.wantKills {
				t.Fatalf("Kill called %d times, want %d", backend.kills, test.wantKills)
			}
		})
	}
}

func TestDisposeFallbackShellLeavesListedShellSessionAlive(t *testing.T) {
	previous := selectedBackend
	backend := &shellLifecycleBackend{}
	setBackend(backend)
	t.Cleanup(func() { setBackend(previous) })

	ptmx, err := os.CreateTemp(t.TempDir(), "session-pty")
	if err != nil {
		t.Fatal(err)
	}
	em, err := newTermEmu(2, 2, termCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	p := &embedded{
		name:  "work-shell",
		shell: true,
		cmd:   &exec.Cmd{},
		ptmx:  ptmx,
		em:    em,
	}

	preserved, err := disposeFallbackShell(p)
	if err != nil {
		t.Fatal(err)
	}
	if preserved {
		t.Fatal("ordinary listed shell unexpectedly entered fallback preservation")
	}
	if backend.kills != 0 {
		t.Fatalf("ordinary listed shell was killed %d times", backend.kills)
	}
}

var errSessionStateUnavailable = &sessionStateTestError{}

type sessionStateTestError struct{}

func (*sessionStateTestError) Error() string { return "session state unavailable" }
