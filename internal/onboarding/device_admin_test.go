package onboarding

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"meshlink/internal/certutil"
	"meshlink/internal/config"
	"meshlink/internal/networkstate"
	"meshlink/internal/p2p"
)

func TestDevicesHideInfrastructureAndRespectReconnectState(t *testing.T) {
	dir := t.TempDir()
	writeSpokeConfigForDeviceTest(t, dir, "desk")
	writeRuntimeStatusForDeviceTest(t, dir, `{
  "state":"running",
  "network_state":"reconnecting",
  "self":{"node_id":"desk","mode":"spoke","virtual_ip":"10.77.0.2"},
  "peers":[
    {"node_id":"hub","mode":"hub","status":"online"},
    {"node_id":"office","mode":"spoke","status":"offline","virtual_ip":"10.77.0.3"},
    {"node_id":"studio","mode":"spoke","status":"online","virtual_ip":"10.77.0.4"}
  ]
}`)
	devices, err := (Manager{BaseDir: dir}).Devices("mesh-agent")
	if err != nil {
		t.Fatal(err)
	}
	if devices.NetworkState != networkstate.Reconnecting {
		t.Fatalf("network state = %q", devices.NetworkState)
	}
	if findDeviceSummary(devices, "hub") != nil {
		t.Fatal("hub must be hidden")
	}
	if peer := findDeviceSummary(devices, "office"); peer == nil || peer.Status != "offline" {
		t.Fatalf("office = %+v, want offline", peer)
	}
	if peer := findDeviceSummary(devices, "studio"); peer == nil || peer.Status != "offline" {
		t.Fatalf("studio = %+v, want reconnecting state to force offline", peer)
	}
}

func TestDevicesWithoutActiveConfigAreNotJoinedAndEmpty(t *testing.T) {
	devices, err := (Manager{BaseDir: t.TempDir()}).Devices("mesh-agent")
	if err != nil {
		t.Fatal(err)
	}
	if devices.NetworkState != networkstate.NotJoined || len(devices.Nodes) != 0 {
		t.Fatalf("devices = %+v", devices)
	}
}

