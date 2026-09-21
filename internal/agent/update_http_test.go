package agent

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"meshlink/internal/config"
	"meshlink/internal/tlsutil"
)

func TestCoordinatorServesUpdatesOnExistingAuthenticatedTLSPort(t *testing.T) {
	m, cfg, _ := serverNodeConfigForTest(t)
	hub, err := New(cfg, discardLogger(), WithBaseDir(m.BaseDir), WithDevice(&runtimeDevice{incoming: make(chan []byte, 8), written: make(chan []byte, 8)}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- hub.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("server did not stop")
		}
	}()
	integrationEventually(t, 5*time.Second, func() (bool, string) {
		return hub.serverNode.status.snapshot().CoordinatorState == "connected", "server not ready"
	})
	childPath := filepath.Join(m.BaseDir, "configs", cfg.ServerNodeConfig)
	child, err := config.Load(childPath)
	if err != nil {
		t.Fatal(err)
	}
	resolveAgentTestPaths(child, childPath)
	secure, err := tlsutil.ClientConfig(child.CAFile, child.CertFile, child.KeyFile, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{TLSClientConfig: secure, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	for _, tc := range []struct {
		path   string
		status int
	}{{"/updates/healthz", 200}, {"/updates/meshlink/configs/active.json", 404}} {
		response, err := client.Get("https://" + cfg.Listen + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != tc.status {
			t.Fatalf("%s status=%d", tc.path, response.StatusCode)
		}
	}
	unauthorized := secure.Clone()
	unauthorized.Certificates = nil
	anonymousTransport := &http.Transport{TLSClientConfig: unauthorized, DisableKeepAlives: true}
	defer anonymousTransport.CloseIdleConnections()
	anonymous := &http.Client{Transport: anonymousTransport, Timeout: 3 * time.Second}
	response, err := anonymous.Get("https://" + cfg.Listen + "/updates/healthz")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated update request accepted: %d", response.StatusCode)
	}
}
