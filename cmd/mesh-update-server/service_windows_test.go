//go:build windows

package main

import "testing"

func TestUpdateServiceBinaryPathIncludesReleaseDirAndListen(t *testing.T) {
	got := updateServiceBinaryPath(
		`C:\Program Files\Meshlink\mesh-update-server.exe`,
		"MeshlinkUpdateServer",
		"10.77.0.1:1263",
		`C:\Program Files\Meshlink\release`,
	)
	want := `"C:\Program Files\Meshlink\mesh-update-server.exe" -service run -service-name MeshlinkUpdateServer -listen 10.77.0.1:1263 -dir "C:\Program Files\Meshlink\release"`
	if got != want {
		t.Fatalf("updateServiceBinaryPath() = %q, want %q", got, want)
	}
}
