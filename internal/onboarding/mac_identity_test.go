package onboarding

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"meshlink/internal/certutil"
	"meshlink/internal/config"
)

func TestMACEnrollmentKeepsIdentityAndAddressAfterRename(t *testing.T) {
	hub := testManager(t.TempDir())
	if _, err := hub.CreateHub(CreateHubRequest{NodeName: "A", ListenPort: 8443}); err != nil {
		t.Fatal(err)
	}
	invite, err := hub.CreateInvite(CreateInviteRequest{Server: "127.0.0.1:8443", LongLived: true, MaxUses: 10})
	if err != nil {
		t.Fatal(err)
	}
	enroll := func(name, mac, code string) (EnrollResponse, error) {
		csr, err := certutil.CreateCSR(certutil.CSROptions{OutDir: t.TempDir(), Name: name})
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(map[string]any{"token": invite.Token, "code": code, "node_name": name, "display_name": name, "mac_address": mac, "csr_pem": csr.CSRPEM})
		var req EnrollRequest
		if err := json.Unmarshal(data, &req); err != nil {
			t.Fatal(err)
		}
		return hub.HandleEnroll(req)
	}
	first, err := enroll("old-name", "00:11:22:33:44:55", invite.Code)
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := enroll("new-name", "00-11-22-33-44-55", invite.Code)
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Config.NodeID != first.Config.NodeID || renamed.Config.VirtualIP != first.Config.VirtualIP {
		t.Fatalf("same MAC allocated new identity/address: %+v -> %+v", first.Config, renamed.Config)
	}
	nodes, err := hub.LoadRegisteredNodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].DisplayName != "new-name" {
		t.Fatalf("rename duplicated or failed to update device: %+v", nodes)
	}
	if _, err := enroll("bad-code", "00:11:22:33:44:55", "invalid"); err == nil {
		t.Fatal("MAC bypassed invitation authorization")
	}
	other, err := enroll("other", "00:11:22:33:44:66", invite.Code)
	if err != nil {
		t.Fatal(err)
	}
	if other.Config.VirtualIP == first.Config.VirtualIP {
		t.Fatal("different MAC shared address")
	}
	if _, err := hub.DisableDevice(first.Config.NodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := enroll("disabled", "00:11:22:33:44:55", invite.Code); err == nil {
		t.Fatal("MAC resurrected disabled device")
	}
}

