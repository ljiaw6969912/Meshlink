package p2p

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

const DefaultUDPProbeTimeout = 2 * time.Second

type TransportKind string

const (
	TransportKindTCPTLSV1      TransportKind = "tcp_tls_v1"
	TransportKindUDPDatagramV1 TransportKind = "udp_datagram_v1"
	TransportKindQUICV1        TransportKind = "quic_v1"
	TransportKindRelay         TransportKind = "relay"
)

type TransportPolicy struct {
	AllowExperimentalUDP  bool `json:"allow_experimental_udp,omitempty"`
	AllowExperimentalQUIC bool `json:"allow_experimental_quic,omitempty"`
}

type TransportDecision struct {
	Kind         TransportKind `json:"kind"`
	PathType     PathType      `json:"path_type,omitempty"`
	Experimental bool          `json:"experimental,omitempty"`
	Reason       string        `json:"reason,omitempty"`
}

type TransportSelection struct {
	Preferred  TransportDecision   `json:"preferred"`
	Candidates []TransportDecision `json:"candidates,omitempty"`
	Excluded   []TransportDecision `json:"excluded,omitempty"`
}

type TransportSelectionRequest struct {
	Pairs                []CandidatePair
	SourceNATProbe       *NATProbeSummary
	TargetNATProbe       *NATProbeSummary
	Policy               TransportPolicy
	AllowRelayFallback   bool
	RelayRequiredReason  string
	NoDirectCandidates   bool
	RelayFallbackAllowed bool
}

func SelectTransport(req TransportSelectionRequest) TransportSelection {
	pairs := append([]CandidatePair(nil), req.Pairs...)
	relayAllowed := req.AllowRelayFallback || req.RelayFallbackAllowed
	relayReason := strings.TrimSpace(req.RelayRequiredReason)
	if relayReason == "" {
		relayReason = natRelayFallbackReason(req.SourceNATProbe, req.TargetNATProbe)
	}
	selection := TransportSelection{}
	if relayReason != "" {
		selection.Excluded = appendExperimentalExclusions(selection.Excluded, req.Policy, "NAT probe recommends Relay fallback")
		if relayAllowed {
			selection.Preferred = TransportDecision{Kind: TransportKindRelay, PathType: PathTypeRelay, Reason: "Relay fallback required by NAT probe"}
			selection.Candidates = append(selection.Candidates, selection.Preferred)
			return selection
		}
		selection.Preferred = TransportDecision{Kind: TransportKindTCPTLSV1, Reason: "Relay fallback required but Relay is disabled"}
		return selection
	}
	if len(pairs) == 0 || req.NoDirectCandidates {
		selection.Excluded = appendExperimentalExclusions(selection.Excluded, req.Policy, "no direct candidate pairs")
		if relayAllowed {
			selection.Preferred = TransportDecision{Kind: TransportKindRelay, PathType: PathTypeRelay, Reason: "no direct candidate pairs; use Relay fallback"}
			selection.Candidates = append(selection.Candidates, selection.Preferred)
			return selection
		}
		selection.Preferred = TransportDecision{Kind: TransportKindTCPTLSV1, Reason: "no direct candidate pairs and Relay fallback is disabled"}
		return selection
	}

	pathType := pairs[0].PathType
	if datagramTransportsAllowed(req.SourceNATProbe, req.TargetNATProbe) {
		if req.Policy.AllowExperimentalQUIC {
			selection.Candidates = append(selection.Candidates, TransportDecision{
				Kind:         TransportKindQUICV1,
				PathType:     pathType,
				Experimental: true,
				Reason:       "experimental QUIC candidate allowed by policy and NAT probe",
			})
		}
		if req.Policy.AllowExperimentalUDP {
			selection.Candidates = append(selection.Candidates, TransportDecision{
				Kind:         TransportKindUDPDatagramV1,
				PathType:     pathType,
				Experimental: true,
				Reason:       "experimental UDP datagram candidate allowed by policy and NAT probe",
			})
		}
	} else {
		selection.Excluded = appendExperimentalExclusions(selection.Excluded, req.Policy, "NAT probe does not allow UDP-based transport")
	}
	tcp := TransportDecision{
		Kind:     TransportKindTCPTLSV1,
		PathType: pathType,
		Reason:   "default audited TCP/TLS direct transport",
	}
	selection.Candidates = append(selection.Candidates, tcp)
	selection.Preferred = selection.Candidates[0]
	return selection
}

