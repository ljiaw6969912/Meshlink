//go:build windows

package main

import (
	"runtime"
	"testing"
)

func TestDesktopRapidLayoutAndClose(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// Closing while asynchronous layout is active must not panic or strand a
	// result sender. No network services or visible windows are used here.
	for i := 0; i < 50; i++ {
		a := &desktopApp{}
		if err := a.createWindow(); err != nil {
			t.Fatal(err)
		}
		a.applySimpleModeSnapshot(simpleModeSnapshot{Mode: "spoke", NodeName: "layout-test"})
		a.mw.Dispose()
		runtime.Gosched()
	}
}
