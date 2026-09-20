package onboarding

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"meshlink/internal/config"
	"meshlink/internal/networkstate"
)

func TestLeaveNetworkClearsManagedSpokeIdentity(t *testing.T) {
	dir := t.TempDir()
	mgr := Manager{BaseDir: dir}
	writeManagedSpokeFixture(t, dir, "desk")

	if err := mgr.LeaveNetwork("MeshlinkAgent"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(dir, "configs", "active.json"),
		filepath.Join(dir, "configs", "logs", "MeshlinkAgent.status.json"),
		filepath.Join(dir, "certs", "ca.pem"),
		filepath.Join(dir, "certs", "desk.pem"),
		filepath.Join(dir, "certs", "desk-key.pem"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("managed file still exists: %s", path)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "logs", "audit.jsonl")); err != nil {
		t.Fatalf("unrelated audit log must remain: %v", err)
	}
	devices, err := mgr.Devices("MeshlinkAgent")
	if err != nil {
		t.Fatal(err)
	}
	if devices.NetworkState != networkstate.NotJoined || len(devices.Nodes) != 0 {
		t.Fatalf("devices = %+v, want empty not-joined list", devices)
	}
	if err := mgr.LeaveNetwork("MeshlinkAgent"); err != nil {
		t.Fatalf("second leave must be idempotent: %v", err)
	}
}

func TestLeaveNetworkRejectsHubWithoutDeletingFiles(t *testing.T) {
	dir := t.TempDir()
	writeManagedSpokeFixture(t, dir, "hub")
	path := filepath.Join(dir, "configs", "active.json")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Mode = "hub"
	if err := writePrettyJSON(path, cfg); err != nil {
		t.Fatal(err)
	}

	if err := (Manager{BaseDir: dir}).LeaveNetwork("MeshlinkAgent"); err == nil {
		t.Fatal("hub leave must be rejected")
	}
	for _, path := range []string{
		filepath.Join(dir, "configs", "active.json"),
		filepath.Join(dir, "configs", "logs", "MeshlinkAgent.status.json"),
		filepath.Join(dir, "certs", "ca.pem"),
		filepath.Join(dir, "certs", "hub.pem"),
		filepath.Join(dir, "certs", "hub-key.pem"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("hub file was changed: %s: %v", path, err)
		}
	}
}

func TestLeaveNetworkRejectsIdentityPathOutsideBaseDir(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "managed")
	outside := filepath.Join(parent, "outside-key.pem")
	writeManagedSpokeFixture(t, dir, "desk")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "configs", "active.json")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.KeyFile = filepath.Join("..", "..", "outside-key.pem")
	if err := writePrettyJSON(path, cfg); err != nil {
		t.Fatal(err)
	}

	if err := (Manager{BaseDir: dir}).LeaveNetwork("MeshlinkAgent"); err == nil {
		t.Fatal("identity path outside the manager base must be rejected")
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "outside" {
		t.Fatalf("outside identity changed: contents=%q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "configs", "active.json")); err != nil {
		t.Fatalf("active config must remain after boundary rejection: %v", err)
	}
}

func TestLeaveNetworkRollsBackEveryStagingFailure(t *testing.T) {
	originalRename := leaveRename
	t.Cleanup(func() { leaveRename = originalRename })

	for failAt := 1; failAt <= 5; failAt++ {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			dir := t.TempDir()
			writeManagedSpokeFixture(t, dir, "desk")
			calls := 0
			leaveRename = func(oldPath, newPath string) error {
				calls++
				if calls == failAt {
					return errors.New("injected staging failure")
				}
				return os.Rename(oldPath, newPath)
			}

			if err := (Manager{BaseDir: dir}).LeaveNetwork("MeshlinkAgent"); err == nil {
				t.Fatal("LeaveNetwork must report the staging failure")
			}
			for _, path := range managedSpokePaths(dir, "desk") {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("managed identity was not rolled back: %s: %v", path, err)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "configs", ".leave-network")); !os.IsNotExist(err) {
				t.Fatalf("rollback transaction still exists: %v", err)
			}
		})
	}
}

func TestLeaveNetworkRecoversEveryCommittedRemovalFailure(t *testing.T) {
	originalRemove := leaveRemove
	t.Cleanup(func() { leaveRemove = originalRemove })

	for failAt := 1; failAt <= 5; failAt++ {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			dir := t.TempDir()
			writeManagedSpokeFixture(t, dir, "desk")
			calls := 0
			leaveRemove = func(path string) error {
				calls++
				if calls == failAt {
					return errors.New("injected removal failure")
				}
				return os.Remove(path)
			}

			if err := (Manager{BaseDir: dir}).LeaveNetwork("MeshlinkAgent"); err == nil {
				t.Fatal("LeaveNetwork must report the committed cleanup failure")
			}
			if _, err := os.Stat(filepath.Join(dir, "configs", "active.json")); !os.IsNotExist(err) {
				t.Fatalf("active identity must stay deactivated after commit: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "configs", ".leave-network")); err != nil {
				t.Fatalf("recoverable transaction marker missing: %v", err)
			}

			leaveRemove = os.Remove
			if err := (Manager{BaseDir: dir}).LeaveNetwork("MeshlinkAgent"); err != nil {
				t.Fatalf("retry must finish committed cleanup: %v", err)
			}
			for _, path := range managedSpokePaths(dir, "desk") {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("managed identity still exists after recovery: %s: %v", path, err)
				}
			}
		})
	}
}

