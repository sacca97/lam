package app

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	eventLogMaxBytes  = 128 << 10
	eventLogKeepLines = 400
)

func eventLogPath() string { return filepath.Join(stateDir(), "events.log") }

// logEvent appends a line to the event log, which records why sessions ended
// and who asked for them to be stopped, so an unexpected loss can be traced.
// It is best effort and never fails the caller.
func logEvent(format string, args ...any) {
	if err := os.MkdirAll(stateDir(), 0755); err != nil {
		return
	}
	path := eventLogPath()
	if st, err := os.Stat(path); err == nil && st.Size() > eventLogMaxBytes {
		trimEventLog(path)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s pid=%d %s\n", time.Now().Format("2006-01-02 15:04:05"), os.Getpid(), fmt.Sprintf(format, args...))
}

// trimEventLog keeps only the newest lines.
func trimEventLog(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > eventLogKeepLines {
		lines = lines[len(lines)-eventLogKeepLines:]
	}
	// Also keep the size down, whatever the line lengths.
	size := 0
	for i := len(lines) - 1; i >= 0; i-- {
		size += len(lines[i]) + 1
		if size > eventLogMaxBytes/2 {
			lines = lines[i+1:]
			break
		}
	}
	_ = os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600)
}

// callerNames names the functions that led to the caller, outermost last, for
// the event log. skip is the number of frames above this call to leave out.
func callerNames(skip, depth int) string {
	pcs := make([]uintptr, depth)
	n := runtime.Callers(skip+2, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	var names []string
	for {
		frame, more := frames.Next()
		name := frame.Function
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		names = append(names, strings.TrimPrefix(name, "app."))
		if !more {
			break
		}
	}
	return strings.Join(names, " < ")
}
