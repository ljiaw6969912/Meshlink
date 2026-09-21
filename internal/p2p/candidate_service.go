package p2p

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"

	"meshlink/internal/proto"
)

const (
	CandidateTTL       = 120 * time.Second
	maxPunchCandidates = 16
	lanPriority        = 100
	publicPriority     = 50
)

var (
	ErrCandidateServiceClosed = errors.New("candidate service is closed")
	ErrCandidateServiceInUse  = errors.New("candidate service is owned by a session manager")
)

type CandidateSnapshot struct {
	Revision   uint64
	Candidates []proto.Candidate
	ObservedAt time.Time
}

type CandidateServiceConfig struct {
	NodeID, NetworkID, Listen string
	TLSConfig                 *tls.Config
	QUICConfig                *quic.Config
	EnumerateIPv4             func() ([]netip.Addr, error)
	Now                       func() time.Time
}

type CandidateService struct {
	nodeID         string
	networkID      string
	conn           *net.UDPConn
	transport      *quic.Transport
	listener       *quic.Listener
	tlsConfig      *tls.Config
	quicConfig     *quic.Config
	enumerate      func() ([]netip.Addr, error)
	now            func() time.Time
	readMu         sync.Mutex
	stateMu        sync.Mutex
	managerMu      sync.Mutex
	revision       uint64
	observed       map[string]proto.Candidate
	closed         atomic.Bool
	managerClaimed bool
	closeOnce      sync.Once
	closeErr       error
}

func NewCandidateService(cfg CandidateServiceConfig) (*CandidateService, error) {
	nodeID := strings.TrimSpace(cfg.NodeID)
	networkID := strings.TrimSpace(cfg.NetworkID)
	if nodeID == "" || networkID == "" {
		return nil, errors.New("candidate service node ID and network ID are required")
	}
	if cfg.TLSConfig == nil {
		return nil, errors.New("candidate service TLS config is required")
	}
	listen := strings.TrimSpace(cfg.Listen)
	if listen == "" {
		listen = "0.0.0.0:0"
	}
	listenAddr, err := net.ResolveUDPAddr("udp4", listen)
	if err != nil {
		return nil, fmt.Errorf("resolve candidate listen address: %w", err)
	}
	conn, err := net.ListenUDP("udp4", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("listen for candidates: %w", err)
	}

	packetConn, err := candidatePacketConn(conn)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("configure candidate socket: %w", err)
	}
	transport := &quic.Transport{Conn: packetConn}
	tlsConfig := cfg.TLSConfig.Clone()
	quicConfig := normalizedSessionQUICConfig(cfg.QUICConfig)
	listener, err := transport.Listen(tlsConfig.Clone(), quicConfig.Clone())
	if err != nil {
		_ = transport.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("listen for QUIC: %w", err)
	}

	primeContext, cancelPrime := context.WithCancel(context.Background())
	cancelPrime()
	_, _, primeErr := transport.ReadNonQUICPacket(primeContext, make([]byte, 1))
	if primeErr = normalizeNonQUICPrimeError(primeErr); primeErr != nil {
		_ = listener.Close()
		_ = transport.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("initialize non-QUIC packet routing: %w", primeErr)
	}

	enumerate := cfg.EnumerateIPv4
	if enumerate == nil {
		enumerate = enumerateActiveIPv4
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &CandidateService{
		nodeID:     nodeID,
		networkID:  networkID,
		conn:       conn,
		transport:  transport,
		listener:   listener,
		tlsConfig:  tlsConfig,
		quicConfig: quicConfig,
		enumerate:  enumerate,
		now:        now,
		observed:   make(map[string]proto.Candidate),
	}, nil
}

