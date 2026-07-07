package agent

import (
	"bufio"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
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

func TestNextReconnectDelayCapsAtMax(t *testing.T) {
	if got := nextReconnectDelay(0); got != spokeReconnectMinDelay {
		t.Fatalf("nextReconnectDelay(0) = %s, want %s", got, spokeReconnectMinDelay)
	}
	if got := nextReconnectDelay(time.Second); got != 2*time.Second {
		t.Fatalf("nextReconnectDelay(1s) = %s, want 2s", got)
	}
	if got := nextReconnectDelay(spokeReconnectMaxDelay); got != spokeReconnectMaxDelay {
		t.Fatalf("nextReconnectDelay(max) = %s, want %s", got, spokeReconnectMaxDelay)
	}
}

func TestServeEnrollHTTPKeepsSingleConnectionOpenForRequest(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	agent := &Agent{
		baseDir: t.TempDir(),
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		agent.serveEnrollHTTP(serverConn)
	}()

	time.Sleep(20 * time.Millisecond)
	if err := clientConn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(clientConn, "GET /enroll/health HTTP/1.1\r\nHost: meshlink\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}

	status, err := bufio.NewReader(clientConn).ReadString('\n')
	if err != nil {
		t.Fatalf("read response status: %v", err)
	}
	if !strings.Contains(status, "200 OK") {
		t.Fatalf("status = %q, want 200 OK", status)
	}
	<-done
}
