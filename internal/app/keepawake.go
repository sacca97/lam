package app

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// keepAwakeGrace is how long the machine stays awake after the last sign of
// activity. It matches the window in which a session counts as active.
const keepAwakeGrace = activeOutputWindow

// keepAwakeCommand builds the helper that holds a sleep assertion until pid
// exits, or nil when the platform has none. The assertion blocks idle sleep
// only: the display may still go dark.
var keepAwakeCommand = func(pid int) *exec.Cmd {
	id := strconv.Itoa(pid)
	switch runtime.GOOS {
	case "darwin":
		if path, err := exec.LookPath("caffeinate"); err == nil {
			return exec.Command(path, "-i", "-w", id)
		}
	case "linux":
		inhibit, err := exec.LookPath("systemd-inhibit")
		if err != nil {
			return nil
		}
		if tail, err := exec.LookPath("tail"); err == nil {
			return exec.Command(inhibit, "--what=idle:sleep", "--who=lam",
				"--why=agent session active", "--mode=block", tail, "--pid="+id, "-f", "/dev/null")
		}
	}
	return nil
}

// keepAwake holds a sleep assertion while activity keeps being reported and
// releases it a grace period after the last report. It never polls: Touch is
// called when something happens and a single timer does the releasing.
type keepAwake struct {
	mu       sync.Mutex
	cmd      *exec.Cmd
	starting bool // a helper is being started; Touch must not start another
	timer    *time.Timer
	last     time.Time // the last Touch that was not skipped
	grace    time.Duration
	off      bool // LAM_KEEP_AWAKE=0, read once
	command  func(pid int) *exec.Cmd
	// still, when set, is asked at the end of each grace period whether the
	// activity goes on; if so the assertion is kept for another period.
	still func() bool
}

func newKeepAwake() *keepAwake {
	return &keepAwake{grace: keepAwakeGrace, off: os.Getenv("LAM_KEEP_AWAKE") == "0", command: keepAwakeCommand}
}

// Touch records activity. Repeated calls within a second cost one comparison,
// and the helper is started in the background so a caller on a hot path never
// waits for a fork.
func (k *keepAwake) Touch() {
	if k == nil || k.off {
		return
	}
	now := time.Now()
	k.mu.Lock()
	defer k.mu.Unlock()
	if now.Sub(k.last) < time.Second {
		return
	}
	k.last = now
	if k.cmd == nil && !k.starting {
		k.starting = true
		go k.start()
	}
	if k.timer == nil {
		k.timer = time.AfterFunc(k.grace, k.expire)
	} else {
		k.timer.Reset(k.grace)
	}
}

// start launches the helper that holds the assertion.
func (k *keepAwake) start() {
	cmd := k.command(os.Getpid())
	if cmd != nil {
		// Its own process group, so Release ends the helper and whatever it ran.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if cmd.Start() != nil {
			cmd = nil
		}
	}
	k.mu.Lock()
	k.starting = false
	if cmd == nil {
		k.mu.Unlock()
		return
	}
	k.cmd = cmd
	k.mu.Unlock()
	_ = cmd.Wait()
	// The helper ended by itself (it was refused, or killed from outside): the
	// next Touch must start another instead of believing the assertion holds.
	k.mu.Lock()
	if k.cmd == cmd {
		k.cmd = nil
	}
	k.mu.Unlock()
}

// expire ends the assertion unless activity continued. The check is repeated
// under the lock because a Touch can land while still is being asked.
func (k *keepAwake) expire() {
	if k.rearmIfRecent() {
		return
	}
	if k.still != nil && k.still() {
		k.mu.Lock()
		k.last = time.Now()
		k.timer.Reset(k.grace)
		k.mu.Unlock()
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if since := time.Since(k.last); since < k.grace {
		k.timer.Reset(k.grace - since)
		return
	}
	k.releaseLocked()
}

// rearmIfRecent waits out the rest of the grace period when there was activity
// during it.
func (k *keepAwake) rearmIfRecent() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	since := time.Since(k.last)
	if since >= k.grace {
		return false
	}
	k.timer.Reset(k.grace - since)
	return true
}

// Held reports whether an assertion is currently in place.
func (k *keepAwake) Held() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.cmd != nil
}

// Release drops the assertion at once.
func (k *keepAwake) Release() {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.timer != nil {
		k.timer.Stop()
	}
	k.releaseLocked()
}

func (k *keepAwake) releaseLocked() {
	if k.cmd != nil {
		_ = syscall.Kill(-k.cmd.Process.Pid, syscall.SIGKILL)
		k.cmd = nil
	}
}