func (s *CandidateService) Refresh(ctx context.Context, rendezvousAddr string, credential proto.ProbeCredential) (CandidateSnapshot, error) {
	if err := s.checkOpen(); err != nil {
		return CandidateSnapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return CandidateSnapshot{}, err
	}
	lanAddresses, err := s.localIPv4()
	if err != nil {
		return CandidateSnapshot{}, fmt.Errorf("enumerate LAN candidates: %w", err)
	}
	if strings.TrimSpace(rendezvousAddr) == "" {
		if err := s.checkOpen(); err != nil {
			return CandidateSnapshot{}, err
		}
		return s.makeSnapshot(lanAddresses, nil), nil
	}

	destination, err := net.ResolveUDPAddr("udp4", rendezvousAddr)
	if err != nil {
		return CandidateSnapshot{}, fmt.Errorf("resolve rendezvous address: %w", err)
	}
	expectedSource, err := addrPortFromNetAddr(destination)
	if err != nil {
		return CandidateSnapshot{}, fmt.Errorf("rendezvous address: %w", err)
	}
	nonce := make([]byte, ProbeNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return CandidateSnapshot{}, fmt.Errorf("generate probe nonce: %w", err)
	}
	sentAt := s.clock()
	request, err := EncodeProbeRequest(credential, sentAt, nonce)
	if err != nil {
		return CandidateSnapshot{}, err
	}

	s.readMu.Lock()
	defer s.readMu.Unlock()
	if err := s.checkOpen(); err != nil {
		return CandidateSnapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return CandidateSnapshot{}, err
	}
	written, err := s.transport.WriteTo(request, destination)
	if err != nil {
		return CandidateSnapshot{}, s.normalizeOperationError(fmt.Errorf("send rendezvous probe: %w", err))
	}
	if written != len(request) {
		return CandidateSnapshot{}, ioShortWrite("rendezvous probe", written, len(request))
	}

	buffer := make([]byte, maxRendezvousPacketLen)
	for {
		n, source, err := s.transport.ReadNonQUICPacket(ctx, buffer)
		if err != nil {
			return CandidateSnapshot{}, s.normalizeOperationError(fmt.Errorf("read rendezvous response: %w", err))
		}
		actualSource, err := addrPortFromNetAddr(source)
		if err != nil || actualSource != expectedSource {
			continue
		}
		response, err := DecodeProbeResponse(buffer[:n], credential, s.clock())
		if err != nil {
			if errors.Is(err, ErrProbeExpired) {
				return CandidateSnapshot{}, err
			}
			continue
		}
		if response.ProbeID != credential.ProbeID || response.UnixMillis != sentAt.UnixMilli() || !bytes.Equal(response.Nonce, nonce) {
			continue
		}
		if err := s.checkOpen(); err != nil {
			return CandidateSnapshot{}, err
		}
		return s.makeSnapshot(lanAddresses, &response.ObservedAddress), nil
	}
}

func (s *CandidateService) Punch(ctx context.Context, sessionID string, generation uint64, key string, candidates []proto.Candidate) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	keyBytes, err := decodeSecret(key)
	if err != nil {
		return fmt.Errorf("invalid pairing key: %w", err)
	}
	defer zeroBytes(keyBytes)
	return s.punchWithKey(ctx, sessionID, generation, keyBytes, candidates)
}

