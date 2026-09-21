//go:build windows

package onboarding

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestAdmissionRegistryReadSurvivesBriefWindowsSharingLock(t *testing.T) {
	m := Manager{BaseDir: t.TempDir()}
	path := m.deviceRegistryPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"nodes":[{"node_id":"B"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(50 * time.Millisecond)
		windows.CloseHandle(handle)
		close(released)
	}()
	defer func() { <-released }()
	nodes, err := m.LoadRegisteredNodes()
	if err != nil || len(nodes) != 1 || nodes[0].NodeID != "B" {
		t.Fatalf("temporary sharing lock denied valid admission registry: %+v, %v", nodes, err)
	}
}
