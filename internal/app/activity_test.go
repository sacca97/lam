package app

import (
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"
)

func TestClassifyActivity(t *testing.T) {
	now := time.Now()
	recent := now.Add(-2 * time.Second)
	stale := now.Add(-time.Minute)
	for name, tc := range map[string]struct {
		in   activitySignals
		want string
	}{
		"unknown before any output":        {activitySignals{Now: now}, ""},
		"sustained output":                 {activitySignals{Now: now, LastOutputAt: recent, RecentBytes: 4000}, "active"},
		"a trickle is not activity":        {activitySignals{Now: now, LastOutputAt: recent, RecentBytes: 40}, "quiet"},
		"old output":                       {activitySignals{Now: now, LastOutputAt: stale, RecentBytes: 4000}, "quiet"},
		"idle prompt beats redraws":        {activitySignals{Now: now, LastOutputAt: recent, RecentBytes: 4000, IdlePrompt: true}, "quiet"},
		"silent build is active":           {activitySignals{Now: now, LastOutputAt: stale, CPUBusy: true}, "active"},
		"waiting agent is quiet":           {activitySignals{Now: now, LastOutputAt: recent, RecentBytes: 4000, AgentStatus: "waiting"}, "quiet"},
		"waiting agent with busy children": {activitySignals{Now: now, LastOutputAt: recent, CPUBusy: true, AgentStatus: "waiting"}, "active"},
		"agent reports work":               {activitySignals{Now: now, LastOutputAt: stale, AgentStatus: "working"}, "active"},
		"busy shell with no child":         {activitySignals{Now: now, IdlePrompt: true, CPUBusy: true}, "active"},
	} {
		if got := classifyActivity(tc.in); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

func TestOutputMeterSlidesOffOldBytes(t *testing.T) {
	var m outputMeter
	t0 := time.Unix(1000, 0)
	m.add(300, t0)
	if got := m.add(300, t0.Add(3*time.Second)); got != 600 {
		t.Fatalf("sum = %d", got)
	}
	if got := m.recent(t0.Add(11 * time.Second)); got != 300 {
		t.Fatalf("after the first bucket expired: %d", got)
	}
	if got := m.recent(t0.Add(30 * time.Second)); got != 0 {
		t.Fatalf("after everything expired: %d", got)
	}
}

func TestCPUSamplerSeesBusyAndIdleProcesses(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc")
	}
	spin := exec.Command("sh", "-c", "while :; do :; done")
	if err := spin.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { spin.Process.Kill(); spin.Wait() }()
	sleeper := exec.Command("sleep", "30")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { sleeper.Process.Kill(); sleeper.Wait() }()

	busy := &cpuSampler{pid: spin.Process.Pid}
	idle := &cpuSampler{pid: sleeper.Process.Pid}
	now := time.Now()
	busy.Busy(now)
	idle.Busy(now)
	time.Sleep(600 * time.Millisecond)
	later := now.Add(activityCPUInterval + time.Second) // the sampler's own clock
	if !busy.Busy(later) {
		t.Error("a spinning process was not seen as busy")
	}
	if idle.Busy(later) {
		t.Error("a sleeping process was seen as busy")
	}
}

func TestKeepAwakeStillAsksWhetherToContinue(t *testing.T) {
	stubKeepAwake(t)
	k := newKeepAwake()
	k.grace = 60 * time.Millisecond
	var working atomic.Bool
	working.Store(true)
	k.still = func() bool { return working.Load() }
	defer k.Release()
	k.Touch()
	time.Sleep(200 * time.Millisecond)
	if !k.Held() {
		t.Fatal("released while the work went on")
	}
	working.Store(false)
	time.Sleep(200 * time.Millisecond)
	if k.Held() {
		t.Fatal("kept after the work stopped")
	}
}

func TestLinkStatusText(t *testing.T) {
	now := time.Now()
	got := linkStatusText(linkStatus{Attempt: 4, RetryAt: now.Add(3500 * time.Millisecond),
		Err: "the remote lam closed the connection: EOF (ssh: connect to host x port 22: Network is unreachable)"}, now)
	want := "Disconnected, reconnecting in 4s (attempt 4) · ssh: connect to host x port 22: Network is unreachable"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if got := linkStatusText(linkStatus{Attempt: 1, Err: "connection timed out"}, now); got != "Disconnected, reconnecting… · connection timed out" {
		t.Errorf("first attempt: %q", got)
	}
}

func TestCPUSamplerDoesNotAverageOverALongGap(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc")
	}
	spin := exec.Command("sh", "-c", "while :; do :; done")
	if err := spin.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { spin.Process.Kill(); spin.Wait() }()
	s := &cpuSampler{pid: spin.Process.Pid}
	now := time.Now()
	s.Busy(now)
	time.Sleep(300 * time.Millisecond)
	// Five minutes later the old sample is worthless: start over rather than
	// dividing a few CPU seconds by the whole gap.
	if s.Busy(now.Add(5 * time.Minute)) {
		t.Error("answered from an average over the gap")
	}
	time.Sleep(600 * time.Millisecond)
	if !s.Busy(now.Add(5*time.Minute + activityCPUInterval + time.Second)) {
		t.Error("the spinning process was not seen after the fresh baseline")
	}
}
