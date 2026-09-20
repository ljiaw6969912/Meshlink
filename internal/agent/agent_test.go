package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"meshlink/internal/config"
	"meshlink/internal/device"
	"meshlink/internal/networkstate"
	"meshlink/internal/p2p"
	"meshlink/internal/proto"
)

func TestNewCoordinatorDoesNotOpenConfiguredDevice(t *testing.T) {
	cfg := coordinatorConfigForTest()
	cfg.Device.Type = "must-not-be-opened"
	cfg.Setup.Enabled = true
	got, err := New(&cfg, discardLogger())
	if err != nil || got.dev != nil {
		t.Fatalf("coordinator data device = %v, err = %v", got.dev, err)
	}
}

func TestNewSpokeUsesInjectedDevice(t *testing.T) {
	cfg := config.Config{
		NodeID:    "peer",
		Mode:      "spoke",
		VirtualIP: "10.77.0.2",
		MTU:       1280,
		Device:    config.DeviceConfig{Type: "must-not-be-opened"},
	}
	injected := device.NewNull("injected", 1280)
	got, err := New(&cfg, discardLogger(), WithDevice(injected))
	if err != nil {
		t.Fatal(err)
	}
	if got.dev != injected {
		t.Fatalf("device = %v, want injected device", got.dev)
	}
}

func coordinatorConfigForTest() config.Config {
	return config.Config{
		Version:     config.ConfigVersion,
		NodeID:      "coordinator",
		Mode:        "hub",
		NetworkCIDR: "10.77.0.0/24",
		Listen:      "127.0.0.1:8443",
		CAFile:      "ca.pem",
		CertFile:    "coordinator.pem",
		KeyFile:     "coordinator-key.pem",
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestAgentRunAlwaysPublishesConservativeTerminalStatus(t *testing.T) {
	tests := []struct {
		name   string
		runErr error
	}{
		{name: "canceled", runErr: context.Canceled},
		{name: "fatal", runErr: errors.New("injected fatal run failure")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newStatusStore("", NodeStatus{NodeID: "desk", Mode: "spoke"})
			store.setNetworkState(networkstate.Connected)
			store.applyRoster(proto.Roster{Nodes: []proto.RosterNode{
				{NodeID: "office", Mode: "spoke", Status: PeerStatusOnline},
			}})
			a := &Agent{
				cfg:    &config.Config{Mode: "spoke"},
				log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
				dev:    device.NewNull("test", 1420),
				status: store,
				runMode: func(context.Context) error {
					return tt.runErr
				},
			}

			err := a.Run(context.Background())
			if !errors.Is(err, tt.runErr) {
				t.Fatalf("Run error = %v, want %v", err, tt.runErr)
			}
			status := store.snapshot()
			if status.State != "stopped" || status.NetworkState != networkstate.Disconnected {
				t.Fatalf("terminal status = %+v, want stopped and disconnected", status)
			}
			peer := findAgentPeerStatus(status, "office")
			if peer == nil || peer.Status != PeerStatusOffline || peer.PathState != p2p.PathStateOffline {
				t.Fatalf("terminal peer = %+v, want offline", peer)
			}
		})
	}
}