// punchWithKey keeps one-time session authorization material mutable so its
// caller can erase every temporary copy after the synchronous write completes.
func (s *CandidateService) punchWithKey(ctx context.Context, sessionID string, generation uint64, key []byte, candidates []proto.Candidate) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(key) != sharedSecretSize {
		return errors.New("pairing key must contain exactly 32 bytes")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || len(sessionID) > maxRendezvousStringLen || generation == 0 {
		return fmt.Errorf("%w: invalid session or generation", ErrPunchMalformed)
	}
	now := s.clock()
	destinations := make(map[string]*net.UDPAddr)
	for _, candidate := range candidates {
		if !candidate.ExpiresAt.IsZero() && !now.Before(candidate.ExpiresAt.UTC()) {
			continue
		}
		if candidate.Scope != "lan" && candidate.Scope != "public" {
			continue
		}
		address, err := netip.ParseAddr(strings.TrimSpace(candidate.Address))
		if err != nil || !address.Unmap().Is4() || candidate.Port == 0 {
			return fmt.Errorf("invalid punch candidate %q:%d", candidate.Address, candidate.Port)
		}
		address = address.Unmap()
		key := netip.AddrPortFrom(address, candidate.Port).String()
		destinations[key] = net.UDPAddrFromAddrPort(netip.AddrPortFrom(address, candidate.Port))
	}
	if len(destinations) > maxPunchCandidates {
		return fmt.Errorf("too many punch candidates: %d exceeds %d", len(destinations), maxPunchCandidates)
	}
	ordered := make([]string, 0, len(destinations))
	for destination := range destinations {
		ordered = append(ordered, destination)
	}
	sort.Strings(ordered)
	for _, destination := range ordered {
		if err := s.checkOpen(); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		nonce := make([]byte, PunchNonceSize)
		if _, err := rand.Read(nonce); err != nil {
			return fmt.Errorf("generate punch nonce: %w", err)
		}
		packet, err := encodePunchPacketWithKey(sessionID, generation, key, nonce)
		if err != nil {
			return err
		}
		written, err := s.transport.WriteTo(packet, destinations[destination])
		if err != nil {
			return s.normalizeOperationError(fmt.Errorf("send punch to %s: %w", destination, err))
		}
		if written != len(packet) {
			return ioShortWrite("punch packet", written, len(packet))
		}
	}
	return nil
}

func encodePunchPacketWithKey(sessionID string, generation uint64, key, nonce []byte) ([]byte, error) {
	if len(key) != sharedSecretSize || len(nonce) != PunchNonceSize {
		return nil, fmt.Errorf("%w: invalid pairing key or nonce", ErrPunchAuthentication)
	}
	punch := punchWirePacket{
		Kind:       punchKind,
		Version:    rendezvousVersion,
		SessionID:  sessionID,
		Generation: generation,
		Nonce:      hex.EncodeToString(nonce),
	}
	punch.HMAC = encodeMAC(punchMAC(key, punch.Kind, punch.Version, punch.SessionID, punch.Generation, nonce))
	return marshalNonQUICPacket(punch)
}

func (s *CandidateService) Transport() *quic.Transport {
	if s == nil {
		return nil
	}
	return s.transport
}

func (s *CandidateService) Listener() *quic.Listener {
	if s == nil {
		return nil
	}
	return s.listener
}

func (s *CandidateService) LocalAddr() *net.UDPAddr {
	if s == nil || s.conn == nil {
		return nil
	}
	address, _ := s.conn.LocalAddr().(*net.UDPAddr)
	if address == nil {
		return nil
	}
	return &net.UDPAddr{IP: append(net.IP(nil), address.IP...), Port: address.Port, Zone: address.Zone}
}

func (s *CandidateService) Close() error {
	if s == nil {
		return nil
	}
	s.managerMu.Lock()
	if s.managerClaimed {
		s.managerMu.Unlock()
		return ErrCandidateServiceInUse
	}
	s.closed.Store(true)
	s.managerMu.Unlock()
	s.closeOnce.Do(func() {
		var closeErrors []error
		if s.listener != nil {
			closeErrors = append(closeErrors, s.listener.Close())
		}
		if s.transport != nil {
			closeErrors = append(closeErrors, s.transport.Close())
		}
		if s.conn != nil {
			closeErrors = append(closeErrors, s.conn.Close())
		}
		s.closeErr = errors.Join(closeErrors...)
	})
	return s.closeErr
}

func (s *CandidateService) claimSessionManager() error {
	if s == nil {
		return ErrCandidateServiceClosed
	}
	s.managerMu.Lock()
	defer s.managerMu.Unlock()
	if s.closed.Load() {
		return ErrCandidateServiceClosed
	}
	if s.managerClaimed {
		return ErrCandidateServiceInUse
	}
	s.managerClaimed = true
	return nil
}

func (s *CandidateService) releaseSessionManager() {
	if s == nil {
		return
	}
	s.managerMu.Lock()
	s.managerClaimed = false
	s.managerMu.Unlock()
}

func (s *CandidateService) clock() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

func (s *CandidateService) checkOpen() error {
	if s == nil || s.transport == nil || s.closed.Load() {
		return ErrCandidateServiceClosed
	}
	return nil
}

