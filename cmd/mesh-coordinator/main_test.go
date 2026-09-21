package main

import (
	"meshlink/internal/config"
	"meshlink/internal/onboarding"
	"os"
	"path/filepath"
	"testing"
)

func TestStartupCreatesLocalNodeAndReusesNetwork(t *testing.T) {
	dir := t.TempDir()
	first, err := prepareServer(options{data: dir, server: "127.0.0.1:43223", listen: "127.0.0.1:43223", maxUses: 100})
	if err != nil {
		t.Fatal(err)
	}
	if first.Invite.Link == "" || len(first.Invite.Code) != 6 {
		t.Fatal("startup did not generate enrollment information")
	}
	hub, err := config.Load(first.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if hub.ServerNodeConfig == "" {
		t.Fatal("Linux host is missing its own mesh node")
	}
	originalNodeID := hub.NodeID
	hub.DisplayName = "custom display name"
	if err := config.Write(first.ConfigPath, *hub); err != nil {
		t.Fatal(err)
	}
	child, err := config.Load(filepath.Join(dir, "configs", hub.ServerNodeConfig))
	if err != nil {
		t.Fatal(err)
	}
	if child.VirtualIP != "10.77.0.1" || child.Device.Type != "tun" {
		t.Fatalf("bad local node: %+v", child)
	}
	caPath := filepath.Join(dir, "certs", "ca-key.pem")
	before, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := prepareServer(options{data: dir, maxUses: 100})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(caPath)
	if first.Invite.Code != second.Invite.Code || first.Invite.Link != second.Invite.Link || string(before) != string(after) {
		t.Fatal("restart replaced credentials or invitation")
	}
	restarted, err := config.Load(second.ConfigPath)
	if err != nil || restarted.NodeID != originalNodeID || restarted.DisplayName != "custom display name" {
		t.Fatalf("restart changed node identity: %+v %v", restarted, err)
	}
	nodes, err := (onboarding.Manager{BaseDir: dir}).LoadRegisteredNodes()
	if err != nil || len(nodes) != 1 || nodes[0].VirtualIP != "10.77.0.1" {
		t.Fatalf("restart duplicated local node: %+v %v", nodes, err)
	}
	rotated, err := prepareServer(options{data: dir, maxUses: 100, newCode: true})
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Invite.Token == second.Invite.Token {
		t.Fatal("explicit code rotation retained old invitation")
	}
}

func TestCustomCertificatePathsAreRejectedWithoutRewriting(t *testing.T) {
	dir := t.TempDir()
	first, err := prepareServer(options{data: dir, server: "127.0.0.1:43223", maxUses: 100})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(first.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.CertFile = "../custom/server.pem"
	if err := config.Write(first.ConfigPath, *cfg); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(first.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepareServer(options{data: dir, maxUses: 100}); err == nil {
		t.Fatal("custom certificate identity silently replaced")
	}
	after, err := os.ReadFile(first.ConfigPath)
	if err != nil || string(before) != string(after) {
		t.Fatal("rejected startup modified existing configuration")
	}
}

func TestInvalidExplicitServerNameDoesNotCreateFiles(t *testing.T) {
	dir := t.TempDir()
	_, err := (onboarding.Manager{BaseDir: dir}).StartServerMode(onboarding.StartServerRequest{NodeName: "../escape", ServerAddress: "127.0.0.1:43223", ListenPort: 43223})
	if err == nil {
		t.Fatal("invalid server name accepted")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("invalid name changed data directory: %v %v", entries, err)
	}
}

func TestStartupRejectsInvalidSettingsWithoutCreatingIdentity(t *testing.T) {
	for _, o := range []options{{}, {server: "https://example.com"}, {server: "example.com:70000"}, {server: "127.0.0.1:3222", listen: "[::]:3222"}, {server: "127.0.0.1:3222", maxUses: -2}} {
		o.data = filepath.Join(t.TempDir(), "new")
		if _, err := prepareServer(o); err == nil {
			t.Fatalf("invalid options accepted: %+v", o)
		}
		if _, err := os.Stat(filepath.Join(o.data, "certs", "ca-key.pem")); !os.IsNotExist(err) {
			t.Fatalf("invalid startup created identity: %v", err)
		}
	}
}

func TestDataLockRejectsSecondInstanceAndReleasesOnClose(t *testing.T) {
	dir := t.TempDir()
	first, err := acquireDataLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := acquireDataLock(dir); err == nil {
		other.Close()
		t.Fatal("second process can overwrite active network")
	}
	first.Close()
	third, err := acquireDataLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	third.Close()
}
