package app

import (
	"log"
	"runtime/debug"
	"time"
)

// runWorkerTask executes a queue task body and converts a panic into a logged
// error instead of crashing the whole bot. Unrecovered panics in any goroutine
// terminate the process, so every long-lived worker must go through this.
func runWorkerTask(name string, fn func()) {
	defer recoverWorkerPanic(name)
	fn()
}

// recoverWorkerPanic is the deferred part of runWorkerTask; it is also used
// directly inside per-task closures.
func recoverWorkerPanic(name string) {
	if r := recover(); r != nil {
		log.Printf("worker panic recovered worker=%s err=%v stack=%s", name, r, clipText(string(debug.Stack()), 2000))
	}
}

// runSupervisedLoop runs fn (the Telegram update loop) and re-enters it after a
// panic. Without this, one panic in any inline handler kills the process and
// drops all in-memory state (reaction counters, caches, queues).
func runSupervisedLoop(name string, fn func()) {
	for {
		panicked := false
		func() {
			defer func() {
				if r := recover(); r != nil {
					panicked = true
					log.Printf("%s panic: %v\n%s", name, r, debug.Stack())
				}
			}()
			fn()
		}()
		if !panicked {
			return
		}
		time.Sleep(2 * time.Second)
	}
}