func TestLeaveNetworkRecoversPreCommitTransactionThenCompletesLeave(t *testing.T) {
	for stagedCount := 0; stagedCount <= 4; stagedCount++ {
		t.Run(string(rune('0'+stagedCount)), func(t *testing.T) {
			dir := t.TempDir()
			writeManagedSpokeFixture(t, dir, "desk")
			stageInterruptedLeaveTransaction(t, dir, "desk", stagedCount)

			if err := (Manager{BaseDir: dir}).LeaveNetwork("MeshlinkAgent"); err != nil {
				t.Fatalf("LeaveNetwork must recover and complete the new leave: %v", err)
			}
			for _, path := range managedSpokePaths(dir, "desk") {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("managed identity still exists after recovery: %s: %v", path, err)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "configs", ".leave-network")); !os.IsNotExist(err) {
				t.Fatalf("completed leave transaction still exists: %v", err)
			}
		})
	}
}

func TestLeaveNetworkReturnsPreCommitRecoveryFailure(t *testing.T) {
	originalRename := leaveRename
	t.Cleanup(func() { leaveRename = originalRename })

	dir := t.TempDir()
	writeManagedSpokeFixture(t, dir, "desk")
	stageInterruptedLeaveTransaction(t, dir, "desk", 1)
	leaveRename = func(oldPath, newPath string) error {
		if filepath.Base(oldPath) == "00" {
			return errors.New("injected recovery failure")
		}
		return os.Rename(oldPath, newPath)
	}

	err := (Manager{BaseDir: dir}).LeaveNetwork("MeshlinkAgent")
	if err == nil || !strings.Contains(err.Error(), "injected recovery failure") {
		t.Fatalf("LeaveNetwork error = %v, want injected recovery failure", err)
	}
}

func managedSpokePaths(dir, nodeID string) []string {
	return []string{
		filepath.Join(dir, "configs", "active.json"),
		filepath.Join(dir, "configs", "logs", "MeshlinkAgent.status.json"),
		filepath.Join(dir, "certs", "ca.pem"),
		filepath.Join(dir, "certs", nodeID+".pem"),
		filepath.Join(dir, "certs", nodeID+"-key.pem"),
	}
}

func stageInterruptedLeaveTransaction(t *testing.T, dir, nodeID string, stagedCount int) {
	t.Helper()
	transactionDir := filepath.Join(dir, "configs", ".leave-network")
	if err := os.Mkdir(transactionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	paths := managedSpokePaths(dir, nodeID)
	ordered := []string{paths[1], paths[4], paths[3], paths[2], paths[0]}
	for i := 0; i < stagedCount; i++ {
		if err := os.Rename(ordered[i], filepath.Join(transactionDir, fmt.Sprintf("%02d", i))); err != nil {
			t.Fatal(err)
		}
	}
}

func writeManagedSpokeFixture(t *testing.T, dir, nodeID string) {
	t.Helper()
	writeSpokeConfigForLeaveTest(t, dir, nodeID)
	for path, body := range map[string]string{
		filepath.Join(dir, "certs", "ca.pem"):                              "ca",
		filepath.Join(dir, "certs", nodeID+".pem"):                         "cert",
		filepath.Join(dir, "certs", nodeID+"-key.pem"):                     "key",
		filepath.Join(dir, "configs", "logs", "MeshlinkAgent.status.json"): `{"state":"running"}`,
		filepath.Join(dir, "logs", "audit.jsonl"):                          "keep\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func writeSpokeConfigForLeaveTest(t *testing.T, dir, nodeID string) {
	t.Helper()
	cfg := config.Config{
		NodeID: nodeID, Mode: "spoke", Connect: "example.com:8443",
		CAFile: "../certs/ca.pem", CertFile: "../certs/" + nodeID + ".pem",
		KeyFile: "../certs/" + nodeID + "-key.pem", VirtualIP: "10.77.0.2",
		Device: config.DeviceConfig{Type: "null"},
	}
	if err := writePrettyJSON(filepath.Join(dir, "configs", "active.json"), cfg); err != nil {
		t.Fatal(err)
	}
}
