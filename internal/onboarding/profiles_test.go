package onboarding

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRoleSwitchPreservesClientIdentityAndServerTrust(t *testing.T) {
	base := t.TempDir()
	m := testManager(base)
	if _, err := m.CreateHub(CreateHubRequest{NodeName: "A", ListenPort: 8443}); err != nil {
		t.Fatal(err)
	}
	caBefore, _ := os.ReadFile(filepath.Join(base, "certs", "ca.pem"))
	spoke, err := SelectRole(base, "spoke")
	if err != nil {
		t.Fatal(err)
	}
	if spoke.BaseDir != filepath.Join(base, "profiles", "spoke") {
		t.Fatal("client would overwrite server files")
	}
	hub, err := SelectRole(base, "hub")
	if err != nil || hub.BaseDir != base {
		t.Fatalf("server role lost: %+v %v", hub, err)
	}
	caAfter, _ := os.ReadFile(filepath.Join(base, "certs", "ca.pem"))
	if !bytes.Equal(caBefore, caAfter) {
		t.Fatal("role selection overwrote CA")
	}
	selected := ManagerForConfig(base, filepath.Join(spoke.BaseDir, "configs", "active.json"))
	if selected.BaseDir != spoke.BaseDir {
		t.Fatal("profile runtime path not followed")
	}
	foreign := ManagerForConfig(base, filepath.Join(t.TempDir(), "configs", "active.json"))
	if foreign.BaseDir != base {
		t.Fatal("adopted unrelated service configuration")
	}
}
