package lazy

import (
	"runtime"
	"strings"
	"time"
)

// goroutineID returns ID of the calling goroutine as shown in stack traces
func goroutineID() string {
	buf := make([]byte, 64)
	buf = buf[:runtime.Stack(buf, false)]

	// "goroutine 35 [running]: ..."
	return strings.Fields(string(buf))[1]
}

// blockedRoutines returns the number of routines created by goroutine
// creatorID, which are blocked for waitReason (e.g. "sync.Mutex.Lock") in
// function fn (e.g. ").reloadIfExpired"). Routines of other tests running in
// parallel can be blocked there too, so they are told apart by their creator.
func blockedRoutines(creatorID, waitReason, fn string) int {
	buf := make([]byte, 1<<16)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}

	count := 0
	for _, routine := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(routine, "["+waitReason) &&
			strings.Contains(routine, fn+"(") &&
			strings.Contains(routine, " in goroutine "+creatorID+"\n") {
			count++
		}
	}

	return count
}

// waitForBlockedRoutines waits until at least n routines created by goroutine
// creatorID are blocked in function fn (see blockedRoutines), so tests do not
// have to guess how long it takes. Returns false when it does not happen in time.
func waitForBlockedRoutines(creatorID, waitReason, fn string, n int) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if blockedRoutines(creatorID, waitReason, fn) >= n {
			return true
		}
		time.Sleep(time.Millisecond)
	}

	return false
}
