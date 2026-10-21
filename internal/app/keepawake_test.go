package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func stubKeepAwake(t *testing.T) *atomic.Int32 {
	t.Helper()
	var starts atomic.Int32
	previous := keepAwakeCommand
	keepAwakeCommand = func(int) *exec.Cmd { starts.Add(1); return exec.Command("sleep", "30") }
	t.Cleanup(func() { keepAwakeCommand = previous })
	return &starts
}

// waitHeld waits for the background start to settle.
func waitHeld(k *keepAwake, want bool) bool {
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if k.Held() == want {
			return true
		}
	}
	return k.Held() == want
}

func TestKeepAwakeHoldsWhileActiveAndReleasesAfterGrace(t *testing.T) {
	starts := stubKeepAwake(t)
	k := newKeepAwake()
	k.grace = 80 * time.Millisecond
	defer k.Release()

	k.Touch()
	k.Touch() // within a second: no second helper
	if !waitHeld(k, true) || starts.Load() != 1 {
		t.Fatalf("held=%v starts=%d", k.Held(), starts.Load())
	}
	if !waitHeld(k, false) {
		t.Fatal("assertion outlived the grace period")
	}
	time.Sleep(1100 * time.Millisecond) // past the once-a-second throttle
	k.Touch()
	if !waitHeld(k, true) || starts.Load() != 2 {
		t.Fatalf("not retaken: held=%v starts=%d", k.Held(), starts.Load())
	}
}

func TestKeepAwakeCanBeDisabled(t *testing.T) {
	starts := stubKeepAwake(t)
	t.Setenv("LAM_KEEP_AWAKE", "0")
	k := newKeepAwake()
	k.Touch()
	if k.Held() || starts.Load() != 0 {
		t.Fatal("disabled keep-awake started a helper")
	}
}

func TestKeepAwakeWithoutHelperIsHarmless(t *testing.T) {
	previous := keepAwakeCommand
	keepAwakeCommand = func(int) *exec.Cmd { return nil }
	defer func() { keepAwakeCommand = previous }()
	k := newKeepAwake()
	k.Touch()
	if k.Held() {
		t.Fatal("held without a helper")
	}
}

func TestKeepAwakeReleaseEndsTheHelpersChildToo(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	previous := keepAwakeCommand
	keepAwakeCommand = func(int) *exec.Cmd {
		return exec.Command("sh", "-c", "sleep 30 & echo $! > "+pidFile+"; wait")
	}
	t.Cleanup(func() { keepAwakeCommand = previous })
	k := newKeepAwake()
	k.Touch()
	waitHeld(k, true)
	var child int
	for deadline := time.Now().Add(2 * time.Second); child == 0 && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if data, err := os.ReadFile(pidFile); err == nil {
			child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		}
	}
	if child == 0 {
		t.Fatal("the helper never started its child")
	}
	k.Release()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if syscall.Kill(child, 0) != nil {
			return
		}
	}
	syscall.Kill(child, syscall.SIGKILL)
	t.Fatal("the helper's child outlived Release")
}

func TestKeepAwakeRestartsAHelperThatEndedByItself(t *testing.T) {
	var starts atomic.Int32
	previous := keepAwakeCommand
	keepAwakeCommand = func(int) *exec.Cmd {
		if starts.Add(1) == 1 {
			return exec.Command("true") // refused: exits at once
		}
		return exec.Command("sleep", "30")
	}
	t.Cleanup(func() { keepAwakeCommand = previous })
	k := newKeepAwake()
	defer k.Release()
	k.Touch()
	if !waitHeld(k, false) {
		t.Fatal("a helper that exited is still counted as holding")
	}
	time.Sleep(1100 * time.Millisecond)
	k.Touch()
	if !waitHeld(k, true) || starts.Load() != 2 {
		t.Fatalf("not restarted: held=%v starts=%d", k.Held(), starts.Load())
	}
}

func TestKeepAwakeActivityDuringTheCheckKeepsTheAssertion(t *testing.T) {
	stubKeepAwake(t)
	k := newKeepAwake()
	k.grace = 150 * time.Millisecond
	k.still = func() bool {
		// Fresh activity arrives while the host is being asked.
		k.mu.Lock()
		k.last = time.Now()
		k.mu.Unlock()
		return false
	}
	defer k.Release()
	k.Touch()
	waitHeld(k, true)
	time.Sleep(400 * time.Millisecond) // the timer fires and asks, twice
	if !k.Held() {
		t.Fatal("the assertion was released although activity had just been recorded")
	}
}
