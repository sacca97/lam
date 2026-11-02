package app

import (
	"errors"
	"reflect"
	"testing"
)

type fakeBackend struct {
	name    string
	list    []sessionInfo
	listErr error
	created []string
	killed  []string
}

func (f *fakeBackend) Name() string { return f.name }
func (f *fakeBackend) Create(name, cwd, command string) error {
	f.created = append(f.created, name)
	return nil
}
func (f *fakeBackend) List() ([]sessionInfo, error) {
	return append([]sessionInfo(nil), f.list...), f.listErr
}
func (f *fakeBackend) Kill(name string) error              { f.killed = append(f.killed, name); return nil }
func (f *fakeBackend) History(name string) (string, error) { return "", nil }
func (f *fakeBackend) Send(name, text string) error        { return nil }

func TestMultiBackendRoutesByHostPrefix(t *testing.T) {
	local := &fakeBackend{name: "native", list: []sessionInfo{{Name: "claude-app", CWD: "/a"}}}
	remote := &fakeBackend{name: "ssh:box", list: []sessionInfo{{Name: "claude-app", CWD: "/a"}}}
	m := newMultiBackend(local)
	m.SetRemote(remote)

	list, err := m.List()
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %v, %v", list, err)
	}
	if list[0].Name != "claude-app" || list[0].Host != "" || list[1].Name != "box/claude-app" || list[1].Host != "box" {
		t.Fatalf("names and hosts: %+v", list)
	}
	if err := m.Kill("box/claude-app"); err != nil {
		t.Fatal(err)
	}
	if err := m.Create("new", "/", "sh"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(remote.killed, []string{"claude-app"}) || !reflect.DeepEqual(local.created, []string{"new"}) || len(local.killed) != 0 {
		t.Fatalf("remote killed %v, local created %v killed %v", remote.killed, local.created, local.killed)
	}
	if err := m.Kill("other/x"); err == nil {
		t.Fatal("a session on a host that is not connected must not reach a machine")
	}
}

func TestMultiBackendKeepsRemoteListingWhileItIsDown(t *testing.T) {
	local := &fakeBackend{name: "native", list: []sessionInfo{{Name: "a"}}}
	remote := &fakeBackend{name: "ssh:box", list: []sessionInfo{{Name: "b"}}}
	m := newMultiBackend(local)
	m.SetRemote(remote)
	if _, err := m.List(); err != nil {
		t.Fatal(err)
	}
	remote.listErr = errors.New("link down")
	list, err := m.List()
	if err != nil || len(list) != 2 || list[1].Name != "box/b" {
		t.Fatalf("a dead remote must keep its last listing and not fail the local one: %v, %v", list, err)
	}
	m.SetRemote(nil)
	if list, _ := m.List(); len(list) != 1 {
		t.Fatalf("after disconnecting only local sessions remain: %v", list)
	}
}

func TestTerminalsAreMatchedPerMachine(t *testing.T) {
	list := []sessionInfo{
		{Name: "claude-app"},
		{Name: "term-claude-app-12345678"},
		{Name: "term-claude-app-abcdefgh", Host: "box"}, // an owner-less terminal on another machine
		{Name: "box/term-claude-app-abcdefgh", Host: "box"},
	}
	got := hideOwnedTerminals(list)
	if len(got) != 3 {
		t.Fatalf("only the local terminal belongs to the local owner: %+v", got)
	}
	if l, ok := terminalSessionLabel("box/term-claude-app-abcdefgh"); !ok || l != "claude-app" {
		t.Fatalf("label = %q, %v", l, ok)
	}
	if terminalLabelFor("box/claude-app") != "claude-app" {
		t.Fatal("a host prefix is not part of a terminal's label")
	}
}

func TestSessionsSortLocalFirstAndGroupPerHost(t *testing.T) {
	a := sessionInfo{Name: "x", CWD: "/p"}
	b := sessionInfo{Name: "box/x", CWD: "/p", Host: "box"}
	if !sessionLess(a, b) || sessionLess(b, a) {
		t.Fatal("this machine's sessions come first")
	}
	rows := buildSidebarRows([]sessionInfo{a, b}, false, nil, 0)
	headers := 0
	for _, r := range rows {
		if r.header {
			headers++
		}
	}
	if headers != 2 {
		t.Fatalf("the same folder on two machines is two projects, got %d headings", headers)
	}
}
