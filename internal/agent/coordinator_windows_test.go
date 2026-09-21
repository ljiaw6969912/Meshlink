//go:build windows

package agent

import (
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"meshlink/internal/onboarding"
)

func TestRegistryWatcherSharingOutageHasOneRetryBudget(t *testing.T) {
	f := newCoordinatorFixture(t, true)
	nodes, err := (onboarding.Manager{BaseDir: f.dir}).LoadRegisteredNodes()
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		f.coordinator.known[node.NodeID] = node
	}
	name, err := windows.UTF16PtrFromString(filepath.Join(f.dir, "configs", "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	defer func() { windows.CloseHandle(handle); <-done }()
	go func() { f.coordinator.reconcileRegistry(); close(done) }()
	select {
	case <-done:
		if len(f.coordinator.known) != len(nodes) {
			t.Fatal("registry outage removed known members")
		}
	case <-time.After(2500 * time.Millisecond):
		t.Fatal("registry watcher repeated the retry budget per node while holding the coordinator lock")
	}
}
