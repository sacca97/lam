package app

import (
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func TestParseShellAgentLaunch(t *testing.T) {
	tests := []struct {
		line     string
		provider string
		want     bool
	}{
		{line: "pi", provider: "pi", want: true},
		{line: "codex --model gpt-5", provider: "codex", want: true},
		{line: `claude "fix the bug"`, provider: "claude", want: true},
		{line: "cd project"},
		{line: "echo pi"},
		{line: "pi | less"},
		{line: "codex > output.txt"},
		{line: "pi $PI_ARGS"},
	}
	for _, test := range tests {
		t.Run(test.line, func(t *testing.T) {
			got, ok := parseShellAgentLaunch([]byte(test.line))
			if ok != test.want {
				t.Fatalf("parseShellAgentLaunch() ok = %t, want %t", ok, test.want)
			}
			if ok && (got.provider != test.provider || got.command != test.line) {
				t.Fatalf("parseShellAgentLaunch() = %#v, want provider %q and exact command %q", got, test.provider, test.line)
			}
		})
	}
}

func TestIsFallbackShellCommand(t *testing.T) {
	if !isFallbackShellCommand(fallbackShellCommand) {
		t.Fatal("fallback shell command was not recognized")
	}
	if isFallbackShellCommand(`exec "${SHELL:-/bin/sh}" -l`) {
		t.Fatal("unrelated shell command was marked as a managed fallback")
	}
}

func TestShellCommandTracker(t *testing.T) {
	t.Run("typed command cancels already echoed line", func(t *testing.T) {
		tracker := shellCommandTracker{trackable: true}
		if _, ok := tracker.Observe([]byte("codex --help")); ok {
			t.Fatal("command launched before Enter")
		}
		launch, ok := tracker.Observe([]byte("\r"))
		if !ok || launch.provider != "codex" || !launch.cancelLine {
			t.Fatalf("Observe() = %#v, %t; want codex launch that cancels the echoed shell line", launch, ok)
		}
	})

	t.Run("command and Enter in one input chunk need no cancellation", func(t *testing.T) {
		tracker := shellCommandTracker{trackable: true}
		launch, ok := tracker.Observe([]byte("pi\r"))
		if !ok || launch.provider != "pi" || launch.cancelLine {
			t.Fatalf("Observe() = %#v, %t; want pi launch without shell cancellation", launch, ok)
		}
	})

	t.Run("CRLF Enter is handled as one key", func(t *testing.T) {
		tracker := shellCommandTracker{trackable: true}
		launch, ok := tracker.Observe([]byte("claude\r\n"))
		if !ok || launch.provider != "claude" || launch.cancelLine {
			t.Fatalf("Observe() = %#v, %t; want claude launch without shell cancellation", launch, ok)
		}
	})

	t.Run("shell syntax is left to the shell", func(t *testing.T) {
		tracker := shellCommandTracker{trackable: true}
		if _, ok := tracker.Observe([]byte("claude | less\r")); ok {
			t.Fatal("a shell pipeline should not be intercepted")
		}
	})

	t.Run("line editing is tracked", func(t *testing.T) {
		tracker := shellCommandTracker{trackable: true}
		launch, ok := tracker.Observe([]byte("codexx\x7f --help\r"))
		if !ok || launch.provider != "codex" || launch.command != "codex --help" {
			t.Fatalf("Observe() = %#v, %t; want edited codex command", launch, ok)
		}
	})
}

func TestTerminalWorkingDirectory(t *testing.T) {
	if got := terminalWorkingDirectory("file://localhost/home/sacca/project%20one"); got != "/home/sacca/project one" {
		t.Fatalf("terminalWorkingDirectory(file URL) = %q", got)
	}
	if got := terminalWorkingDirectory("/tmp/work/.."); got != "/tmp" {
		t.Fatalf("terminalWorkingDirectory(absolute path) = %q", got)
	}
	if got := terminalWorkingDirectory("relative/path"); got != "" {
		t.Fatalf("terminalWorkingDirectory(relative path) = %q, want empty", got)
	}
}

func TestProcessHasLiveChildren(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("process child inspection uses Linux /proc")
	}
	cmd := exec.Command("sh", "-c", "sleep 5 & wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		busy, err := processHasLiveChildren(cmd.Process.Pid)
		if err == nil && busy {
			return
		}
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("running child process was not detected")
}
