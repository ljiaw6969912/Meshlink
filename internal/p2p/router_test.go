package p2p

import (
	"errors"
	"net/netip"
	"testing"

	"meshlink/internal/proto"
)

func TestRouteTableUsesLongestPrefixAndRejectsEqualPrefixConflict(t *testing.T) {
	members := []proto.Member{
		{NodeID: "node-b", VirtualIP: "10.0.0.2", Routes: []string{"10.0.0.0/8"}},
		{NodeID: "node-c", VirtualIP: "10.77.0.3", Routes: []string{"10.77.0.0/24"}},
		{NodeID: "node-d", VirtualIP: "10.77.0.42", Routes: []string{}},
	}
	var table RouteTable
	if err := table.Replace(members); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		address string
		nodeID  string
	}{
		{address: "10.77.0.9", nodeID: "node-c"},
		{address: "10.77.0.42", nodeID: "node-d"},
		{address: "10.88.0.9", nodeID: "node-b"},
	}
	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			member, ok := table.Lookup(netip.MustParseAddr(tt.address))
			if !ok {
				t.Fatalf("Lookup(%s) did not find a route", tt.address)
			}
			if member.NodeID != tt.nodeID {
				t.Fatalf("Lookup(%s) owner = %q, want %q", tt.address, member.NodeID, tt.nodeID)
			}
		})
	}

	conflicting := append([]proto.Member(nil), members...)
	conflicting = append(conflicting, proto.Member{NodeID: "node-e", VirtualIP: "10.99.0.5", Routes: []string{"10.77.0.0/24"}})
	if err := table.Replace(conflicting); !errors.Is(err, ErrRouteConflict) {
		t.Fatalf("Replace conflict error = %v, want ErrRouteConflict", err)
	}
	member, ok := table.Lookup(netip.MustParseAddr("10.77.0.9"))
	if !ok || member.NodeID != "node-c" {
		t.Fatalf("table changed after rejected replacement: member=%+v ok=%t", member, ok)
	}

	duplicateVirtualIP := []proto.Member{
		{NodeID: "node-b", VirtualIP: "10.77.0.3"},
		{NodeID: "node-c", VirtualIP: "10.77.0.3"},
	}
	if err := table.Replace(duplicateVirtualIP); !errors.Is(err, ErrRouteConflict) {
		t.Fatalf("duplicate virtual IP error = %v, want ErrRouteConflict", err)
	}
}

func TestRouteTableRejectsInvalidIPv4Membership(t *testing.T) {
	tests := []struct {
		name   string
		member proto.Member
	}{
		{name: "empty node id", member: proto.Member{VirtualIP: "10.77.0.3"}},
		{name: "invalid virtual ip", member: proto.Member{NodeID: "node-c", VirtualIP: "invalid"}},
		{name: "IPv6 virtual ip", member: proto.Member{NodeID: "node-c", VirtualIP: "fd77::3"}},
		{name: "invalid route", member: proto.Member{NodeID: "node-c", VirtualIP: "10.77.0.3", Routes: []string{"invalid"}}},
		{name: "IPv6 route", member: proto.Member{NodeID: "node-c", VirtualIP: "10.77.0.3", Routes: []string{"fd77::/64"}}},
		{name: "public route", member: proto.Member{NodeID: "node-c", VirtualIP: "10.77.0.3", Routes: []string{"203.0.113.0/24"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var table RouteTable
			if err := table.Replace([]proto.Member{tt.member}); err == nil {
				t.Fatal("Replace returned nil error")
			}
		})
	}
}

func TestRouteTableVirtualIPHonorsRFC1918Boundaries(t *testing.T) {
	tests := []struct {
		address string
		valid   bool
	}{
		{address: "10.0.0.0", valid: true},
		{address: "10.255.255.255", valid: true},
		{address: "172.15.255.255", valid: false},
		{address: "172.16.0.0", valid: true},
		{address: "172.31.255.255", valid: true},
		{address: "172.32.0.0", valid: false},
		{address: "192.168.0.0", valid: true},
		{address: "192.168.255.255", valid: true},
		{address: "0.0.0.0", valid: false},
		{address: "127.0.0.1", valid: false},
		{address: "169.254.1.1", valid: false},
		{address: "203.0.113.7", valid: false},
		{address: "224.0.0.1", valid: false},
	}
	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			var table RouteTable
			err := table.Replace([]proto.Member{{NodeID: "node-c", VirtualIP: tt.address}})
			if tt.valid && err != nil {
				t.Fatalf("Replace rejected RFC1918 virtual IP %s: %v", tt.address, err)
			}
			if !tt.valid && err == nil {
				t.Fatalf("Replace accepted non-RFC1918 virtual IP %s", tt.address)
			}
		})
	}
}

