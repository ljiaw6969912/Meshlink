package onboarding

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"meshlink/internal/certutil"
)

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
