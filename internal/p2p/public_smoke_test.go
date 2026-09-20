package p2p

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTCPDialerPublicDirectSmokeFromEnv(t *testing.T) {
	addr := strings.TrimSpace(os.Getenv("MESHLINK_P2P_PUBLIC_SMOKE_ADDR"))
	if addr == "" {
		t.Skip("set MESHLINK_P2P_PUBLIC_SMOKE_ADDR to run public direct TCP smoke")
	}
	host, port := mustSplitSmokeAddr(t, "MESHLINK_P2P_PUBLIC_SMOKE_ADDR", addr)
	negotiation := publicSmokeNegotiation(host, port)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := Connector{Dialer: TCPDialer{Timeout: 5 * time.Second}}.Connect(ctx, negotiation)
	if result.State != PathStatePublicDirectConnected || result.PathType != PathTypePublicDirect {
		t.Fatalf("result = %+v, want public direct connected to %s", result, addr)
	}
	assertP2PJSONDoesNotContain(t, result, "join_token", "invite_token", "invite_code", "private_key")

	fallbackAddr := strings.TrimSpace(os.Getenv("MESHLINK_P2P_PUBLIC_SMOKE_FALLBACK_ADDR"))
	if fallbackAddr == "" {
		fallbackAddr = addr
		time.Sleep(200 * time.Millisecond)
	}
	fallbackHost, fallbackPort := mustSplitSmokeAddr(t, "MESHLINK_P2P_PUBLIC_SMOKE_FALLBACK_ADDR", fallbackAddr)
	fallbackNegotiation := publicSmokeNegotiation(fallbackHost, fallbackPort)
	fallbackResult := Connector{Dialer: TCPDialer{Timeout: 500 * time.Millisecond}}.Connect(context.Background(), fallbackNegotiation)
	if fallbackResult.State != PathStateFallbackRelay || fallbackResult.PathType != PathTypeRelay {
		t.Fatalf("fallback result = %+v, want Relay fallback for unavailable public address %s", fallbackResult, fallbackAddr)
	}
	if !fallbackResult.RelayFallback.CreateRelaySession {
		t.Fatalf("relay fallback = %+v, want create-session semantics without join token", fallbackResult.RelayFallback)
	}
	assertP2PJSONDoesNotContain(t, fallbackResult, "join_token", "invite_token", "invite_code", "private_key")
}

func publicSmokeNegotiation(host string, port int) Negotiation {
	return BuildNegotiation(NegotiationRequest{
		AccountID:          "acct_public_smoke",
		NetworkID:          "net_home",
		SourceDeviceID:     "dev_source",
		TargetDeviceID:     "dev_target",
		SourceCandidates:   []Candidate{publicCandidate("dev_source", "198.51.100.200", 49110, 50)},
		TargetCandidates:   []Candidate{publicCandidate("dev_target", host, port, 80)},
		AllowRelayFallback: true,
	})
}

func mustSplitSmokeAddr(t *testing.T, envName string, addr string) (string, int) {
	t.Helper()
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %s: %v", envName, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse %s port %q: %v", envName, portText, err)
	}
	return host, port
}