func TestValidateInboundPacketRejectsSpoofedSourceAndForeignDestination(t *testing.T) {
	peer := proto.Member{
		NodeID:    "node-c",
		VirtualIP: "10.77.0.3",
		Routes:    []string{"192.168.50.0/24"},
	}
	localVirtualIP := netip.MustParseAddr("10.77.0.2")
	localRoutes := []netip.Prefix{netip.MustParsePrefix("172.16.0.0/16")}

	validVirtualIPs := []byte{
		0x45, 0x00, 0x00, 0x14,
		0x00, 0x00, 0x00, 0x00,
		0x40, 0x06, 0x00, 0x00,
		10, 77, 0, 3,
		10, 77, 0, 2,
	}
	if err := ValidateInboundPacket(peer, localVirtualIP, localRoutes, validVirtualIPs); err != nil {
		t.Fatalf("valid virtual-IP packet rejected: %v", err)
	}

	validDeclaredRoutes := []byte{
		0x45, 0x00, 0x00, 0x14,
		0x00, 0x00, 0x00, 0x00,
		0x40, 0x06, 0x00, 0x00,
		192, 168, 50, 8,
		172, 16, 4, 9,
	}
	if err := ValidateInboundPacket(peer, localVirtualIP, localRoutes, validDeclaredRoutes); err != nil {
		t.Fatalf("valid routed packet rejected: %v", err)
	}

	spoofedSource := []byte{
		0x45, 0x00, 0x00, 0x14,
		0x00, 0x00, 0x00, 0x00,
		0x40, 0x06, 0x00, 0x00,
		10, 77, 0, 4,
		10, 77, 0, 2,
	}
	if err := ValidateInboundPacket(peer, localVirtualIP, localRoutes, spoofedSource); !errors.Is(err, ErrSpoofedSource) {
		t.Fatalf("spoofed source error = %v, want ErrSpoofedSource", err)
	}

	foreignDestination := []byte{
		0x45, 0x00, 0x00, 0x14,
		0x00, 0x00, 0x00, 0x00,
		0x40, 0x06, 0x00, 0x00,
		10, 77, 0, 3,
		10, 77, 0, 9,
	}
	if err := ValidateInboundPacket(peer, localVirtualIP, localRoutes, foreignDestination); !errors.Is(err, ErrForeignDestination) {
		t.Fatalf("foreign destination error = %v, want ErrForeignDestination", err)
	}
}

func TestValidateInboundPacketFailsClosedOnInvalidAuthorization(t *testing.T) {
	packet := []byte{
		0x45, 0x00, 0x00, 0x14,
		0x00, 0x00, 0x00, 0x00,
		0x40, 0x06, 0x00, 0x00,
		10, 77, 0, 3,
		10, 77, 0, 2,
	}
	tests := []struct {
		name           string
		peer           proto.Member
		localVirtualIP netip.Addr
		localRoutes    []netip.Prefix
		packet         []byte
	}{
		{name: "invalid peer virtual ip", peer: proto.Member{NodeID: "node-c", VirtualIP: "invalid"}, localVirtualIP: netip.MustParseAddr("10.77.0.2"), packet: packet},
		{name: "public peer virtual ip", peer: proto.Member{NodeID: "node-c", VirtualIP: "203.0.113.7"}, localVirtualIP: netip.MustParseAddr("10.77.0.2"), packet: packet},
		{name: "empty peer node id", peer: proto.Member{VirtualIP: "10.77.0.3"}, localVirtualIP: netip.MustParseAddr("10.77.0.2"), packet: packet},
		{name: "invalid peer route", peer: proto.Member{NodeID: "node-c", VirtualIP: "10.77.0.3", Routes: []string{"invalid"}}, localVirtualIP: netip.MustParseAddr("10.77.0.2"), packet: packet},
		{name: "public peer route", peer: proto.Member{NodeID: "node-c", VirtualIP: "10.77.0.3", Routes: []string{"203.0.113.0/24"}}, localVirtualIP: netip.MustParseAddr("10.77.0.2"), packet: packet},
		{name: "invalid local virtual ip", peer: proto.Member{NodeID: "node-c", VirtualIP: "10.77.0.3"}, localVirtualIP: netip.Addr{}, packet: packet},
		{name: "public local virtual ip", peer: proto.Member{NodeID: "node-c", VirtualIP: "10.77.0.3"}, localVirtualIP: netip.MustParseAddr("203.0.113.7"), packet: packet},
		{name: "IPv6 local route", peer: proto.Member{NodeID: "node-c", VirtualIP: "10.77.0.3"}, localVirtualIP: netip.MustParseAddr("10.77.0.2"), localRoutes: []netip.Prefix{netip.MustParsePrefix("fd77::/64")}, packet: packet},
		{name: "public local route", peer: proto.Member{NodeID: "node-c", VirtualIP: "10.77.0.3"}, localVirtualIP: netip.MustParseAddr("10.77.0.2"), localRoutes: []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}, packet: packet},
		{name: "malformed packet", peer: proto.Member{NodeID: "node-c", VirtualIP: "10.77.0.3"}, localVirtualIP: netip.MustParseAddr("10.77.0.2"), packet: []byte{0x45}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateInboundPacket(tt.peer, tt.localVirtualIP, tt.localRoutes, tt.packet); err == nil {
				t.Fatal("ValidateInboundPacket returned nil error")
			}
		})
	}
}
