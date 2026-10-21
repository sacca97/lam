package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type cliSessionTestBackend struct {
	sessions []sessionInfo
	created  sessionInfo
}

func (*cliSessionTestBackend) Name() string { return "cli-test" }
func (b *cliSessionTestBackend) Create(name, cwd, command string) error {
	b.created = sessionInfo{Name: name, CWD: cwd, Command: command}
	return nil
}
func (*cliSessionTestBackend) AttachCmd(string) *exec.Cmd { return exec.Command("true") }
func (b *cliSessionTestBackend) List() ([]sessionInfo, error) {
	return append([]sessionInfo(nil), b.sessions...), nil
}
func (*cliSessionTestBackend) Kill(string) error              { return nil }
func (*cliSessionTestBackend) History(string) (string, error) { return "", nil }
func (*cliSessionTestBackend) Send(string, string) error      { return nil }

func TestParseSessionStartArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    cliSessionRequest
		wantErr bool
	}{
		{name: "defaults", want: cliSessionRequest{}},
		{
			name: "provider then positional working directory",
			args: []string{"codex", "./repo"},
			want: cliSessionRequest{Provider: "codex", Path: "./repo"},
		},
		{
			name: "provider command after working directory",
			args: []string{"claude", "../repo", "--cmd", "--resume", "last"},
			want: cliSessionRequest{Provider: "claude", Path: "../repo", Command: []string{"--resume", "last"}},
		},
		{
			name: "explicit path and name before command",
			args: []string{"--name", "review", "--path", "/tmp/repo", "codex", "--cmd", "resume"},
			want: cliSessionRequest{Provider: "codex", Path: "/tmp/repo", Name: "review", Command: []string{"resume"}},
		},
		{name: "path alone starts shell", args: []string{"./repo"}, want: cliSessionRequest{Path: "./repo"}},
		{name: "generic command uses cmd option", args: []string{"./repo", "--cmd", "echo", "two words"}, want: cliSessionRequest{Path: "./repo", Command: []string{"echo", "two words"}}},
		{name: "command without provider", args: []string{"--cmd", "echo", "hello"}, want: cliSessionRequest{Command: []string{"echo", "hello"}}},
		{name: "path repeated", args: []string{"--path", "repo", "--path", "other"}, wantErr: true},
		{name: "unknown option", args: []string{"--fast"}, wantErr: true},
		{name: "missing cmd", args: []string{"codex", "--cmd"}, wantErr: true},
		{name: "unexpected positional", args: []string{"codex", "repo", "extra"}, wantErr: true},
		{name: "duplicate provider", args: []string{"codex", "claude"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseSessionStartArgs(test.args)
			if (err != nil) != test.wantErr {
				t.Fatalf("parseSessionStartArgs() error = %v, wantErr %t", err, test.wantErr)
			}
			if err == nil && !reflect.DeepEqual(got, test.want) {
				t.Fatalf("parseSessionStartArgs() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestCreateCLISessionWithoutCommandStartsShellInCurrentDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	previous := selectedBackend
	backend := &cliSessionTestBackend{}
	setBackend(backend)
	t.Cleanup(func() { setBackend(previous) })

	created, err := createCLISession(cliSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != backend.created.Name || created.CWD != dir {
		t.Fatalf("created session info = %#v, want created shell in %q", created, dir)
	}
	if backend.created.CWD != dir {
		t.Fatalf("created cwd = %q, want %q", backend.created.CWD, dir)
	}
	if backend.created.Name != generatedSessionName("shell", dir, nil) {
		t.Fatalf("created name = %q, want shell name for %q", backend.created.Name, dir)
	}
	if backend.created.Command != fallbackShellCommand {
		t.Fatalf("created command = %q, want fallback shell %q", backend.created.Command, fallbackShellCommand)
	}
	if _, err := os.Stat(filepath.Join(stateDir(), "launcher")); !os.IsNotExist(err) {
		t.Fatalf("shell session should not change last-used provider, stat error = %v", err)
	}
}

func TestCreateCLISessionRunsProviderCommandWithExtraArgs(t *testing.T) {
	dir := t.TempDir()
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	previous := selectedBackend
	backend := &cliSessionTestBackend{}
	setBackend(backend)
	t.Cleanup(func() { setBackend(previous) })

	if _, err := createCLISession(cliSessionRequest{Provider: "codex", Command: []string{"resume", "--last"}, Path: dir, Name: "review"}); err != nil {
		t.Fatal(err)
	}
	if backend.created.Name != "review" || backend.created.CWD != dir {
		t.Fatalf("created session = %#v, want name review in %q", backend.created, dir)
	}
	if want := "'codex' 'resume' '--last'"; backend.created.Command != want {
		t.Fatalf("created command = %q, want %q", backend.created.Command, want)
	}
	if got, want := getLauncher(), "codex"; got != want {
		t.Fatalf("last launcher = %q, want %q", got, want)
	}
}

func TestSessionCommandPreservesShellTextAndArgumentBoundaries(t *testing.T) {
	if got, want := sessionCommand([]string{"my command"}), "my command"; got != want {
		t.Fatalf("single command = %q, want %q", got, want)
	}
	if got, want := sessionCommand([]string{"echo", "two words", "it's fine"}), `'echo' 'two words' 'it'\''s fine'`; got != want {
		t.Fatalf("quoted argv command = %q, want %q", got, want)
	}
}

func TestGeneratedSessionNameAvoidsCollisions(t *testing.T) {
	sessions := []sessionInfo{{Name: "session-my-project"}, {Name: "session-my-project-1"}}
	if got, want := generatedSessionName("session", "/work/My Project", sessions), "session-my-project-2"; got != want {
		t.Fatalf("generatedSessionName() = %q, want %q", got, want)
	}
}

// Session hosts started for remote clients live in the daemon's process group.
// If the service manager is allowed to kill that whole group, reinstalling or
// restarting the daemon ends running sessions.
func TestDaemonServiceDefinitionsLeaveSessionsAlone(t *testing.T) {
	if unit := systemdUnitText("/usr/bin/lam"); !strings.Contains(unit, "\nKillMode=process\n") {
		t.Errorf("systemd unit does not set KillMode=process:\n%s", unit)
	}
	plist := launchdPlistText("/usr/bin/lam")
	if !strings.Contains(plist, "<key>AbandonProcessGroup</key>\n    <true/>") {
		t.Errorf("launchd plist does not abandon the process group:\n%s", plist)
	}
	if !strings.Contains(plist, "/usr/bin/lam") || !strings.Contains(systemdUnitText("/usr/bin/lam"), "ExecStart=/usr/bin/lam daemon") {
		t.Error("the executable path is missing from the service definitions")
	}
}

func TestLauncherSessionNameUsesToolAndLastFolder(t *testing.T) {
	for _, tc := range []struct {
		launcher, cwd string
		taken         []string
		want          string
	}{
		{"claude", "/home/me/Projects/os-findmy", nil, "claude-os-findmy"},
		{"claude", "/home/me/Projects/os-findmy", []string{"claude-os-findmy"}, "claude-os-findmy-1"},
		{"claude", "/home/me/Projects/os-findmy", []string{"claude-os-findmy", "claude-os-findmy-1"}, "claude-os-findmy-2"},
		{"codex", "/home/me/Projects/os-findmy", []string{"claude-os-findmy"}, "codex-os-findmy"},
		{"claude", "Projects/test", nil, "claude-test"},
		{"terminal", "/srv/app/", nil, "shell-app"},
	} {
		var sessions []sessionInfo
		for _, name := range tc.taken {
			sessions = append(sessions, sessionInfo{Name: name})
		}
		if got := launcherSessionName(tc.launcher, tc.cwd, sessions); got != tc.want {
			t.Errorf("launcherSessionName(%q, %q, %v) = %q, want %q", tc.launcher, tc.cwd, tc.taken, got, tc.want)
		}
	}
}
