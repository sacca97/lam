package app

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// launchedAsApp says the program was started as the macOS app, by Finder, the
// Dock, Spotlight or open(1), and not from a terminal: it lives in an app
// bundle, its parent is launchd, and it was given nothing but what the window
// takes (lam ssh HOST). The command line tool inside the bundle, run from a
// shell, and the session hosts it starts, are none of these.
func launchedAsApp() bool {
	if runtime.GOOS != "darwin" || os.Getppid() != 1 {
		return false
	}
	if len(os.Args) > 1 && os.Args[1] != "ssh" {
		return false
	}
	exe, err := os.Executable()
	return err == nil && strings.Contains(exe, ".app/Contents/MacOS/")
}

var envLine = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)

// enterLoginEnvironment gives the macOS app the environment of the user's login
// shell. Started from Finder or Spotlight it has launchd's bare one: no PATH
// from the shell profile, so no ssh agent, tailscale or agents. It reads the
// environment in place rather than starting the program again through the
// shell, because a process that was replaced that way is no longer recognised
// as the app (generic Dock icon, no notifications). Variables already set win.
// LAM_LOGIN_ENV marks it done for the session hosts the app starts.
func enterLoginEnvironment() {
	if os.Getenv("LAM_LOGIN_ENV") != "" || !launchedAsApp() {
		return
	}
	os.Setenv("LAM_LOGIN_ENV", "1")
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, shell, "-l", "-c", "/usr/bin/env").Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		m := envLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		switch name := m[1]; name {
		case "PATH":
			os.Setenv("PATH", mergePATH(os.Getenv("PATH"), strings.Split(m[2], ":")))
		case "PWD", "OLDPWD", "SHLVL", "_":
		default:
			if _, set := os.LookupEnv(name); !set {
				os.Setenv(name, m[2])
			}
		}
	}
}