func appendExperimentalExclusions(out []TransportDecision, policy TransportPolicy, reason string) []TransportDecision {
	if policy.AllowExperimentalQUIC {
		out = append(out, TransportDecision{
			Kind:         TransportKindQUICV1,
			Experimental: true,
			Reason:       reason,
		})
	}
	if policy.AllowExperimentalUDP {
		out = append(out, TransportDecision{
			Kind:         TransportKindUDPDatagramV1,
			Experimental: true,
			Reason:       reason,
		})
	}
	return out
}

func datagramTransportsAllowed(source, target *NATProbeSummary) bool {
	if source == nil || target == nil {
		return false
	}
	for _, summary := range []*NATProbeSummary{source, target} {
		normalized := NormalizeNATProbeSummary(*summary)
		if normalized.RelayRecommended || !normalized.UDPAvailable || normalized.Type == NATTypeSymmetric ||
			normalized.Type == NATTypeUDPBlocked || normalized.Type == NATTypeProbeFailed {
			return false
		}
	}
	return true
}

type DatagramProbeRequest struct {
	Endpoint string
	Payload  []byte
}

type DatagramProbeResult struct {
	Transport     TransportKind `json:"transport"`
	BytesSent     int           `json:"bytes_sent"`
	BytesReceived int           `json:"bytes_received"`
	AuditReason   string        `json:"audit_reason"`
}

type UDPDatagramProber struct {
	Timeout time.Duration
	Dialer  *net.Dialer
}

func (p UDPDatagramProber) Probe(ctx context.Context, req DatagramProbeRequest) (DatagramProbeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return DatagramProbeResult{}, err
	}
	endpoint := strings.TrimSpace(req.Endpoint)
	if endpoint == "" {
		return DatagramProbeResult{}, fmt.Errorf("udp endpoint is required")
	}
	if _, err := net.ResolveUDPAddr("udp4", endpoint); err != nil {
		return DatagramProbeResult{}, fmt.Errorf("udp endpoint must be IPv4 host:port: %w", err)
	}
	payload := append([]byte(nil), req.Payload...)
	if len(payload) == 0 {
		payload = []byte("meshlink-udp-probe")
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = DefaultUDPProbeTimeout
	}
	dialer := net.Dialer{Timeout: timeout}
	if p.Dialer != nil {
		dialer = *p.Dialer
		if dialer.Timeout <= 0 {
			dialer.Timeout = timeout
		}
	}
	conn, err := dialer.DialContext(ctx, "udp4", endpoint)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return DatagramProbeResult{}, ctxErr
		}
		return DatagramProbeResult{}, fmt.Errorf("udp datagram probe failed")
	}
	defer conn.Close()

	deadline := time.Now().Add(timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return DatagramProbeResult{}, fmt.Errorf("udp datagram deadline failed")
	}
	n, err := conn.Write(payload)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return DatagramProbeResult{}, ctxErr
		}
		return DatagramProbeResult{}, fmt.Errorf("udp datagram write failed")
	}
	buf := make([]byte, len(payload)+64)
	readN, err := conn.Read(buf)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return DatagramProbeResult{}, ctxErr
		}
		return DatagramProbeResult{}, fmt.Errorf("udp datagram response failed")
	}
	return DatagramProbeResult{
		Transport:     TransportKindUDPDatagramV1,
		BytesSent:     n,
		BytesReceived: readN,
		AuditReason:   "udp datagram loopback probe completed",
	}, nil
}
