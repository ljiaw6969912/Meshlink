package p2p

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"time"
)

const DefaultTCPProbeTimeout = 2 * time.Second

type TCPDialer struct {
	Timeout time.Duration
	Dialer  *net.Dialer
}

func (d TCPDialer) Dial(ctx context.Context, pair CandidatePair) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := tcpProbeEndpoint(pair)
	if err != nil {
		return err
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = DefaultTCPProbeTimeout
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	dialer := net.Dialer{Timeout: timeout}
	if d.Dialer != nil {
		dialer = *d.Dialer
		if dialer.Timeout <= 0 {
			dialer.Timeout = timeout
		}
	}
	conn, err := dialer.DialContext(ctx, "tcp4", target)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("tcp direct probe to %s failed: %w", target, err)
	}
	if err := conn.Close(); err != nil {
		return fmt.Errorf("tcp direct probe to %s close failed: %w", target, err)
	}
	return nil
}

func tcpProbeEndpoint(pair CandidatePair) (string, error) {
	if err := validateTCPCandidate(pair.Source, "source"); err != nil {
		return "", err
	}
	if err := validateTCPCandidate(pair.Target, "target"); err != nil {
		return "", err
	}
	return net.JoinHostPort(pair.Target.Address, strconv.Itoa(pair.Target.Port)), nil
}

func validateTCPCandidate(candidate Candidate, label string) error {
	if candidate.Protocol != "" && candidate.Protocol != ProtocolTCP {
		return fmt.Errorf("%s candidate protocol %q is not supported for tcp direct probe", label, candidate.Protocol)
	}
	addr, err := netip.ParseAddr(candidate.Address)
	if err != nil {
		return fmt.Errorf("%s candidate address must be IPv4 for tcp direct probe: %w", label, err)
	}
	if !addr.Is4() {
		return fmt.Errorf("%s candidate address must be IPv4 for tcp direct probe", label)
	}
	if candidate.Port <= 0 || candidate.Port > 65535 {
		return fmt.Errorf("%s candidate port must be between 1 and 65535 for tcp direct probe", label)
	}
	return nil
}