func TestDeviceAdminRenameDisableRemoveAndAudit(t *testing.T) {
	dir := t.TempDir()
	mgr := testManager(dir)
	enrollDeviceForAdminTest(t, mgr, "laptop")

	renamed, err := mgr.RenameDevice("laptop", "Alice laptop")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.DisplayName != "Alice laptop" {
		t.Fatalf("DisplayName = %q, want Alice laptop", renamed.DisplayName)
	}

	disabled, err := mgr.DisableDevice("laptop")
	if err != nil {
		t.Fatal(err)
	}
	if !disabled.Disabled {
		t.Fatal("DisableDevice did not mark registry node disabled")
	}
	rejection, rejected, err := mgr.DeviceRejection("laptop", disabled.CertFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if !rejected || !strings.Contains(rejection.Reason, "device disabled") {
		t.Fatalf("DeviceRejection = %+v rejected=%v, want disabled rejection", rejection, rejected)
	}

	devices, err := mgr.Devices("")
	if err != nil {
		t.Fatal(err)
	}
	peer := findDeviceSummary(devices, "laptop")
	if peer == nil {
		t.Fatalf("disabled peer missing from device list: %+v", devices.Nodes)
	}
	if peer.Status != "disabled" || peer.DisplayName != "Alice laptop" {
		t.Fatalf("disabled peer summary = %+v, want disabled Alice laptop", *peer)
	}

	removed, err := mgr.RemoveDevice("laptop")
	if err != nil {
		t.Fatal(err)
	}
	if removed.DeletedAt == nil {
		t.Fatal("RemoveDevice did not set DeletedAt")
	}
	devices, err = mgr.Devices("")
	if err != nil {
		t.Fatal(err)
	}
	if peer := findDeviceSummary(devices, "laptop"); peer != nil {
		t.Fatalf("removed device still listed as normal peer: %+v", *peer)
	}

	events := readAuditEvents(t, dir)
	assertAuditEvent(t, events, "invite_created")
	assertAuditEvent(t, events, "enroll_succeeded")
	assertAuditEvent(t, events, "device_renamed")
	assertAuditEvent(t, events, "device_disabled")
	assertAuditEvent(t, events, "device_removed")
	assertAuditLogHasNoPrivateMaterial(t, dir)
}

func TestDeviceRegistryPolicyAppliesToRuntimeDeviceList(t *testing.T) {
	dir := t.TempDir()
	mgr := testManager(dir)
	enrolled := enrollDeviceForAdminTest(t, mgr, "laptop")
	if _, err := mgr.DisableDevice("laptop"); err != nil {
		t.Fatal(err)
	}
	statusDir := filepath.Join(dir, "configs", "logs")
	if err := os.MkdirAll(statusDir, 0o700); err != nil {
		t.Fatal(err)
	}
	statusPath := filepath.Join(statusDir, "mesh-agent.status.json")
	if err := os.WriteFile(statusPath, []byte(`{
  "updated_at": "2026-07-08T08:00:00Z",
  "state": "running",
  "self": {"node_id": "hub", "mode": "hub", "virtual_ip": "10.77.0.1"},
  "peers": [{
    "node_id": "laptop",
    "status": "online",
    "virtual_ip": "10.77.0.2",
    "fingerprint": "`+enrolled.CertFingerprint+`",
    "last_seen": "2026-07-08T08:00:00Z"
  }]
}`), 0o600); err != nil {
		t.Fatal(err)
	}

	devices, err := mgr.Devices("mesh-agent")
	if err != nil {
		t.Fatal(err)
	}
	peer := findDeviceSummary(devices, "laptop")
	if peer == nil {
		t.Fatalf("runtime peer missing from device list: %+v", devices.Nodes)
	}
	if peer.Status != "disabled" {
		t.Fatalf("runtime peer status = %q, want disabled", peer.Status)
	}

	if _, err := mgr.RemoveDevice("laptop"); err != nil {
		t.Fatal(err)
	}
	devices, err = mgr.Devices("mesh-agent")
	if err != nil {
		t.Fatal(err)
	}
	if peer := findDeviceSummary(devices, "laptop"); peer != nil {
		t.Fatalf("deleted runtime peer still shown: %+v", *peer)
	}
}

func TestDevicesReadsRuntimeConnectionPathQualityFields(t *testing.T) {
	dir := t.TempDir()
	mgr := testManager(dir)
	enrolled := enrollDeviceForAdminTest(t, mgr, "laptop")
	statusDir := filepath.Join(dir, "configs", "logs")
	if err := os.MkdirAll(statusDir, 0o700); err != nil {
		t.Fatal(err)
	}
	statusPath := filepath.Join(statusDir, "mesh-agent.status.json")
	if err := os.WriteFile(statusPath, []byte(`{
  "updated_at": "2026-07-09T08:00:00Z",
  "state": "running",
  "self": {"node_id": "hub", "mode": "hub", "virtual_ip": "10.77.0.1"},
  "peers": [{
    "node_id": "laptop",
    "status": "online",
    "virtual_ip": "10.77.0.2",
    "fingerprint": "`+enrolled.CertFingerprint+`",
    "path_type": "relay",
    "path_state": "fallback_relay",
    "latency_ms": 88,
    "relay_bytes_in": 64,
    "relay_bytes_out": 96,
    "switch_count": 1,
    "switch_reasons": ["direct_quality_degraded", "token should not leak"],
    "switch_from_path": "lan_direct",
    "switch_to_path": "relay",
    "switch_score_delta": 31,
    "auto_switched": true,
    "last_error": "password should not leak",
    "last_seen": "2026-07-09T08:00:00Z"
  }]
}`), 0o600); err != nil {
		t.Fatal(err)
	}

	devices, err := mgr.Devices("mesh-agent")
	if err != nil {
		t.Fatal(err)
	}
	peer := findDeviceSummary(devices, "laptop")
	if peer == nil {
		t.Fatalf("runtime peer missing from device list: %+v", devices.Nodes)
	}
	if peer.PathType != p2p.PathTypeRelay || peer.PathState != p2p.PathStateFallbackRelay {
		t.Fatalf("peer path = %+v, want relay fallback", peer.ConnectionStatus)
	}
	if peer.LatencyMS != 88 || peer.RelayBytesIn != 64 || peer.RelayBytesOut != 96 || peer.QualityScore == 0 {
		t.Fatalf("peer quality = %+v, want runtime latency, relay bytes, and score", peer.ConnectionStatus)
	}
	if peer.SwitchCount != 2 || len(peer.SwitchReasons) != 2 || peer.SwitchReasons[1] != "redacted" || peer.LastError != "redacted" {
		t.Fatalf("peer sanitized fields = %+v, want sensitive switch reason and last_error redacted", peer.ConnectionStatus)
	}
	if peer.SwitchFromPath != p2p.PathTypeLANDirect || peer.SwitchToPath != p2p.PathTypeRelay || peer.SwitchScoreDelta != 31 || !peer.AutoSwitched {
		t.Fatalf("peer switch summary = %+v, want Task 7E fields", peer.ConnectionStatus)
	}
}

func TestDevicesDefaultsOldRuntimeStatusConnectionFields(t *testing.T) {
	dir := t.TempDir()
	mgr := testManager(dir)
	enrolled := enrollDeviceForAdminTest(t, mgr, "laptop")
	writeSpokeConfigForDeviceTest(t, dir, "desk")
	statusDir := filepath.Join(dir, "configs", "logs")
	if err := os.MkdirAll(statusDir, 0o700); err != nil {
		t.Fatal(err)
	}
	statusPath := filepath.Join(statusDir, "mesh-agent.status.json")
	if err := os.WriteFile(statusPath, []byte(`{
  "updated_at": "2026-07-09T08:00:00Z",
  "state": "running",
  "self": {"node_id": "desk", "mode": "spoke", "virtual_ip": "10.77.0.2"},
  "peers": [{
    "node_id": "laptop",
    "status": "online",
    "virtual_ip": "10.77.0.2",
    "fingerprint": "`+enrolled.CertFingerprint+`",
    "last_seen": "2026-07-09T08:00:00Z"
  }]
}`), 0o600); err != nil {
		t.Fatal(err)
	}

	devices, err := mgr.Devices("mesh-agent")
	if err != nil {
		t.Fatal(err)
	}
	if devices.NetworkState != networkstate.Connected {
		t.Fatalf("network state = %q, want connected for legacy running status", devices.NetworkState)
	}
	self := findDeviceSummary(devices, "desk")
	if self == nil {
		t.Fatalf("self missing from device list: %+v", devices.Nodes)
	}
	if self.PathType != "" || self.PathState != p2p.PathStateIdle {
		t.Fatalf("self path = %+v, legacy process presence must not invent direct", self.ConnectionStatus)
	}
	peer := findDeviceSummary(devices, "laptop")
	if peer == nil {
		t.Fatalf("runtime peer missing from device list: %+v", devices.Nodes)
	}
	if peer.PathType != "" || peer.PathState != p2p.PathStateIdle {
		t.Fatalf("peer path = %+v, legacy presence must not invent direct", peer.ConnectionStatus)
	}
	if peer.LatencyMS != 0 || peer.RelayBytesIn != 0 || peer.RelayBytesOut != 0 || peer.LastError != "" || peer.QualityScore != 0 {
		t.Fatalf("peer defaults = %+v, want conservative zero metrics until handshake", peer.ConnectionStatus)
	}
}

func TestDevicesOmitHubSelfAndRetainSpokePeers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "configs", "active.json")
	cfg := config.Config{
		NodeID: "hub", Mode: "hub", Listen: ":8443", VirtualIP: "10.77.0.1",
		CAFile: "../certs/ca.pem", CertFile: "../certs/hub.pem",
		KeyFile: "../certs/hub-key.pem", Device: config.DeviceConfig{Type: "null"},
	}
	if err := writePrettyJSON(path, cfg); err != nil {
		t.Fatal(err)
	}
	writeRuntimeStatusForDeviceTest(t, dir, `{
  "state":"running",
  "network_state":"connected",
  "self":{"node_id":"hub","mode":"hub","virtual_ip":"10.77.0.1"},
  "peers":[{"node_id":"desk","mode":"spoke","status":"online","virtual_ip":"10.77.0.2"}]
}`)

	devices, err := (Manager{BaseDir: dir}).Devices("mesh-agent")
	if err != nil {
		t.Fatal(err)
	}
	if findDeviceSummary(devices, "hub") != nil {
		t.Fatal("hub self must be hidden")
	}
	if peer := findDeviceSummary(devices, "desk"); peer == nil || peer.Status != "online" {
		t.Fatalf("desk = %+v, want online spoke", peer)
	}
}

