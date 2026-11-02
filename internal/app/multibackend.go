package app

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// multiBackend is what the GUI runs on: this machine's sessions, and the
// sessions of one remote host while it is connected, side by side. A remote
// session's name is prefixed with its host ("box/claude-lam"), so every name
// is unique across both machines and each operation finds its own machine from
// the name alone. Local names never contain a slash.
type multiBackend struct {
	local SessionBackend

	mu         sync.Mutex
	remote     SessionBackend
	host       string        // the remote's label, the prefix of its sessions' names
	lastRemote []sessionInfo // the last listing that worked, shown while a link is down
}

func newMultiBackend(local SessionBackend) *multiBackend { return &multiBackend{local: local} }

// splitHost separates a session name into the host it carries, if any, and
// the name on that host.
func splitHost(name string) (host, short string) {
	if i := strings.IndexByte(name, '/'); i > 0 {
		return name[:i], name[i+1:]
	}
	return "", name
}

func hostOfName(name string) string {
	h, _ := splitHost(name)
	return h
}

// shortName is a session's name without its host, for showing.
func shortName(name string) string {
	_, short := splitHost(name)
	return short
}

// hostPrefix is what a name on host starts with; empty for this machine.
func hostPrefix(host string) string {
	if host == "" {
		return ""
	}
	return host + "/"
}

// Remote is the connected remote backend and its label, nil when there is none.
func (m *multiBackend) Remote() (SessionBackend, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.remote, m.host
}

// SetRemote connects b as the remote machine (nil disconnects) and returns the
// one it replaces, for the caller to close.
func (m *multiBackend) SetRemote(b SessionBackend) SessionBackend {
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.remote
	m.remote, m.host, m.lastRemote = b, "", nil
	if b != nil {
		m.host = strings.TrimPrefix(b.Name(), "ssh:")
	}
	return old
}

// route finds the machine a name lives on and the name there.
func (m *multiBackend) route(name string) (SessionBackend, string, error) {
	host, short := splitHost(name)
	if host == "" {
		return m.local, short, nil
	}
	r, h := m.Remote()
	if r == nil || h != host {
		return nil, "", fmt.Errorf("not connected to %s", host)
	}
	return r, short, nil
}

// routeBackend is route for any backend: a plain one owns every name.
func routeBackend(name string) (SessionBackend, string, error) {
	if m, ok := backend().(*multiBackend); ok {
		return m.route(name)
	}
	return backend(), name, nil
}

func (m *multiBackend) Name() string { return m.local.Name() }

func (m *multiBackend) Create(name, cwd, command string) error {
	b, short, err := m.route(name)
	if err != nil {
		return err
	}
	return b.Create(short, cwd, command)
}

// List is this machine's sessions and the remote's. A remote that cannot be
// read does not hide the local ones: its last listing stays, so nothing is torn
// down while the link comes back.
func (m *multiBackend) List() ([]sessionInfo, error) {
	r, host := m.Remote()
	type result struct {
		list []sessionInfo
		err  error
	}
	done := make(chan result, 1)
	if r != nil {
		go func() {
			list, err := r.List()
			done <- result{list, err}
		}()
	}
	list, err := m.local.List()
	if err != nil {
		return nil, err
	}
	if r == nil {
		return list, nil
	}
	res := <-done
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.remote != r {
		return list, nil // replaced while reading
	}
	if res.err == nil {
		m.lastRemote = nil
		for _, s := range res.list {
			s.Name, s.Host = hostPrefix(host)+s.Name, host
			m.lastRemote = append(m.lastRemote, s)
		}
	}
	return append(list, m.lastRemote...), nil
}

func (m *multiBackend) Kill(name string) error {
	b, short, err := m.route(name)
	if err != nil {
		return err
	}
	return b.Kill(short)
}

func (m *multiBackend) History(name string) (string, error) {
	b, short, err := m.route(name)
	if err != nil {
		return "", err
	}
	return b.History(short)
}

func (m *multiBackend) Send(name, text string) error {
	b, short, err := m.route(name)
	if err != nil {
		return err
	}
	return b.Send(short, text)
}

func (m *multiBackend) ClearUnread(name string) error {
	b, short, err := m.route(name)
	if err != nil {
		return err
	}
	if c, ok := b.(interface{ ClearUnread(string) error }); ok {
		return c.ClearUnread(short)
	}
	os.Remove(fmt.Sprintf("/tmp/claude-unread-%s", short))
	return nil
}

func (m *multiBackend) Rename(name, newName string) error {
	b, short, err := m.route(name)
	if err != nil {
		return err
	}
	r, ok := b.(sessionRenamer)
	if !ok {
		return fmt.Errorf("session rename is unavailable")
	}
	_, newShort := splitHost(newName)
	return r.Rename(short, newShort)
}

func (m *multiBackend) SessionWorkingDirectory(name string) (string, error) {
	b, short, err := m.route(name)
	if err != nil {
		return "", err
	}
	in, ok := b.(sessionRuntimeInspector)
	if !ok {
		return "", fmt.Errorf("session working directory is unavailable")
	}
	return in.SessionWorkingDirectory(short)
}

func (m *multiBackend) SessionStatus(name string) (bool, int, error) {
	b, short, err := m.route(name)
	if err != nil {
		return true, 0, err
	}
	in, ok := b.(sessionRuntimeInspector)
	if !ok {
		return true, 0, fmt.Errorf("session process state is unavailable")
	}
	return in.SessionStatus(short)
}

// Close ends the remote connection; this machine's sessions have none.
func (m *multiBackend) Close() {
	if old := m.SetRemote(nil); old != nil {
		if c, ok := old.(interface{ Close() }); ok {
			c.Close()
		}
	}
}
