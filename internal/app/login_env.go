package app

import (
	"os"
	"runtime"
	"strings"
	"syscall"
)

// enterLoginEnvironment runs the macOS app once more through the user's login
// shell. Started from Finder or Spotlight it has launchd's bare environment:
// no PATH from the shell profile, so no ssh agent, tailscale or agents; and the
// bundle's executable has to be the program itself, not a script, for the app
// to be signed. LAM_LOGIN_ENV marks the second run, which the session hosts it
// starts inherit.
func enterLoginEnvironment() {
	if runtime.GOOS != "darwin" || os.Getenv("LAM_LOGIN_ENV") != "" {
		return
	}
	exe, err := os.Executable()
	if err != nil || !strings.Contains(exe, ".app/Contents/MacOS/") {
		return
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	args := append([]string{shell, "-l", "-c", `exec "$0" "$@"`, exe}, os.Args[1:]...)
	_ = syscall.Exec(shell, args, append(os.Environ(), "LAM_LOGIN_ENV=1")) // on failure, carry on as is
}