func TestInviteFailureAuditInvalidationAndPrivateMaterialRedaction(t *testing.T) {
	dir := t.TempDir()
	mgr := testManager(dir)
	if _, err := mgr.CreateHub(CreateHubRequest{NetworkName: "mesh", NodeName: "hub", ListenPort: 8443}); err != nil {
		t.Fatal(err)
	}
	invite, err := mgr.CreateInvite(CreateInviteRequest{Server: "example.com:8443"})
	if err != nil {
		t.Fatal(err)
	}
	csr, err := certutil.CreateCSR(certutil.CSROptions{OutDir: t.TempDir(), Name: "laptop"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := mgr.HandleEnroll(EnrollRequest{
			Token:      invite.Token,
			Code:       "000000",
			NodeName:   "laptop",
			CSRPEM:     csr.CSRPEM,
			SourceAddr: "198.51.100.20:55123",
		}); err == nil {
			t.Fatalf("attempt %d unexpectedly succeeded", i+1)
		}
	}

	events := readAuditEvents(t, dir)
	if got := countAuditEvents(events, "enroll_verification_failed"); got != 5 {
		t.Fatalf("verification failure audit count = %d, want 5", got)
	}
	assertAuditEvent(t, events, "invite_invalidated")
	assertAuditLogHasNoPrivateMaterial(t, dir)
}

func TestLongLivedInviteMaxUsesRejectsFourthDevice(t *testing.T) {
	dir := t.TempDir()
	mgr := testManager(dir)
	if _, err := mgr.CreateHub(CreateHubRequest{NetworkName: "mesh", NodeName: "hub", ListenPort: 8443}); err != nil {
		t.Fatal(err)
	}
	invite, err := mgr.CreateInvite(CreateInviteRequest{
		Server:    "example.com:8443",
		LongLived: true,
		MaxUses:   3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !invite.LongLived || invite.MaxUses != 3 {
		t.Fatalf("invite = %+v, want long-lived MaxUses=3", invite)
	}

	for _, nodeID := range []string{"laptop", "tablet", "office"} {
		csr, err := certutil.CreateCSR(certutil.CSROptions{OutDir: t.TempDir(), Name: nodeID})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := mgr.HandleEnroll(EnrollRequest{
			Token:    invite.Token,
			Code:     invite.Code,
			NodeName: nodeID,
			CSRPEM:   csr.CSRPEM,
		}); err != nil {
			t.Fatalf("enroll %s: %v", nodeID, err)
		}
	}

	csr, err := certutil.CreateCSR(certutil.CSROptions{OutDir: t.TempDir(), Name: "fourth"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = mgr.HandleEnroll(EnrollRequest{
		Token:    invite.Token,
		Code:     invite.Code,
		NodeName: "fourth",
		CSRPEM:   csr.CSRPEM,
	})
	if err == nil {
		t.Fatal("expected fourth long-lived invite use to fail")
	}
	if !strings.Contains(err.Error(), "invite device limit has been reached") {
		t.Fatalf("error = %q, want device limit", err.Error())
	}
}

func TestLongLivedInviteDefaultsToLimitedDeviceCount(t *testing.T) {
	dir := t.TempDir()
	mgr := testManager(dir)
	invite, err := mgr.CreateInvite(CreateInviteRequest{
		Server:    "example.com:8443",
		LongLived: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if invite.MaxUses <= 0 {
		t.Fatalf("long-lived invite MaxUses = %d, want required device limit", invite.MaxUses)
	}
	var store InviteStore
	readJSON(t, filepath.Join(dir, "invites", "invites.json"), &store)
	if len(store.Invites) != 1 || store.Invites[0].MaxUses != invite.MaxUses {
		t.Fatalf("stored invite = %+v, result = %+v", store.Invites, invite)
	}
}

func TestLookupDeviceUsesExactPersistedIdentityAndIncludesRevocation(t *testing.T) {
	mgr := testManager(t.TempDir())
	enrolled := enrollDeviceForAdminTest(t, mgr, "laptop")
	lookup, ok := any(mgr).(interface {
		LookupDevice(string) (RegisteredNode, error)
	})
	if !ok {
		t.Fatal("Manager is missing persisted exact LookupDevice")
	}
	for _, id := range []string{"LAPTOP", " laptop ", "missing", ""} {
		if _, err := lookup.LookupDevice(id); err == nil {
			t.Fatalf("lookup accepted non-exact identity %q", id)
		}
	}
	node, err := lookup.LookupDevice("laptop")
	if err != nil || node.CertFingerprint != enrolled.CertFingerprint || node.VirtualIP != enrolled.VirtualIP {
		t.Fatalf("lookup: %+v %v", node, err)
	}
	if _, err = mgr.DisableDevice("laptop"); err != nil {
		t.Fatal(err)
	}
	node, err = lookup.LookupDevice("laptop")
	if err != nil || !node.Disabled {
		t.Fatalf("lookup did not reread disable: %+v %v", node, err)
	}
	if _, err = mgr.RemoveDevice("laptop"); err != nil {
		t.Fatal(err)
	}
	node, err = lookup.LookupDevice("laptop")
	if err != nil || node.DeletedAt == nil {
		t.Fatalf("lookup did not reread deletion: %+v %v", node, err)
	}
}

func enrollDeviceForAdminTest(t *testing.T, mgr Manager, nodeID string) RegisteredNode {
	t.Helper()
	if _, err := mgr.CreateHub(CreateHubRequest{NetworkName: "mesh", NodeName: "hub", ListenPort: 8443}); err != nil {
		t.Fatal(err)
	}
	invite, err := mgr.CreateInvite(CreateInviteRequest{Server: "example.com:8443"})
	if err != nil {
		t.Fatal(err)
	}
	csr, err := certutil.CreateCSR(certutil.CSROptions{OutDir: t.TempDir(), Name: nodeID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.HandleEnroll(EnrollRequest{
		Token:      invite.Token,
		Code:       invite.Code,
		NodeName:   nodeID,
		CSRPEM:     csr.CSRPEM,
		SourceAddr: "198.51.100.20:55123",
	}); err != nil {
		t.Fatal(err)
	}
	var registry DeviceRegistry
	readJSON(t, filepath.Join(mgr.baseDir(), "configs", "devices.json"), &registry)
	if len(registry.Nodes) != 1 {
		t.Fatalf("registry = %+v, want one node", registry)
	}
	return registry.Nodes[0]
}

func findDeviceSummary(devices DeviceList, nodeID string) *DeviceSummary {
	for i := range devices.Nodes {
		if devices.Nodes[i].NodeID == nodeID {
			return &devices.Nodes[i]
		}
	}
	return nil
}

func writeSpokeConfigForDeviceTest(t *testing.T, dir, nodeID string) {
	t.Helper()
	path := filepath.Join(dir, "configs", "active.json")
	cfg := config.Config{
		NodeID: nodeID, Mode: "spoke", Connect: "example.com:8443",
		CAFile: "../certs/ca.pem", CertFile: "../certs/" + nodeID + ".pem",
		KeyFile: "../certs/" + nodeID + "-key.pem", VirtualIP: "10.77.0.2",
		Device: config.DeviceConfig{Type: "null"},
	}
	if err := writePrettyJSON(path, cfg); err != nil {
		t.Fatal(err)
	}
}

func writeRuntimeStatusForDeviceTest(t *testing.T, dir, body string) {
	t.Helper()
	path := filepath.Join(dir, "configs", "logs", "mesh-agent.status.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readAuditEvents(t *testing.T, baseDir string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(baseDir, "logs", "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	events := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("invalid audit line %q: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}

func assertAuditEvent(t *testing.T, events []map[string]any, eventType string) {
	t.Helper()
	if countAuditEvents(events, eventType) == 0 {
		t.Fatalf("missing audit event %q in %+v", eventType, events)
	}
}

func countAuditEvents(events []map[string]any, eventType string) int {
	count := 0
	for _, event := range events {
		if event["event"] == eventType {
			count++
		}
	}
	return count
}

func assertAuditLogHasNoPrivateMaterial(t *testing.T, baseDir string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(baseDir, "logs", "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"PRIVATE KEY", "BEGIN RSA", "BEGIN EC", "rdp_content"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("audit log contains forbidden private/user-content marker %q: %s", forbidden, string(b))
		}
	}
}