func TestRenameJoinedDevicePreservesCertificateAndIP(t *testing.T) {
	hub := testManager(t.TempDir())
	if _, err := hub.CreateHub(CreateHubRequest{NodeName: "A", ListenPort: 8443}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(hub.EnrollHTTPHandler())
	defer server.Close()
	invite, err := hub.CreateInvite(CreateInviteRequest{Server: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	client := testManager(t.TempDir())
	client.HTTPClient = server.Client()
	first, err := client.JoinSpoke(JoinSpokeRequest{InviteLink: invite.Link, Code: invite.Code, NodeName: "old"})
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(client.certsDir(), "old-key.pem")
	key, _ := os.ReadFile(keyPath)
	renamed, err := client.JoinSpoke(JoinSpokeRequest{NodeName: "new"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(renamed.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(keyPath)
	if renamed.VirtualIP != first.VirtualIP || cfg.NodeID != "old" || cfg.DisplayName != "new" || string(key) != string(after) {
		t.Fatal("rename replaced identity, key or address")
	}
}

func TestFreshDirectoryWithSameMACReceivesOriginalCertificateIdentity(t *testing.T) {
	hub := testManager(t.TempDir())
	if _, err := hub.CreateHub(CreateHubRequest{NodeName: "A", ListenPort: 8443}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(hub.EnrollHTTPHandler())
	defer server.Close()
	invite, err := hub.CreateInvite(CreateInviteRequest{Server: server.URL, LongLived: true, MaxUses: 5})
	if err != nil {
		t.Fatal(err)
	}
	var previous *config.Config
	for _, name := range []string{"before", "after"} {
		client := testManager(t.TempDir())
		client.HTTPClient = server.Client()
		client.LocalMAC = func() string { return "00:11:22:33:44:55" }
		joined, err := client.JoinSpoke(JoinSpokeRequest{InviteLink: invite.Link, Code: invite.Code, NodeName: name})
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := config.Load(joined.ConfigPath)
		if err != nil {
			t.Fatal(err)
		}
		if previous != nil && (cfg.NodeID != previous.NodeID || cfg.VirtualIP != previous.VirtualIP) {
			t.Fatal("fresh directory changed same-MAC identity")
		}
		if _, err := client.JoinSpoke(JoinSpokeRequest{}); err != nil {
			t.Fatalf("returned key and cert cannot resume: %v", err)
		}
		previous = cfg
	}
	nodes, err := hub.LoadRegisteredNodes()
	if err != nil || len(nodes) != 1 || nodes[0].DisplayName != "after" {
		t.Fatalf("bad registry: %+v %v", nodes, err)
	}
}

func TestAuthenticatedMetadataBindingPreservesIdentity(t *testing.T) {
	m := testManager(t.TempDir())
	if err := os.MkdirAll(m.configsDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := m.saveDeviceRegistry(DeviceRegistry{Nodes: []RegisteredNode{
		{NodeID: "legacy", DisplayName: "legacy", VirtualIP: "10.77.0.2", CertFingerprint: "trusted"},
		{NodeID: "other", VirtualIP: "10.77.0.3", CertFingerprint: "other-trusted"},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SyncDeviceMetadata("legacy", "wrong", "attack", "00:11:22:33:44:55"); err == nil {
		t.Fatal("unauthenticated binding accepted")
	}
	node, err := m.SyncDeviceMetadata("legacy", "trusted", "after", "00-11-22-33-44-55")
	if err != nil {
		t.Fatal(err)
	}
	if node.NodeID != "legacy" || node.VirtualIP != "10.77.0.2" || node.DisplayName != "after" || node.MACAddress != "00:11:22:33:44:55" {
		t.Fatalf("bad metadata: %+v", node)
	}
	if _, err := m.SyncDeviceMetadata("other", "other-trusted", "other", "00:11:22:33:44:55"); err == nil {
		t.Fatal("ambiguous MAC was bound twice")
	}
	if _, err := m.RenameDevice("legacy", "admin-name"); err != nil {
		t.Fatal(err)
	}
	unchanged, err := m.SyncDeviceMetadata("legacy", "trusted", "after", "00:11:22:33:44:55")
	if err != nil || unchanged.DisplayName != "admin-name" {
		t.Fatalf("reconnect reverted administrator name: %+v %v", unchanged, err)
	}
	renamed, err := m.SyncDeviceMetadata("legacy", "trusted", "client-new-name", "00:11:22:33:44:55")
	if err != nil || renamed.DisplayName != "client-new-name" {
		t.Fatalf("explicit client rename not synchronized: %+v %v", renamed, err)
	}
	if _, err := m.DisableDevice("legacy"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SyncDeviceMetadata("legacy", "trusted", "revive", "00:11:22:33:44:55"); err == nil {
		t.Fatal("metadata revived disabled identity")
	}
}

func TestNewClientCanEnrollWithLegacyStrictServer(t *testing.T) {
	hub := testManager(t.TempDir())
	if _, err := hub.CreateHub(CreateHubRequest{NodeName: "A", ListenPort: 8443}); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var legacy struct {
			Token    string `json:"token"`
			Code     string `json:"code"`
			NodeName string `json:"node_name"`
			CSRPEM   string `json:"csr_pem"`
		}
		if err := decodeEnrollJSON(r, &legacy); err != nil {
			writeEnrollError(w, err)
			return
		}
		result, err := hub.HandleEnroll(EnrollRequest{Token: legacy.Token, Code: legacy.Code, NodeName: legacy.NodeName, CSRPEM: []byte(legacy.CSRPEM)})
		if err != nil {
			writeEnrollError(w, err)
			return
		}
		writeEnrollJSON(w, http.StatusOK, EnrollHTTPResponse{OK: true, CAPEM: string(result.CAPEM), CertPEM: string(result.CertPEM), Config: result.Config})
	}))
	defer server.Close()
	invite, err := hub.CreateInvite(CreateInviteRequest{Server: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	client := testManager(t.TempDir())
	client.HTTPClient = server.Client()
	client.LocalMAC = func() string { return "00:11:22:33:44:55" }
	if _, err := client.JoinSpoke(JoinSpokeRequest{InviteLink: invite.Link, Code: invite.Code, NodeName: "B"}); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("expected one strictly rejected attempt and one legacy enrollment; got %d", requests)
	}
	nodes, err := hub.LoadRegisteredNodes()
	if err != nil || len(nodes) != 1 {
		t.Fatalf("legacy retry duplicated enrollment: %+v %v", nodes, err)
	}
}
