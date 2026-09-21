package onboarding

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"meshlink/internal/config"
)

func TestJoinedNetworkRestoresEnrollmentFieldsAfterReopen(t *testing.T) {
	hub := testManager(t.TempDir())
	if _, err := hub.CreateHub(CreateHubRequest{NodeName: "A", ListenPort: 8443}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(hub.EnrollHTTPHandler())
	defer server.Close()
	invite, err := hub.CreateInvite(CreateInviteRequest{Server: server.URL, LongLived: true, MaxUses: 3})
	if err != nil {
		t.Fatal(err)
	}
	client := testManager(t.TempDir())
	client.HTTPClient = server.Client()
	req := JoinSpokeRequest{InviteLink: invite.Link, Code: invite.Code, NodeName: "office-pc"}
	joined, err := client.JoinSpoke(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.JoinSpoke(JoinSpokeRequest{}); err != nil {
		t.Fatal(err)
	}
	// A newly opened window must recover the original fields, even after an
	// empty reconnect request, without using another invitation or identity.
	reopened := Manager{BaseDir: client.BaseDir}
	info, err := reopened.JoinedNetwork()
	if err != nil {
		t.Fatal(err)
	}
	if info.InviteLink != req.InviteLink || info.Code != req.Code || info.NodeName != req.NodeName || info.VirtualIP != joined.VirtualIP {
		t.Fatalf("restored enrollment differs: %+v", info)
	}
	if err := reopened.LeaveNetwork("test-service"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(client.BaseDir, "configs", "join-state.json")); !os.IsNotExist(err) {
		t.Fatalf("leave retained enrollment credentials: %v", err)
	}
}

func TestJoinedNetworkDoesNotShowAnotherIdentitysInvitation(t *testing.T) {
	hub := testManager(t.TempDir())
	if _, err := hub.CreateHub(CreateHubRequest{NodeName: "A", ListenPort: 8443}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(hub.EnrollHTTPHandler())
	defer server.Close()
	invite, err := hub.CreateInvite(CreateInviteRequest{Server: server.URL, LongLived: true, MaxUses: 3})
	if err != nil {
		t.Fatal(err)
	}
	client := testManager(t.TempDir())
	client.HTTPClient = server.Client()
	joined, err := client.JoinSpoke(JoinSpokeRequest{InviteLink: invite.Link, Code: invite.Code, NodeName: "office-pc"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(joined.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Connect = "other.example:8443"
	cfg.Transport.Connect = cfg.Connect
	if err := config.Write(joined.ConfigPath, *cfg); err != nil {
		t.Fatal(err)
	}
	info, err := client.JoinedNetwork()
	if err != nil {
		t.Fatal(err)
	}
	if info.InviteLink != "" || info.Code != "" || info.Server != cfg.Connect {
		t.Fatalf("stale invitation leaked across networks: %+v", info)
	}
}
