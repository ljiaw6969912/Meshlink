package agent

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meshlink/internal/onboarding"
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

func TestHubRejectsDisabledPeerByRegistryAndAudits(t *testing.T) {
	dir := t.TempDir()
	fingerprint := "SHA256:AA:BB:CC"
	registry := onboarding.DeviceRegistry{
		Nodes: []onboarding.RegisteredNode{
			{
				NodeID:          "laptop",
				VirtualIP:       "10.77.0.2",
				CertFingerprint: fingerprint,
				Disabled:        true,
			},
		},
	}
	if err := os.MkdirAll(filepath.Join(dir, "configs"), 0o700); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "configs", "devices.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}

	agent := &Agent{
		baseDir: dir,
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	rejected, reason := agent.rejectPeerByRegistry("laptop", fingerprint, "203.0.113.10:55123")
	if !rejected || !strings.Contains(reason, "device disabled") {
		t.Fatalf("rejectPeerByRegistry rejected=%v reason=%q, want disabled rejection", rejected, reason)
	}

	audit, err := os.ReadFile(filepath.Join(dir, "logs", "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), `"event":"certificate_rejected"`) || !strings.Contains(string(audit), `"reason":"device disabled"`) {
		t.Fatalf("audit log = %s, want certificate_rejected device disabled", string(audit))
	}
}
