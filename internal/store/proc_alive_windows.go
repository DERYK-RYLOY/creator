//go:build windows

package store

import (
	"time"

	"golang.org/x/sys/windows"
)

// processAlive reports whether pid is running. Failure direction is
// "alive": only the OS positively saying the process is gone marks it
// dead — a permission error, an access-denied or any unexpected failure
// must not mass-orphan live jobs and delegates.
//
// approxStart is the record's best-known start time; a process created
// substantially after it is a recycled PID, not the owner.
func processAlive(pid int, approxStart time.Time) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		// ERROR_INVALID_PARAMETER = the pid does not exist.
		// ERROR_ACCESS_DENIED = exists but protected; still alive.
		return err != windows.ERROR_INVALID_PARAMETER
	}
	defer windows.CloseHandle(handle)
	var created, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exit, &kernel, &user); err != nil {
		// Alive, times unreadable: fail toward alive.
		return true
	}
	if !approxStart.IsZero() && created.Nanoseconds() > approxStart.Add(2*time.Minute).UnixNano() {
		// Process created well after the record was made: the PID was
		// recycled; the owner is gone.
		return false
	}
	return true
}
