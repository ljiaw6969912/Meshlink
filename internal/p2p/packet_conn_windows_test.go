//go:build windows

package p2p

import (
	"bytes"
	"container/list"
	"encoding/binary"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestCandidateUDPRetainsIngressRouteAndReportsFullReply(t *testing.T) {
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	conn, err := candidatePacketConn(server)
	if err != nil {
		t.Fatal(err)
	}
	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	deadline := time.Now().Add(3 * time.Second)
	conn.SetDeadline(deadline)
	client.SetDeadline(deadline)
	target := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: server.LocalAddr().(*net.UDPAddr).Port}
	if _, err := client.WriteToUDP([]byte("request"), target); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 128)
	n, remote, err := conn.ReadFrom(buffer)
	if err != nil {
		t.Fatal(err)
	}
	routed, ok := conn.(*routedPacketConn)
	if !ok {
		t.Fatal("candidate socket does not retain incoming packet information")
	}
	route, found := routed.replyRoute(remote.(*net.UDPAddr).AddrPort(), time.Now())
	if !found || route.address != [4]byte{127, 0, 0, 1} || route.ifIndex == 0 {
		t.Fatalf("incoming address/interface were lost: %+v, found=%v", route, found)
	}
	if written, err := conn.WriteTo(buffer[:n], remote); err != nil || written != n {
		t.Fatalf("reply wrote %d of %d bytes: %v", written, n, err)
	}
	n, source, err := client.ReadFromUDP(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if !source.IP.Equal(target.IP) || source.Port != target.Port {
		t.Fatalf("UDP reply used %s, client contacted %s: wildcard sockets must retain the incoming interface/address", source, target)
	}
	if !bytes.Equal(buffer[:n], []byte("request")) {
		t.Fatalf("reply was not delivered intact: %q", buffer[:n])
	}
}

func TestReplyRouteCacheIsBoundedExpiresAndTracksPortChanges(t *testing.T) {
	c := &routedPacketConn{routes: make(map[netip.AddrPort]*list.Element)}
	now := time.Now()
	route := replyRoute{address: [4]byte{192, 0, 2, 1}, ifIndex: 5}
	for port := 1; port <= maxReplyRoutes+1; port++ {
		c.rememberRoute(netip.AddrPortFrom(netip.MustParseAddr("203.0.113.1"), uint16(port)), route, now)
	}
	if len(c.routes) != maxReplyRoutes || c.recent.Len() != maxReplyRoutes {
		t.Fatal("unauthenticated datagrams can grow route memory beyond its bound")
	}
	if _, ok := c.replyRoute(netip.MustParseAddrPort("203.0.113.1:1"), now); ok {
		t.Fatal("oldest route was not evicted")
	}
	remote := netip.MustParseAddrPort("203.0.113.1:2")
	route.ifIndex = 7
	c.rememberRoute(remote, route, now.Add(time.Second))
	if got, ok := c.replyRoute(remote, now.Add(replyRouteTTL)); !ok || got != route {
		t.Fatal("latest incoming route did not replace and refresh the old route")
	}
	if _, ok := c.replyRoute(remote, now.Add(replyRouteTTL+time.Second)); ok {
		t.Fatal("stale interface route survived its expiry")
	}
	if _, ok := c.replyRoute(netip.MustParseAddrPort("203.0.113.1:60000"), now); ok {
		t.Fatal("route was reused for another endpoint's port")
	}
}

func TestReplyRouteRejectsMalformedControlData(t *testing.T) {
	want := replyRoute{address: [4]byte{192, 0, 2, 1}, ifIndex: 5}
	valid := marshalReplyRoute(want)
	if got, ok := parseReplyRoute(valid[:]); !ok || got != want {
		t.Fatalf("packet information round trip: %+v, %v", got, ok)
	}
	for n := 0; n < len(valid); n++ {
		if _, ok := parseReplyRoute(valid[:n]); ok {
			t.Fatalf("accepted truncated message of %d bytes", n)
		}
	}
	for _, route := range []replyRoute{{ifIndex: 1}, {address: want.address}, {address: [4]byte{239, 1, 1, 1}, ifIndex: 1}} {
		bad := marshalReplyRoute(route)
		if _, ok := parseReplyRoute(bad[:]); ok {
			t.Fatalf("accepted unusable route: %+v", route)
		}
	}
	bad := valid
	binary.NativeEndian.PutUint32(bad[:4], 0xffffffff)
	if _, ok := parseReplyRoute(bad[:]); ok {
		t.Fatal("accepted out-of-bounds control message")
	}
	bad = valid
	binary.NativeEndian.PutUint32(bad[packetInfoAlignment+4:], 0x7fffffff)
	if _, ok := parseReplyRoute(bad[:]); ok {
		t.Fatal("accepted unrelated control message")
	}
}
