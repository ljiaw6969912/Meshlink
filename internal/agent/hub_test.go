package agent

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meshlink/internal/onboarding"
	"meshlink/internal/proto"
)

func TestCoordinatorRunWithoutDevice(t *testing.T) {
	cfg := coordinatorConfigForTest()
	a, err := New(&cfg, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	a.runMode = func(context.Context) error { return context.Canceled }
	defer func() {
		if v := recover(); v != nil {
			t.Errorf("hub Run dereferenced missing device: %v", v)
		}
	}()
	if err := a.Run(context.Background()); err != context.Canceled {
		t.Fatalf("Run = %v", err)
	}
	if a.status.snapshot().State != "stopped" {
		t.Fatal("hub terminal status not written")
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
	f := newCoordinatorFixture(t)
	if _, err := (onboarding.Manager{BaseDir: f.dir}).DisableDevice("B"); err != nil {
		t.Fatal(err)
	}
	p := f.dial("B")
	p.send(proto.ControlTypeClientHello, f.hello("B"))
	p.want(proto.ControlTypeError)
	p.closed()
	audit, err := os.ReadFile(filepath.Join(f.dir, "logs", "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), `"event":"certificate_rejected"`) || !strings.Contains(string(audit), `"reason":"device disabled"`) {
		t.Fatalf("audit log = %s, want certificate_rejected device disabled", string(audit))
	}
}
