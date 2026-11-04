package app

import (
	"slices"
	"testing"
)

// Sessions tell Pi to send copies to the terminal, whatever it inherited.
func TestHostEnvAsksPiForOSC52(t *testing.T) {
	t.Setenv("PI_CLIPBOARD_OSC52", "0")
	t.Setenv("TERM", "dumb")
	env := hostEnv()
	if !slices.Contains(env, "PI_CLIPBOARD_OSC52=1") || slices.Contains(env, "PI_CLIPBOARD_OSC52=0") {
		t.Fatalf("PI_CLIPBOARD_OSC52 not forced on: %v", env)
	}
	if !slices.Contains(env, "TERM=xterm-256color") {
		t.Fatal("TERM was not set")
	}
}
