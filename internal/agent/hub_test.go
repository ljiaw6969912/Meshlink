package agent

import (
	"io"
	"log/slog"
	"net"
	"net/netip"
	"testing"
)

func testPeer(t *testing.T, id, ip string) (*peer, func()) {
	t.Helper()
	local, remote := net.Pipe()
	p := &peer{
		id:        id,
		virtualIP: netip.MustParseAddr(ip),
		conn:      local,
		log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	cleanup := func() {
		_ = local.Close()
		_ = remote.Close()
	}
	return p, cleanup
}

func TestRouterReplacePeerIgnoresStaleRemove(t *testing.T) {
	rt := newRouter()
	first, cleanupFirst := testPeer(t, "node-a", "10.77.0.2")
	defer cleanupFirst()
	replacement, cleanupReplacement := testPeer(t, "node-a", "10.77.0.2")
	defer cleanupReplacement()

	if old := rt.add(first); old != nil {
		t.Fatalf("first add returned old peer: %v", old)
	}
	if old := rt.add(replacement); old != first {
		t.Fatalf("replacement add returned %v, want first peer", old)
	}
	if removed := rt.removeIf(first.id, first); removed {
		t.Fatal("stale peer removed the active replacement")
	}
	if got := rt.find(netip.MustParseAddr("10.77.0.2")); got != replacement {
		t.Fatalf("route points to %v, want replacement", got)
	}
	if removed := rt.removeIf(replacement.id, replacement); !removed {
		t.Fatal("active replacement was not removed")
	}
	if got := rt.find(netip.MustParseAddr("10.77.0.2")); got != nil {
		t.Fatalf("route still points to %v after active removal", got)
	}
}
