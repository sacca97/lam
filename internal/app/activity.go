package app

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// A session counts as producing output only when it printed at least this
	// much within the meter window: a prompt clock or a blinking cursor stays
	// well below it while a spinner or streamed text is far above.
	activityOutputBytes  = 512
	activityMeterWindow  = 10
	activityCPUInterval  = 2 * time.Second
	activityCPUThreshold = 0.03 // share of one core, over the interval
	clockTicksPerSecond  = 100
)

// activitySignals is everything the host can cheaply tell about a session.
type activitySignals struct {
	Now          time.Time
	LastOutputAt time.Time
	RecentBytes  int    // output within the meter window
	IdlePrompt   bool   // a shell with nothing running in it
	CPUBusy      bool   // the process tree is computing
	AgentStatus  string // what an agent reported about itself, if anything
}

// classifyActivity folds the signals into "active", "quiet" or "" (unknown).
//
//   - Work that burns CPU is active even if it prints nothing, such as a build
//     or a loop inside the shell itself.
//   - A shell sitting at its prompt is quiet however much its prompt redraws.
//   - An agent that reports it is waiting or idle is quiet unless work runs.
//   - Otherwise sustained output means active; a trickle does not.
func classifyActivity(s activitySignals) string {
	if s.CPUBusy {
		return "active"
	}
	if s.IdlePrompt {
		return "quiet"
	}
	switch strings.ToLower(s.AgentStatus) {
	case "waiting", "idle":
		return "quiet"
	case "":
	default:
		return "active"
	}
	if s.LastOutputAt.IsZero() {
		return ""
	}
	if s.Now.Sub(s.LastOutputAt) < activeOutputWindow && s.RecentBytes >= activityOutputBytes {
		return "active"
	}
	return "quiet"
}

// outputMeter counts bytes per second over a short sliding window.
type outputMeter struct {
	mu      sync.Mutex
	seconds [activityMeterWindow]int64 // unix second each bucket belongs to
	bytes   [activityMeterWindow]int
}

func (m *outputMeter) add(n int, now time.Time) int {
	sec := now.Unix()
	m.mu.Lock()
	defer m.mu.Unlock()
	i := int(sec % activityMeterWindow)
	if m.seconds[i] != sec {
		m.seconds[i], m.bytes[i] = sec, 0
	}
	m.bytes[i] += n
	return m.sumLocked(sec)
}

func (m *outputMeter) recent(now time.Time) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sumLocked(now.Unix())
}

func (m *outputMeter) sumLocked(sec int64) int {
	total := 0
	for i := range m.bytes {
		if sec-m.seconds[i] < activityMeterWindow {
			total += m.bytes[i]
		}
	}
	return total
}

// cpuSampler reports whether a process tree used CPU since it last looked. It
// only reads /proc when asked, and at most once per activityCPUInterval.
type cpuSampler struct {
	mu    sync.Mutex
	pid   int
	at    time.Time
	ticks int64
	busy  bool
}

func (c *cpuSampler) Busy(now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pid <= 0 {
		return false
	}
	if !c.at.IsZero() && now.Sub(c.at) < activityCPUInterval {
		return c.busy
	}
	if now.Sub(c.at) > 3*activityCPUInterval {
		// Too long since the last look: an average over the gap would hide a
		// burst or invent one. Start over and answer from the next sample.
		c.at, c.busy = time.Time{}, false
	}
	ticks, ok := processTreeTicks(c.pid)
	if !ok {
		c.busy = false
		return false
	}
	if !c.at.IsZero() {
		elapsed := now.Sub(c.at).Seconds()
		c.busy = float64(ticks-c.ticks) > activityCPUThreshold*elapsed*clockTicksPerSecond
	}
	c.at, c.ticks = now, ticks
	return c.busy
}

// processTreeTicks adds up the CPU time of pid and its descendants, including
// the children they already reaped, so short-lived commands still register.
// It is Linux-only; elsewhere ok is false and CPU is simply not a signal.
func processTreeTicks(pid int) (total int64, ok bool) {
	seen := map[int]bool{}
	var walk func(int, int)
	walk = func(p, depth int) {
		if seen[p] || depth > 8 {
			return
		}
		seen[p] = true
		t, good := procStatTicks(p)
		if !good {
			return
		}
		ok = true
		total += t
		children, _ := procChildren(p)
		for _, child := range children {
			walk(child, depth+1)
		}
	}
	walk(pid, 0)
	return total, ok
}

// procStatFields returns the fields of /proc/<pid>/stat that follow the command
// name (the first is the process state). The name is parenthesized and may
// contain spaces, so parsing starts after its final closing parenthesis. A
// zombie or dead process reports ok false.
func procStatFields(pid int) (fields []string, ok bool) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return nil, false
	}
	end := bytes.LastIndexByte(data, ')')
	if end < 0 {
		return nil, false
	}
	fields = strings.Fields(string(data[end+1:]))
	if len(fields) == 0 || fields[0] == "Z" || fields[0] == "X" {
		return nil, false
	}
	return fields, true
}

// procStatTicks is the CPU time of one process and the children it reaped:
// utime, stime, cutime and cstime are fields 11 to 14.
func procStatTicks(pid int) (int64, bool) {
	f, ok := procStatFields(pid)
	if !ok || len(f) < 15 {
		return 0, false
	}
	var sum int64
	for _, i := range []int{11, 12, 13, 14} {
		v, err := strconv.ParseInt(f[i], 10, 64)
		if err != nil {
			return 0, false
		}
		sum += v
	}
	return sum, true
}

func procChildren(pid int) ([]int, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "task", strconv.Itoa(pid), "children"))
	if err != nil {
		return nil, err
	}
	var out []int
	for _, field := range strings.Fields(string(data)) {
		if n, err := strconv.Atoi(field); err == nil {
			out = append(out, n)
		}
	}
	return out, nil
}

// agentStatus reads what a Claude session reported about itself. The report
// only counts when it is newer than the session.
func agentStatus(name, command string, created time.Time) string {
	if commandName(command) != "claude" {
		return ""
	}
	path := fmt.Sprintf("/tmp/claude-status-%s", name)
	info, err := os.Stat(path)
	if err != nil || info.ModTime().Before(created) {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
