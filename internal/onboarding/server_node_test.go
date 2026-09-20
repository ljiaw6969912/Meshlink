package onboarding

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"meshlink/internal/config"
	"meshlink/internal/networkstate"
)

func TestServerModePersistsHostIdentityWithoutConsumingInvite(t *testing.T) {
	m := testManager(t.TempDir())
	request := StartServerRequest{ServerAddress: "desk.example.com", ListenPort: 9443, LongLived: true, MaxUses: 5}
	first, err := m.StartServerMode(request)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(first.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerNodeConfig == "" || cfg.ServerPublicEndpoint != "desk.example.com:9443" {
		t.Fatalf("missing server host config: %+v", cfg)
	}
	host, err := config.Load(filepath.Join(m.configsDir(), cfg.ServerNodeConfig))
	if err != nil {
		t.Fatal(err)
	}
	if host.Mode != "spoke" || host.VirtualIP != "10.77.0.1" || host.Device.Type != "tun" || !host.Setup.Enabled {
		t.Fatalf("host is not a mesh node: %+v", host)
	}
	before, err := os.ReadFile(resolveConfigPath(m.configsDir(), host.CertFile))
	if err != nil {
		t.Fatal(err)
	}
	hubBefore, err := os.ReadFile(resolveConfigPath(m.configsDir(), cfg.CertFile))
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.StartServerMode(request)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(resolveConfigPath(m.configsDir(), host.CertFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("server host identity changed on restart")
	}
	hubAfter, err := os.ReadFile(resolveConfigPath(m.configsDir(), cfg.CertFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(hubBefore) != string(hubAfter) {
		t.Fatal("unchanged server startup replaced coordinator identity")
	}
	if first.Invite.Link != second.Invite.Link || first.Invite.Code != second.Invite.Code {
		t.Fatal("restart rotated the invite")
	}
	registry, err := m.loadDeviceRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.Nodes) != 1 || registry.Nodes[0].NodeID != host.NodeID || registry.Nodes[0].VirtualIP != host.VirtualIP || registry.Nodes[0].CertFingerprint != certificateFingerprint(before) {
		t.Fatalf("invalid host registration: %+v", registry)
	}
	store, err := m.loadInviteStore()
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Invites) != 1 || store.Invites[0].Uses != 0 {
		t.Fatalf("host consumed invite quota: %+v", store)
	}
	devices, err := m.Devices("")
	if err != nil {
		t.Fatal(err)
	}
	if len(devices.Nodes) != 1 || devices.Nodes[0].Kind != "self" || devices.Nodes[0].NodeID != host.NodeID || devices.Nodes[0].VirtualIP != "10.77.0.1" {
		t.Fatalf("server host was hidden or duplicated: %+v", devices)
	}
}

func TestServerModeUpgradesLimitedInviteOnceThenReusesUnlimitedInvite(t *testing.T) {
	for _, oldLongLived := range []bool{false, true} {
		name := "one-time"
		if oldLongLived {
			name = "three-devices"
		}
		t.Run(name, func(t *testing.T) {
			m := testManager(t.TempDir())
			request := StartServerRequest{ServerAddress: "desk.example.com", ListenPort: 9443, LongLived: oldLongLived}
			old, err := m.StartServerMode(request)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m.validateInvite(EnrollRequest{Token: old.Invite.Token, Code: old.Invite.Code}); err != nil {
				t.Fatalf("old invite must still be usable before upgrade: %v", err)
			}
			request.LongLived, request.MaxUses = true, -1
			upgraded, err := m.StartServerMode(request)
			if err != nil {
				t.Fatal(err)
			}
			if upgraded.Invite.Token == old.Invite.Token || !upgraded.Invite.LongLived || upgraded.Invite.MaxUses != -1 {
				t.Fatalf("unlimited request retained the old limited invite: %+v", upgraded.Invite)
			}
			restarted, err := m.StartServerMode(request)
			if err != nil {
				t.Fatal(err)
			}
			if restarted.Invite.Token != upgraded.Invite.Token || restarted.Invite.Code != upgraded.Invite.Code {
				t.Fatal("identical unlimited settings rotated the invite again")
			}
		})
	}
}

func TestServerModeReusesInviteForEquivalentDefaultLimits(t *testing.T) {
	for _, longLived := range []bool{false, true} {
		m := testManager(t.TempDir())
		request := StartServerRequest{ServerAddress: "desk.example.com", ListenPort: 9443, LongLived: longLived}
		first, err := m.StartServerMode(request)
		if err != nil {
			t.Fatal(err)
		}
		request.MaxUses = defaultInviteMaxUses
		if longLived {
			request.MaxUses = defaultLongLivedMaxUses
		}
		explicit, err := m.StartServerMode(request)
		if err != nil {
			t.Fatal(err)
		}
		if explicit.Invite.Token != first.Invite.Token {
			t.Fatalf("equivalent default max uses rotated invite (long_lived=%t)", longLived)
		}
	}
}

func TestDefaultServerListenSupportsPublicOnlyAndMultihomedHosts(t *testing.T) {
	m := Manager{BaseDir: t.TempDir()}
	hub, err := m.CreateHub(CreateHubRequest{NodeName: "server", ListenPort: 9443})
	if err != nil {
		t.Fatal(err)
	}
	if hub.Listen != "0.0.0.0:9443" {
		t.Fatalf("listen = %q", hub.Listen)
	}
}

func TestServerModePreservesExistingCustomNetwork(t *testing.T) {
	m := testManager(t.TempDir())
	hub, err := m.CreateHub(CreateHubRequest{VirtualCIDR: "10.88.0.0/24", VirtualIP: "10.88.0.1", ListenPort: 9443})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(hub.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartServerMode(StartServerRequest{ServerAddress: "desk.example.com", ListenPort: 9443}); err == nil {
		t.Fatal("silently changed the existing network and its allocated addresses")
	}
	after, err := os.ReadFile(hub.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("rejected start changed the original config")
	}
}

func TestServerHostDevicesReadChildRuntimeWithoutInventingDirectPath(t *testing.T) {
	m := testManager(t.TempDir())
	started, err := m.StartServerMode(StartServerRequest{ServerAddress: "desk.example.com", ListenPort: 9443})
	if err != nil {
		t.Fatal(err)
	}
	hub, err := config.Load(started.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	host, err := config.Load(resolveConfigPath(m.configsDir(), hub.ServerNodeConfig))
	if err != nil {
		t.Fatal(err)
	}
	status := runtimeStatus{State: "running", NetworkState: networkstate.Connected, CoordinatorState: "connected", P2PListen: "0.0.0.0:9443", UpdatedAt: time.Now(), Self: nodeStatus{NodeID: host.NodeID, Mode: "spoke", VirtualIP: host.VirtualIP}, Peers: []peerStatus{{NodeID: "B", Mode: "spoke", Status: "online", VirtualIP: "10.77.0.2"}}}
	if err := writePrettyJSON(statusPath(started.ConfigPath, "test-service")+".server-node.json", status); err != nil {
		t.Fatal(err)
	}
	devices, err := m.Devices("test-service")
	if err != nil {
		t.Fatal(err)
	}
	if len(devices.Nodes) != 2 || devices.Nodes[0].NodeID != host.NodeID || devices.P2PListen != "0.0.0.0:9443" {
		t.Fatalf("wrong host projection: %+v", devices)
	}
	for _, node := range devices.Nodes {
		if node.PathType != "" {
			t.Fatalf("control membership invented direct path: %+v", node)
		}
	}
}

func TestServerRestartWithMissingHostKeyPreservesActiveConfig(t *testing.T) {
	m := testManager(t.TempDir())
	req := StartServerRequest{ServerAddress: "desk.example.com", ListenPort: 9443}
	started, err := m.StartServerMode(req)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(started.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	var hub config.Config
	if err := json.Unmarshal(before, &hub); err != nil {
		t.Fatal(err)
	}
	host, err := config.Load(resolveConfigPath(m.configsDir(), hub.ServerNodeConfig))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(resolveConfigPath(m.configsDir(), host.KeyFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartServerMode(req); err == nil {
		t.Fatal("silently replaced damaged host identity")
	}
	after, err := os.ReadFile(started.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("failed restart removed the server mesh node configuration")
	}
}
