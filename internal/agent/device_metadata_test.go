package agent

import (
	"bytes"
	"crypto/sha256"
	"net"
	"testing"

	"meshlink/internal/proto"
)

func connectMetadata(t *testing.T, f *coordinatorFixture, id, name, mac string) *coordinatorClient {
	t.Helper()
	p := f.dial(id)
	h := f.hello(id)
	h.Capabilities = proto.DeviceMetadataCapabilities(name, mac)
	p.send(proto.ControlTypeClientHello, h)
	p.want(proto.ControlTypeServerHello)
	p.want(proto.ControlTypeProbeCredential)
	p.want(proto.ControlTypeMemberSnapshot)
	return p
}

func TestCoordinatorMetadataAdmissionAndLegacySnapshots(t *testing.T) {
	f := newCoordinatorFixture(t, true)
	legacy, _ := f.connect("C")
	modern := connectMetadata(t, f, "B", "办公电脑", "02:11:22:33:44:55")
	node, err := f.coordinator.manager.LookupDevice("B")
	if err != nil || node.DisplayName != "办公电脑" || node.MACAddress != "02:11:22:33:44:55" {
		t.Fatalf("stored metadata: %+v %v", node, err)
	}
	env := legacy.want(proto.ControlTypeMemberSnapshot)
	if bytes.Contains(env.Body, []byte("display_name")) {
		t.Fatalf("legacy snapshot contains new field: %s", env.Body)
	}
	// Metadata must not permit a different certificate to claim B.
	bad := f.dial("C")
	hello := f.hello("B")
	hello.Capabilities = proto.DeviceMetadataCapabilities("hijacked", "02:11:22:33:44:55")
	bad.send(proto.ControlTypeClientHello, hello)
	bad.want(proto.ControlTypeError)
	node, err = f.coordinator.manager.LookupDevice("B")
	if err != nil || node.DisplayName != "办公电脑" {
		t.Fatalf("unauthenticated metadata changed registry: %+v %v", node, err)
	}
	conflict := f.dial("C")
	conflictHello := f.hello("C")
	conflictHello.Capabilities = proto.DeviceMetadataCapabilities("duplicate", "02:11:22:33:44:55")
	conflict.send(proto.ControlTypeClientHello, conflictHello)
	if got := decodeCoordinatorBody[proto.ControlError](t, conflict.want(proto.ControlTypeError)); got.Code != "device_metadata_conflict" {
		t.Fatalf("MAC binding conflict: %+v", got)
	}
	legacy.barrier()
	modern.barrier()
}

func TestCoordinatorMetadataRenamePreservesEstablishedRelationship(t *testing.T) {
	f := newCoordinatorFixture(t, true)
	b := connectMetadata(t, f, "B", "before", "02:11:22:33:44:55")
	c := connectMetadata(t, f, "C", "other", "02:11:22:33:44:66")
	coordinatorCompleteSession(t, b, c)
	node, err := f.coordinator.manager.LookupDevice("B")
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the real registry mutation used by the UI.
	if _, err := f.coordinator.manager.RenameDevice(node.NodeID, "after"); err != nil {
		t.Fatal(err)
	}
	f.coordinator.reconcileRegistry()
	snapshot := decodeCoordinatorBody[proto.MemberSnapshot](t, c.want(proto.ControlTypeMemberSnapshot))
	found := false
	for _, member := range snapshot.Members {
		if member.NodeID == "B" {
			found = member.DisplayName == "after"
		}
	}
	if !found {
		t.Fatalf("rename missing in snapshot: %+v", snapshot)
	}
	b.absent(proto.ControlTypeDisconnectPeer)
	c.absent(proto.ControlTypeDisconnectPeer)
	b.barrier()
	c.barrier()
}

// Simulated peers in one process model distinct physical devices.
func withTestDeviceMAC(nodeID string) Option {
	sum := sha256.Sum256([]byte(nodeID))
	mac := net.HardwareAddr{0x02, sum[0], sum[1], sum[2], sum[3], sum[4]}.String()
	return func(o *options) { o.localMAC = func() string { return mac } }
}
