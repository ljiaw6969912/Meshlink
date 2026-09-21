package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"meshlink/internal/certutil"
	"meshlink/internal/config"
	"meshlink/internal/onboarding"
	"meshlink/internal/proto"
)

func serverNodeConfigForTest(t *testing.T) (onboarding.Manager, *config.Config, onboarding.StartServerResult) {
	t.Helper()
	m := onboarding.Manager{BaseDir: t.TempDir(), LocalIPv4: func() (string, error) { return "127.0.0.1", nil }}
	addr := reserveDualProtocolAddress(t)
	_, port, _ := net.SplitHostPort(addr)
	n, _ := strconv.Atoi(port)
	started, err := m.StartServerMode(onboarding.StartServerRequest{ServerAddress: addr, ListenPort: n, LongLived: true, MaxUses: 10})
	if err != nil {
		t.Fatal(err)
	}
	hub, err := config.Load(started.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	childPath := filepath.Join(m.BaseDir, "configs", hub.ServerNodeConfig)
	child, err := config.Load(childPath)
	if err != nil {
		t.Fatal(err)
	}
	child.Setup.Enabled = false
	child.Device.Type = "null"
	if err := config.Write(childPath, *child); err != nil {
		t.Fatal(err)
	}
	resolveAgentTestPaths(hub, started.ConfigPath)
	return m, hub, started
}

func resolveAgentTestPaths(cfg *config.Config, path string) {
	for _, p := range []*string{&cfg.CAFile, &cfg.CertFile, &cfg.KeyFile} {
		if !filepath.IsAbs(*p) {
			*p = filepath.Join(filepath.Dir(path), *p)
		}
	}
}

func TestServerHostSharesCoordinatorPortAndTransfersPeerPackets(t *testing.T) {
	for _, role := range []string{"dialer", "acceptor"} {
		t.Run("server_"+role, func(t *testing.T) {
			testServerHostSharesCoordinatorPortAndTransfersPeerPackets(t, role)
		})
	}
}

func testServerHostSharesCoordinatorPortAndTransfersPeerPackets(t *testing.T, role string) {
	t.Helper()
	m, cfg, started := serverNodeConfigForTest(t)
	dev := &runtimeDevice{incoming: make(chan []byte, 32), written: make(chan []byte, 32)}
	hub, err := New(cfg, discardLogger(), WithBaseDir(m.BaseDir), WithDevice(dev))
	if err != nil {
		t.Fatal(err)
	}
	if hub.serverNode == nil || hub.serverNode.dev != dev {
		t.Fatal("server did not create its packet node")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- hub.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("server did not stop")
		}
	}()
	integrationEventually(t, 5*time.Second, func() (bool, string) {
		return hub.serverNode.status.snapshot().CoordinatorState == "connected", "host control not connected"
	})
	runtime := integrationCurrentPeerRuntime(hub.serverNode)
	_, port, _ := net.SplitHostPort(cfg.Listen)
	if strconv.Itoa(runtime.candidates.LocalAddr().Port) != port {
		t.Fatal("host has an unmapped ephemeral UDP port")
	}
	clientDir := t.TempDir()
	peerName := strings.TrimSuffix(hub.serverNode.cfg.NodeID, "-node") + "-remote"
	if role == "acceptor" {
		peerName = strings.TrimSuffix(hub.serverNode.cfg.NodeID, "-node") + "-client"
	}
	if (hub.serverNode.cfg.NodeID < peerName) != (role == "dialer") {
		t.Fatalf("test identities do not select server role %s", role)
	}
	csr, err := certutil.CreateCSR(certutil.CSROptions{OutDir: clientDir, Name: peerName})
	if err != nil {
		t.Fatal(err)
	}
	enrolled, err := m.HandleEnroll(onboarding.EnrollRequest{Token: started.Invite.Token, Code: started.Invite.Code, NodeName: peerName, CSRPEM: csr.CSRPEM})
	if err != nil {
		t.Fatal(err)
	}
	peerCfg := enrolled.Config
	peerCfg.CAFile = filepath.Join(clientDir, "ca.pem")
	peerCfg.CertFile = filepath.Join(clientDir, peerName+".pem")
	peerCfg.KeyFile = csr.KeyPath
	peerCfg.Setup.Enabled = false
	peerCfg.Device.Type = "null"
	peerCfg.P2P.Listen = "127.0.0.1:0"
	if err := os.WriteFile(peerCfg.CAFile, enrolled.CAPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(peerCfg.CertFile, enrolled.CertPEM, 0600); err != nil {
		t.Fatal(err)
	}
	peerDev := &runtimeDevice{incoming: make(chan []byte, 32), written: make(chan []byte, 32)}
	peer, err := New(&peerCfg, discardLogger(), WithDevice(peerDev))
	if err != nil {
		t.Fatal(err)
	}
	peerDone := make(chan error, 1)
	go func() { peerDone <- peer.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-peerDone:
		case <-time.After(5 * time.Second):
			t.Error("peer did not stop")
		}
	}()
	integrationEventually(t, 5*time.Second, func() (bool, string) {
		return len(peer.status.snapshot().Peers) > 0 && hub.status.snapshot().CoordinatorMetrics.ProbeSuccesses > 0, "peer admission/probe not ready"
	})
	integrationEventually(t, 5*time.Second, func() (bool, string) {
		remote := findAgentPeerStatus(peer.status.snapshot(), hub.serverNode.cfg.NodeID)
		local := findAgentPeerStatus(hub.serverNode.status.snapshot(), peer.cfg.NodeID)
		return remote != nil && local != nil && remote.Status == "online" && local.Status == "online",
			fmt.Sprintf("server/client did not establish direct sessions before traffic: remote=%+v local=%+v", remote, local)
	})
	toHost := append([]byte(nil), literalBToC...)
	toHost[19] = 1
	toPeer := append([]byte(nil), literalCToB...)
	toPeer[15] = 1
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	delivered := false
	for !delivered {
		select {
		case <-tick.C:
			peerDev.incoming <- toHost
		case got := <-dev.written:
			if !bytes.Equal(got, toHost) {
				t.Fatalf("host received wrong packet %x", got)
			}
			delivered = true
		case <-deadline.C:
			t.Fatalf("no peer-to-host delivery; host=%+v peer=%+v", hub.serverNode.status.snapshot(), peer.status.snapshot())
		}
	}
	dev.incoming <- toPeer
	select {
	case got := <-peerDev.written:
		if !bytes.Equal(got, toPeer) {
			t.Fatalf("peer received wrong packet %x", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no host-to-peer delivery")
	}
	status := peer.status.snapshot()
	remote := findAgentPeerStatus(status, hub.serverNode.cfg.NodeID)
	if remote == nil || (remote.PathState != "lan_direct" && remote.PathState != "public_direct") {
		t.Fatalf("missing real direct path: %+v", remote)
	}
	if hub.status.snapshot().CoordinatorMetrics.TypePacketViolations != 0 {
		t.Fatal("host packet entered the coordinator control plane")
	}
	// A live QUIC session must not steal or stop later authenticated probes.
	before := hub.status.snapshot().CoordinatorMetrics.ProbeSuccesses
	peerRuntime := integrationCurrentPeerRuntime(peer)
	if err := peerRuntime.control.refresh(ctx, false); err != nil {
		t.Fatal(err)
	}
	if hub.status.snapshot().CoordinatorMetrics.ProbeSuccesses <= before {
		t.Fatal("QUIC session prevented authenticated rendezvous probes")
	}
}

func TestCoordinatorAdvertisesConfiguredServerMappingOnlyForHostIdentity(t *testing.T) {
	m, cfg, _ := serverNodeConfigForTest(t)
	hub, err := New(cfg, discardLogger(), WithBaseDir(m.BaseDir), WithDevice(&runtimeDevice{}))
	if err != nil {
		t.Fatal(err)
	}
	defer hub.serverNode.closeDevice()
	hub.serverPublicAddress = netip.MustParseAddrPort("203.0.113.8:4443")
	c := newCoordinator(hub)
	host := &coordinatorPeer{node: onboarding.RegisteredNode{NodeID: hub.serverNode.cfg.NodeID}}
	remote := &coordinatorPeer{node: onboarding.RegisteredNode{NodeID: "remote-client"}}
	for _, p := range []*coordinatorPeer{host, remote} {
		if err := c.updateCandidatesLocked(p, proto.CandidateUpdate{Revision: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if len(host.candidates) != 1 || host.candidates[0].Address != "203.0.113.8" || host.candidates[0].Port != 4443 || host.candidates[0].Scope != "public" {
		t.Fatalf("incorrect server mapping: %+v", host.candidates)
	}
	if len(remote.candidates) != 0 {
		t.Fatal("server public mapping leaked into another peer identity")
	}
}

func TestServerHostBindFailureClosesItsDeviceAndLeavesNoRunningStatus(t *testing.T) {
	for _, network := range []string{"tcp4", "udp4"} {
		t.Run(network, func(t *testing.T) {
			m, cfg, _ := serverNodeConfigForTest(t)
			var closeOccupied func() error
			var err error
			if network == "tcp4" {
				var occupied net.Listener
				occupied, err = net.Listen(network, cfg.Listen)
				if err == nil {
					closeOccupied = occupied.Close
				}
			} else {
				var occupied net.PacketConn
				occupied, err = net.ListenPacket(network, cfg.Listen)
				if err == nil {
					closeOccupied = occupied.Close
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			defer closeOccupied()
			dev := &closeOnlyRuntimeDevice{closed: make(chan struct{})}
			hub, err := New(cfg, discardLogger(), WithBaseDir(m.BaseDir), WithDevice(dev))
			if err != nil {
				t.Fatal(err)
			}
			if err := hub.Run(context.Background()); err == nil {
				t.Fatal("occupied TCP listen unexpectedly succeeded")
			}
			select {
			case <-dev.closed:
			default:
				t.Fatal("host device leaked on listener failure")
			}
			if hub.serverNode.status.snapshot().State == "running" {
				t.Fatal("host falsely reports running after bind failure")
			}
			if network == "udp4" {
				tcp, err := net.Listen("tcp4", cfg.Listen)
				if err != nil {
					t.Fatalf("TCP listener leaked after UDP bind failure: %v", err)
				}
				tcp.Close()
			}
		})
	}
}

func TestServerHostRejectsFakeIPPublicEndpoint(t *testing.T) {
	m, cfg, _ := serverNodeConfigForTest(t)
	cfg.ServerPublicEndpoint = "198.19.0.4:9443"
	hub, err := New(cfg, discardLogger(), WithBaseDir(m.BaseDir), WithDevice(&runtimeDevice{}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err = hub.Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "fake-IP") {
		t.Fatalf("server advertised a proxy fake address: %v", err)
	}
}

func TestServerHostStopInterruptsDeviceAndReleasesSharedPort(t *testing.T) {
	m, cfg, _ := serverNodeConfigForTest(t)
	dev := &closeOnlyRuntimeDevice{entered: make(chan struct{}), closed: make(chan struct{})}
	hub, err := New(cfg, discardLogger(), WithBaseDir(m.BaseDir), WithDevice(dev))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- hub.Run(ctx) }()
	select {
	case <-dev.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("server packet device was not started")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server stop blocked on its packet device")
	}
	if dev.closes.Load() != 1 {
		t.Fatalf("server device closed %d times", dev.closes.Load())
	}
	tcp, err := net.Listen("tcp4", cfg.Listen)
	if err != nil {
		t.Fatalf("server TCP listener leaked: %v", err)
	}
	defer tcp.Close()
	udp, err := net.ListenPacket("udp4", cfg.Listen)
	if err != nil {
		t.Fatalf("shared server UDP listener leaked: %v", err)
	}
	defer udp.Close()
}
