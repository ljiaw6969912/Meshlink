package onboarding

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"meshlink/internal/certutil"
	"meshlink/internal/config"
)

func TestFreshClientsWithSameNameCanConnectWithDistinctIdentities(t *testing.T) {
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
	req := JoinSpokeRequest{InviteLink: invite.Link, Code: invite.Code, NodeName: "same-computer-name"}
	var identities []*config.Config
	for i := 0; i < 2; i++ {
		client := testManager(t.TempDir())
		client.HTTPClient = server.Client()
		joined, err := client.JoinSpoke(req)
		if err != nil {
			t.Fatalf("fresh client %d could not enroll: %v", i+1, err)
		}
		cfg, err := config.Load(joined.ConfigPath)
		if err != nil {
			t.Fatal(err)
		}
		identities = append(identities, cfg)
		before, err := os.ReadFile(joined.ConfigPath)
		if err != nil {
			t.Fatal(err)
		}
		resumed, err := client.JoinSpoke(JoinSpokeRequest{NodeName: req.NodeName})
		if err != nil || resumed.VirtualIP != joined.VirtualIP {
			t.Fatalf("client %d lost its identity on reconnect: %+v %v", i+1, resumed, err)
		}
		after, _ := os.ReadFile(joined.ConfigPath)
		if !bytes.Equal(before, after) {
			t.Fatal("reconnect replaced the client configuration")
		}
		var saved map[string]any
		if err := json.Unmarshal(after, &saved); err != nil {
			t.Fatal(err)
		}
		if saved["display_name"] != req.NodeName {
			t.Fatalf("local device name lost: %+v", saved)
		}
	}
	if identities[0].NodeID == identities[1].NodeID || identities[0].VirtualIP == identities[1].VirtualIP {
		t.Fatal("different fresh clients share an identity or IP")
	}
	registry, err := hub.loadDeviceRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.Nodes) != 2 {
		t.Fatalf("reconnect changed device count: %d", len(registry.Nodes))
	}
}

func TestJoinSameNetworkReusesIdentityWithoutAnotherEnrollment(t *testing.T) {
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
	spoke := testManager(t.TempDir())
	spoke.HTTPClient = server.Client()
	req := JoinSpokeRequest{InviteLink: invite.Link, Code: invite.Code, NodeName: "B"}
	first, err := spoke.JoinSpoke(req)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{spoke.activeConfigPath(), filepath.Join(spoke.certsDir(), "B-key.pem"), filepath.Join(spoke.certsDir(), "B.pem"), filepath.Join(spoke.certsDir(), "ca.pem"), hub.deviceRegistryPath(), hub.inviteStorePath()}
	before := make(map[string][]byte)
	for _, path := range paths {
		before[path], err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	// The first, one-use invitation is consumed. Reconnecting must work even
	// when the enrollment endpoint is unavailable and must preserve key/IP.
	server.Close()
	second, err := spoke.JoinSpoke(req)
	if err != nil {
		t.Fatalf("joining the same network should reuse its identity: %v", err)
	}
	if second.ConfigPath != first.ConfigPath || second.VirtualIP != "10.77.0.2" {
		t.Fatalf("identity changed: first=%+v second=%+v", first, second)
	}
	// After reopening the UI, invitation fields may be empty. The installed
	// identity is sufficient to resume, just as with the reconnect button.
	resumed, err := spoke.JoinSpoke(JoinSpokeRequest{})
	if err != nil || resumed.ConfigPath != first.ConfigPath || resumed.VirtualIP != "10.77.0.2" {
		t.Fatalf("resume without another invitation: result=%+v err=%v", resumed, err)
	}
	for _, path := range paths {
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(after, before[path]) {
			t.Fatalf("reconnect changed %s", path)
		}
	}
}

func TestEnrollmentCannotCreateDuplicateNodeIdentity(t *testing.T) {
	hub := testManager(t.TempDir())
	if _, err := hub.CreateHub(CreateHubRequest{NodeName: "A", ListenPort: 8443}); err != nil {
		t.Fatal(err)
	}
	invite, err := hub.CreateInvite(CreateInviteRequest{Server: "127.0.0.1:8443", LongLived: true, MaxUses: 3})
	if err != nil {
		t.Fatal(err)
	}
	csr, err := certutil.CreateCSR(certutil.CSROptions{OutDir: t.TempDir(), Name: "B"})
	if err != nil {
		t.Fatal(err)
	}
	req := EnrollRequest{Token: invite.Token, Code: invite.Code, NodeName: "B", CSRPEM: csr.CSRPEM}
	if _, err := hub.HandleEnroll(req); err != nil {
		t.Fatal(err)
	}
	registryBefore, err := os.ReadFile(hub.deviceRegistryPath())
	if err != nil {
		t.Fatal(err)
	}
	inviteBefore, err := os.ReadFile(hub.inviteStorePath())
	if err != nil {
		t.Fatal(err)
	}
	otherCSR, err := certutil.CreateCSR(certutil.CSROptions{OutDir: t.TempDir(), Name: "B"})
	if err != nil {
		t.Fatal(err)
	}
	req.CSRPEM = otherCSR.CSRPEM
	for _, name := range []string{"B", "b", " B "} {
		req.NodeName = name
		if _, err := hub.HandleEnroll(req); err == nil {
			t.Fatalf("duplicate node name %q was enrolled, making identities ambiguous", name)
		}
	}
	registryAfter, _ := os.ReadFile(hub.deviceRegistryPath())
	inviteAfter, _ := os.ReadFile(hub.inviteStorePath())
	if !bytes.Equal(registryBefore, registryAfter) || !bytes.Equal(inviteBefore, inviteAfter) {
		t.Fatal("rejected duplicate changed existing identity or consumed invitation")
	}
}

func TestRejectedEnrollmentPreservesExistingPrivateKey(t *testing.T) {
	for _, name := range []string{"B", "../B", `..\B`} {
		t.Run(name, func(t *testing.T) {
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
			spoke := testManager(t.TempDir())
			spoke.HTTPClient = server.Client()
			csr, err := certutil.CreateCSR(certutil.CSROptions{OutDir: spoke.certsDir(), Name: "B"})
			if err != nil {
				t.Fatal(err)
			}
			keyBefore, err := os.ReadFile(csr.KeyPath)
			if err != nil {
				t.Fatal(err)
			}
			_, err = spoke.JoinSpoke(JoinSpokeRequest{InviteLink: invite.Link, Code: "incorrect", NodeName: name})
			if err == nil {
				t.Fatal("invalid invitation code was accepted")
			}
			keyAfter, err := os.ReadFile(csr.KeyPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(keyBefore, keyAfter) {
				t.Fatal("failed join destroyed the existing private key")
			}
			if _, err := os.Stat(spoke.activeConfigPath()); !os.IsNotExist(err) {
				t.Fatalf("failed join installed a configuration: %v", err)
			}
		})
	}
}
