package main

import (
	"sync/atomic"
	"testing"
	"time"
)

// systray.Quit runs onExit on its caller's goroutine while the tray loop
// returns at once; main must still wait for onExit (#14), and run it once
// when the loop calls it too.
func TestRunUntilExitedWaitsForOnExit(t *testing.T) {
	var calls, finished atomic.Int32
	fakeRun := func(onReady, onExit func()) {
		onReady()
		go onExit() // Quit's goroutine
		go onExit() // the loop's WM_DESTROY, racing it
	} // the loop returns before onExit is done
	runUntilExited(fakeRun, func() {}, func() {
		calls.Add(1)
		time.Sleep(100 * time.Millisecond) // unmounting, starting the installer
		finished.Add(1)
	}, 5*time.Second)
	if finished.Load() != 1 || calls.Load() != 1 {
		t.Fatalf("returned with onExit called %d times, finished %d times", calls.Load(), finished.Load())
	}
}

// A loop that ends without ever calling onExit doesn't hang the exit.
func TestRunUntilExitedGivesUp(t *testing.T) {
	start := time.Now()
	runUntilExited(func(onReady, onExit func()) {}, func() {}, func() {}, 50*time.Millisecond)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("waited %v", d)
	}
}
