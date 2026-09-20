package p2p

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTCPDialerConnectsLoopbackLANDirect(t *testing.T) {
	listener, host, port := mustListenTCP4(t)
	defer listener.Close()
	accepted := acceptOneTCP(t, listener)

	negotiation := BuildNegotiation(NegotiationRequest{
		AccountID:          "acct_owner",
		NetworkID:          "net_home",
		SourceDeviceID:     "dev_source",
		TargetDeviceID:     "dev_target",
		SourceCandidates:   []Candidate{lanCandidate("dev_source", host, 49000, 50)},
		TargetCandidates:   []Candidate{lanCandidate("dev_target", host, port, 60)},
		AllowRelayFallback: true,
	})

	result := Connector{Dialer: TCPDialer{Timeout: time.Second}}.Connect(context.Background(), negotiation)

	if result.State != PathStateLANDirectConnected || result.PathType != PathTypeLANDirect {
		t.Fatalf("result = %+v, want LAN direct connected", result)
	}
	if len(result.Attempts) != 1 || result.Attempts[0].Status != AttemptStatusSucceeded {
		t.Fatalf("attempts = %+v, want one successful TCP probe", result.Attempts)
	}
	select {
	case err := <-accepted:
		if err != nil {
			t.Fatalf("listener accept returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("listener did not observe the TCP probe")
	}
}

func TestTCPDialerFallsBackAfterLoopbackListenerCloses(t *testing.T) {
	listener, host, port := mustListenTCP4(t)
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	negotiation := BuildNegotiation(NegotiationRequest{
		AccountID:          "acct_owner",
		NetworkID:          "net_home",
		SourceDeviceID:     "dev_source",
		TargetDeviceID:     "dev_target",
		SourceCandidates:   []Candidate{lanCandidate("dev_source", host, 49001, 50)},
		TargetCandidates:   []Candidate{lanCandidate("dev_target", host, port, 60)},
		AllowRelayFallback: true,
	})

	result := Connector{Dialer: TCPDialer{Timeout: 200 * time.Millisecond}}.Connect(context.Background(), negotiation)

	if result.State != PathStateFallbackRelay || result.PathType != PathTypeRelay {
		t.Fatalf("result = %+v, want Relay fallback after TCP probe failure", result)
	}
	if !result.RelayFallback.CreateRelaySession || result.RelayFallback.Reason == "" {
		t.Fatalf("relay fallback = %+v, want create-session semantics with reason", result.RelayFallback)
	}
	if len(result.Attempts) != 1 || result.Attempts[0].Status != AttemptStatusFailed {
		t.Fatalf("attempts = %+v, want failed TCP probe", result.Attempts)
	}
	assertP2PJSONDoesNotContain(t, result, "join_token", "invite_token", "private_key")
}

func TestTCPDialerFailsWhenRelayFallbackIsDisabled(t *testing.T) {
	listener, host, port := mustListenTCP4(t)
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	negotiation := BuildNegotiation(NegotiationRequest{
		AccountID:          "acct_owner",
		NetworkID:          "net_home",
		SourceDeviceID:     "dev_source",
		TargetDeviceID:     "dev_target",
		SourceCandidates:   []Candidate{lanCandidate("dev_source", host, 49002, 50)},
		TargetCandidates:   []Candidate{lanCandidate("dev_target", host, port, 60)},
		AllowRelayFallback: false,
	})

	result := Connector{Dialer: TCPDialer{Timeout: 200 * time.Millisecond}}.Connect(context.Background(), negotiation)

	if result.State != PathStateFailed {
		t.Fatalf("state = %q, want failed when TCP probe fails and Relay fallback is disabled", result.State)
	}
	if result.PathType != "" || result.RelayFallback.CreateRelaySession {
		t.Fatalf("result = %+v, must not force Relay fallback when disabled", result)
	}
}

func TestTCPDialerRespectsContextCancelAndDeadline(t *testing.T) {
	pair := CandidatePair{
		Source:   lanCandidate("dev_source", "127.0.0.1", 49003, 50),
		Target:   lanCandidate("dev_target", "127.0.0.1", 49004, 60),
		PathType: PathTypeLANDirect,
	}
	dialer := TCPDialer{Timeout: time.Second}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	startedAt := time.Now()
	if err := dialer.Dial(cancelled, pair); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Dial error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 100*time.Millisecond {
		t.Fatalf("cancelled Dial took %s, want prompt cancellation", elapsed)
	}

	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if err := dialer.Dial(expired, pair); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired Dial error = %v, want context.DeadlineExceeded", err)
	}
}

func TestTCPDialerRejectsNonIPv4OrNonTCPPairs(t *testing.T) {
	dialer := TCPDialer{Timeout: time.Second}
	pair := CandidatePair{
		Source:   lanCandidate("dev_source", "127.0.0.1", 49005, 50),
		Target:   lanCandidate("dev_target", "127.0.0.1", 49006, 60),
		PathType: PathTypeLANDirect,
	}
	pair.Target.Protocol = Protocol("udp")
	if err := dialer.Dial(context.Background(), pair); err == nil || !strings.Contains(err.Error(), "tcp") {
		t.Fatalf("non-TCP Dial error = %v, want protocol rejection", err)
	}

	pair.Target.Protocol = ProtocolTCP
	pair.Target.Address = "2001:db8::1"
	if err := dialer.Dial(context.Background(), pair); err == nil || !strings.Contains(err.Error(), "IPv4") {
		t.Fatalf("IPv6 Dial error = %v, want IPv4 rejection", err)
	}
}

func mustListenTCP4(t *testing.T) (net.Listener, string, int) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp4: %v", err)
	}
	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		listener.Close()
		t.Fatalf("split listener address %q: %v", listener.Addr().String(), err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		listener.Close()
		t.Fatalf("parse listener port %q: %v", portText, err)
	}
	return listener, host, port
}

func acceptOneTCP(t *testing.T, listener net.Listener) <-chan error {
	t.Helper()
	accepted := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			accepted <- err
			return
		}
		accepted <- conn.Close()
	}()
	return accepted
}

func assertP2PJSONDoesNotContain(t *testing.T, v any, forbidden ...string) {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal value: %v", err)
	}
	lower := strings.ToLower(string(encoded))
	for _, value := range forbidden {
		if strings.Contains(lower, strings.ToLower(value)) {
			t.Fatalf("JSON leaks forbidden value %q: %s", value, encoded)
		}
	}
}