func (s *CandidateService) normalizeOperationError(err error) error {
	if s == nil || s.closed.Load() {
		return fmt.Errorf("%w: %v", ErrCandidateServiceClosed, err)
	}
	return err
}

func (s *CandidateService) localIPv4() ([]netip.Addr, error) {
	addresses, err := s.enumerate()
	if err != nil {
		return nil, err
	}
	unique := make(map[netip.Addr]struct{}, len(addresses)+1)
	for _, address := range addresses {
		address = address.Unmap()
		if !address.Is4() || address.IsUnspecified() || address.IsLoopback() {
			continue
		}
		unique[address] = struct{}{}
	}
	if local := s.LocalAddr(); local != nil {
		bound := local.AddrPort().Addr().Unmap()
		if bound.Is4() && !bound.IsUnspecified() {
			unique[bound] = struct{}{}
		}
	}
	result := make([]netip.Addr, 0, len(unique))
	for address := range unique {
		result = append(result, address)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Less(result[j]) })
	return result, nil
}

func (s *CandidateService) makeSnapshot(lanAddresses []netip.Addr, observed *netip.AddrPort) CandidateSnapshot {
	now := s.clock()
	expiresAt := now.Add(CandidateTTL)
	port := uint16(s.LocalAddr().Port)

	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.observed == nil {
		s.observed = make(map[string]proto.Candidate)
	}
	for key, candidate := range s.observed {
		if !now.Before(candidate.ExpiresAt.UTC()) {
			delete(s.observed, key)
		}
	}
	if observed != nil {
		candidate := proto.Candidate{
			Address:   observed.Addr().Unmap().String(),
			Port:      observed.Port(),
			Scope:     "public",
			Priority:  publicPriority,
			ExpiresAt: expiresAt,
		}
		s.observed[candidateMapKey(candidate)] = candidate
	}

	byKey := make(map[string]proto.Candidate, len(lanAddresses)+len(s.observed))
	for _, address := range lanAddresses {
		candidate := proto.Candidate{
			Address:   address.String(),
			Port:      port,
			Scope:     "lan",
			Priority:  lanPriority,
			ExpiresAt: expiresAt,
		}
		byKey[candidateMapKey(candidate)] = candidate
	}
	for key, candidate := range s.observed {
		byKey[key] = candidate
	}
	candidates := make([]proto.Candidate, 0, len(byKey))
	for _, candidate := range byKey {
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Scope != candidates[j].Scope {
			return candidates[i].Scope < candidates[j].Scope
		}
		if candidates[i].Priority != candidates[j].Priority {
			return candidates[i].Priority > candidates[j].Priority
		}
		if candidates[i].Address != candidates[j].Address {
			return candidates[i].Address < candidates[j].Address
		}
		return candidates[i].Port < candidates[j].Port
	})
	s.revision++
	return CandidateSnapshot{Revision: s.revision, Candidates: candidates, ObservedAt: now}
}

func enumerateActiveIPv4() ([]netip.Addr, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var result []netip.Addr
	for _, networkInterface := range interfaces {
		if networkInterface.Flags&net.FlagUp == 0 || networkInterface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := networkInterface.Addrs()
		if err != nil {
			return nil, fmt.Errorf("interface %s: %w", networkInterface.Name, err)
		}
		for _, raw := range addresses {
			prefix, err := netip.ParsePrefix(raw.String())
			if err != nil {
				continue
			}
			address := prefix.Addr().Unmap()
			if address.Is4() && !address.IsUnspecified() && !address.IsLoopback() {
				result = append(result, address)
			}
		}
	}
	return result, nil
}

func candidateMapKey(candidate proto.Candidate) string {
	return candidate.Scope + "\x00" + candidate.Address + "\x00" + strconv.Itoa(int(candidate.Port))
}

func ioShortWrite(operation string, wrote, expected int) error {
	return fmt.Errorf("%s: short write: wrote %d of %d bytes", operation, wrote, expected)
}

func normalizeNonQUICPrimeError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
