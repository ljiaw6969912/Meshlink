package agent

import (
	"testing"

	"meshlink/internal/proto"
)

func TestControlClientDoesNotAdvertiseMeshlinkTunnelAsLAN(t *testing.T) {
	f := newRuntimeFixtureWithListen(t, "0.0.0.0:0", "B")
	r := f.r["B"]
	candidates := []proto.Candidate{
		{Address: r.a.cfg.VirtualIP, Port: 4567, Scope: "lan"},
		{Address: "192.168.1.23", Port: 4567, Scope: "lan"},
		{Address: "198.18.0.1", Port: 4567, Scope: "lan"},
		{Address: "203.0.113.10", Port: 4567, Scope: "public"},
	}
	got := r.control.lanCandidates(candidates)
	if len(got) != 1 || got[0].Address != "192.168.1.23" {
		t.Fatalf("advertised candidates = %+v; want only physical LAN address", got)
	}
}
