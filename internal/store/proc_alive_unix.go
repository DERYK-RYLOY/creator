//go:build !windows

package store

import (
	"bytes"
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// processAlive reports whether pid is running (or exists but cannot be
// signalled by this user). approxStart is the record's best-known start
// time; on Linux a process created substantially after it is a recycled
// PID, not the owner.
func processAlive(pid int, approxStart time.Time) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		// EPERM = exists but protected; that still means alive.
		return errors.Is(err, syscall.EPERM)
	}
	// A zombie (or fully dead) process is not an owner: it will never do
	// work again, so its records must be marked orphaned.
	if state, ok := procState(pid); ok && (state == 'Z' || state == 'X' || state == 'x') {
		return false
	}
	// PID reuse guard via /proc starttime (Linux only; others fall back to
	// signal-based existence, which fails toward alive).
	if startedAt, ok := procStartTime(pid); ok && !approxStart.IsZero() {
		if startedAt.After(approxStart.Add(2 * time.Minute)) {
			return false
		}
	}
	return true
}

// procState reads /proc/<pid>/stat field 3 (process state: R, S, Z, ...).
// It is only available on Linux; other platforms report ok=false.
func procState(pid int) (byte, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	idx := bytes.LastIndexByte(data, ')')
	if idx < 0 {
		return 0, false
	}
	fields := strings.Fields(string(data[idx+2:]))
	if len(fields) == 0 || len(fields[0]) == 0 {
		return 0, false
	}
	return fields[0][0], true
}

// procStartTime reads Linux /proc/<pid>/stat field 22 (starttime, in clock
// ticks since boot) and converts it to a wall-clock time.
func procStartTime(pid int) (time.Time, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return time.Time{}, false
	}
	idx := bytes.LastIndexByte(data, ')')
	if idx < 0 {
		return time.Time{}, false
	}
	fields := strings.Fields(string(data[idx+2:]))
	// fields[0] = state, fields[19] = starttime
	if len(fields) <= 19 {
		return time.Time{}, false
	}
	startTicks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	const hz = 100
	btime := 0
	if b, err := os.ReadFile("/proc/stat"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "btime ") {
				f := strings.Fields(l)
				if len(f) == 2 {
					btime, _ = strconv.Atoi(f[1])
				}
			}
		}
	}
	if btime == 0 {
		return time.Time{}, false
	}
	boot := time.Unix(int64(btime), 0)
	return boot.Add(time.Duration(startTicks) * time.Second / hz), true
}
