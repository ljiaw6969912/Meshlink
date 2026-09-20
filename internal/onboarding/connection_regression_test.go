package onboarding

import (
	"bytes"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"

	"meshlink/internal/networkstate"
)

func TestCoordinatorPresenceDoesNotDependOnUpstreamConnection(t *testing.T) {
	for _, mode := range []string{"hub", "spoke"} {
		t.Run(mode, func(t *testing.T) {
			mgr := testManager(t.TempDir())
			got, err := mgr.deviceListFromRuntime(runtimeStatus{
				State: "running", NetworkState: networkstate.Connected,
				CoordinatorState: "disconnected", Self: nodeStatus{NodeID: "A", Mode: mode},
				Peers: []peerStatus{{NodeID: "B", Mode: "spoke", Status: "online", VirtualIP: "10.77.0.5"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			peer := findDeviceSummary(got, "B")
			want := "offline"
			if mode == "hub" {
				want = "online"
				if got.CoordinatorState != "serving" {
					t.Fatalf("coordinator state = %q, want serving", got.CoordinatorState)
				}
			}
			if peer == nil || peer.Status != want {
				t.Fatalf("peer = %+v, want %s", peer, want)
			}
			if peer.PathType != "" {
				t.Fatalf("control presence invented a direct data path: %+v", peer)
			}
		})
	}
}

func TestStartingServerAgainPreservesClientTrust(t *testing.T) {
	dir := t.TempDir()
	mgr := testManager(dir)
	req := StartServerRequest{ServerAddress: "desk.example.com", ListenPort: 9443}
	if _, err := mgr.StartServerMode(req); err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(dir, "certs", "ca.pem")
	keyPath := filepath.Join(dir, "certs", "ca-key.pem")
	ca, _ := os.ReadFile(caPath)
	key, _ := os.ReadFile(keyPath)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		t.Fatal("invalid original CA")
	}
	if _, err := mgr.StartServerMode(req); err != nil {
		t.Fatal(err)
	}
	newCA, _ := os.ReadFile(caPath)
	newKey, _ := os.ReadFile(keyPath)
	if !bytes.Equal(ca, newCA) || !bytes.Equal(key, newKey) {
		t.Fatal("starting server replaced the network CA and invalidated enrolled clients")
	}
	var cfg struct {
		NodeID string `json:"node_id"`
	}
	readJSON(t, filepath.Join(dir, "configs", "active.json"), &cfg)
	server := readCertificate(t, filepath.Join(dir, "certs", cfg.NodeID+".pem"))
	if _, err := server.Verify(x509.VerifyOptions{Roots: roots, DNSName: "desk.example.com"}); err != nil {
		t.Fatalf("existing client no longer trusts restarted server: %v", err)
	}
}

func TestStartingServerWithMissingCAKeyDoesNotReplaceTrust(t *testing.T) {
	dir := t.TempDir()
	mgr := testManager(dir)
	req := StartServerRequest{ServerAddress: "desk.example.com", ListenPort: 9443}
	if _, err := mgr.StartServerMode(req); err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(dir, "certs", "ca.pem")
	ca, _ := os.ReadFile(caPath)
	if err := os.Remove(filepath.Join(dir, "certs", "ca-key.pem")); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.StartServerMode(req); err == nil {
		t.Fatal("silently replaced a partially missing CA")
	}
	current, _ := os.ReadFile(caPath)
	if !bytes.Equal(ca, current) {
		t.Fatal("modified existing CA on failure")
	}
}

func TestStartingExistingServerWithBothCAFilesMissingFailsClosed(t *testing.T) {
	dir := t.TempDir()
	mgr := testManager(dir)
	req := StartServerRequest{ServerAddress: "desk.example.com", ListenPort: 9443}
	if _, err := mgr.StartServerMode(req); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "configs", "active.json")
	original, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ca.pem", "ca-key.pem"} {
		if err := os.Remove(filepath.Join(dir, "certs", name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := mgr.StartServerMode(req); err == nil {
		t.Fatal("replaced the trust root of an existing network with missing CA files")
	}
	current, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(original, current) {
		t.Fatal("modified active configuration on failure")
	}
	for _, name := range []string{"ca.pem", "ca-key.pem"} {
		if _, err := os.Stat(filepath.Join(dir, "certs", name)); !os.IsNotExist(err) {
			t.Fatalf("created %s instead of preserving damaged network state", name)
		}
	}
}
