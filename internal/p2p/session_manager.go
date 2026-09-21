package p2p

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"

	"meshlink/internal/proto"
)

const (
	defaultSessionHeartbeatInterval = 10 * time.Second
	defaultSessionHeartbeatTimeout  = 35 * time.Second
	defaultSessionDialTimeout       = 8 * time.Second
	maxLANFallbackDialTime          = 500 * time.Millisecond
	defaultSessionKeepAlive         = 15 * time.Second
	defaultSessionMaxIdle           = 45 * time.Second
	pendingPacketLimit              = 64
	pendingByteLimit                = 256 * 1024
	pendingPacketLifetime           = 3 * time.Second
	readySendQueueLimit             = 256
	globalPendingPacketLimit        = 1024
	globalPendingByteLimit          = 8 * 1024 * 1024
	reassemblySweepInterval         = 250 * time.Millisecond
	requestBackoffInitial           = time.Second
	requestBackoffMaximum           = 30 * time.Second
	sessionApplicationError         = quic.ApplicationErrorCode(0x4d50)
)

var (
	ErrSessionManagerClosed = errors.New("session manager is closed")
	ErrSessionOfferRejected = errors.New("session offer rejected")
	ErrSessionNotReady      = errors.New("session is not ready")
)

type SessionManagerConfig struct {
	NodeID, NetworkID string
	MTU               int
	Candidates        *CandidateService
	TLSConfig         *tls.Config
	QUICConfig        *quic.Config
	LocalVirtualIP    netip.Addr
	LocalRoutes       []netip.Prefix
	PeerMember        func(string) (proto.Member, bool)
	RequestSession    func(string)
	DeliverPacket     func(string, []byte) error
	// Preferred by owners that must reject delayed delivery after revocation.
	DeliverPacketContext func(context.Context, string, []byte) error
	SessionChanged       func(SessionSnapshot)
	HeartbeatInterval    time.Duration
	HeartbeatTimeout     time.Duration
	DialTimeout          time.Duration
	Now                  func() time.Time
	Logger               *slog.Logger
}

type SessionManager struct {
	nodeID               string
	networkID            string
	mtu                  int
	candidates           *CandidateService
	tlsConfig            *tls.Config
	quicConfig           *quic.Config
	localVirtualIP       netip.Addr
	localRoutes          []netip.Prefix
	peerMember           func(string) (proto.Member, bool)
	requestSession       func(string)
	deliverPacket        func(string, []byte) error
	deliverPacketContext func(context.Context, string, []byte) error
	sessionChanged       func(SessionSnapshot)
	heartbeatInterval    time.Duration
	heartbeatTimeout     time.Duration
	dialTimeout          time.Duration
	now                  func() time.Time
	logger               *slog.Logger
	reassembler          *Reassembler
	ctx                  context.Context
	cancel               context.CancelFunc
	mu                   sync.Mutex
	pairs                map[string]*sessionPair
	coordinatorAvailable bool
	pendingSequence      uint64
	closed               bool
	closeOnce            sync.Once
	wg                   sync.WaitGroup
}

type sessionPair struct {
	peerID            string
	highestGeneration uint64
	attempt           *sessionAttempt
	active            *managedSession
	snapshot          SessionSnapshot
	pending           []pendingPacket
	pendingBytes      int
	pendingDropped    uint64
	requestInFlight   bool
	requestAttempts   uint
	requestEpoch      uint64
	retryTimer        *time.Timer
	retryAt           time.Time
	retryStop         chan struct{}
	closed            bool
}

type pendingPacket struct {
	packet   []byte
	queuedAt time.Time
	sequence uint64
}

type sessionAttempt struct {
	offer     proto.SessionOffer
	keyMu     sync.Mutex
	key       []byte
	started   bool
	startedCh chan struct{}
	done      chan struct{}
	punchDone chan struct{}
	punchOnce sync.Once
	punchErr  error
	claimed   bool
	consumed  bool
	ctx       context.Context
	cancel    context.CancelFunc
}

type managedSession struct {
	manager        *SessionManager
	peerID         string
	sessionID      string
	generation     uint64
	pathType       PathType
	conn           *quic.Conn
	stream         *quic.Stream
	wireWriter     *sessionWireWriter
	ctx            context.Context
	cancel         context.CancelFunc
	sendQueue      chan []byte
	packetID       atomic.Uint64
	bytesSent      atomic.Uint64
	bytesReceived  atomic.Uint64
	sendDropped    atomic.Uint64
	inboundDropped atomic.Uint64
	lastHeartbeat  atomic.Int64
	rtt            atomic.Int64
	heartbeatMu    sync.Mutex
	pendingPing    string
	pendingAt      time.Time
	endOnce        sync.Once
}

func NewSessionManager(cfg SessionManagerConfig) (*SessionManager, error) {
	nodeID := strings.TrimSpace(cfg.NodeID)
	networkID := strings.TrimSpace(cfg.NetworkID)
	if nodeID == "" || networkID == "" {
		return nil, errors.New("session manager node ID and network ID are required")
	}
	if cfg.MTU <= 0 || cfg.MTU > MaxOriginalPacketSize {
		return nil, fmt.Errorf("session manager MTU must be between 1 and %d", MaxOriginalPacketSize)
	}
	if cfg.Candidates == nil || cfg.Candidates.Transport() == nil || cfg.Candidates.Listener() == nil {
		return nil, errors.New("session manager candidate service is required")
	}
	if cfg.Candidates.nodeID != nodeID || cfg.Candidates.networkID != networkID {
		return nil, errors.New("session manager identity must match its candidate service")
	}
	if cfg.TLSConfig == nil {
		return nil, errors.New("session manager TLS config is required")
	}
	if cfg.TLSConfig.InsecureSkipVerify {
		return nil, errors.New("session manager TLS config may not skip verification")
	}
	if len(cfg.TLSConfig.Certificates) == 0 || cfg.TLSConfig.RootCAs == nil || cfg.TLSConfig.ClientCAs == nil {
		return nil, errors.New("session manager TLS config requires a certificate and mutual CA pools")
	}
	if cfg.TLSConfig.ClientAuth != tls.RequireAndVerifyClientCert {
		return nil, errors.New("session manager TLS config must require and verify client certificates")
	}
	if !containsString(cfg.TLSConfig.NextProtos, sessionALPN) {
		return nil, fmt.Errorf("session manager TLS config must advertise ALPN %q", sessionALPN)
	}
	if err := validatePrivateIPv4Address(cfg.LocalVirtualIP); err != nil {
		return nil, fmt.Errorf("session manager local virtual IP: %w", err)
	}
	localRoutes := append([]netip.Prefix(nil), cfg.LocalRoutes...)
	for index, route := range localRoutes {
		if err := validatePrivatePrefix(route); err != nil {
			return nil, fmt.Errorf("session manager local route %d: %w", index, err)
		}
		localRoutes[index] = route.Masked()
	}
	if cfg.PeerMember == nil {
		return nil, errors.New("session manager peer authorization callback is required")
	}
	if cfg.DeliverPacket == nil && cfg.DeliverPacketContext == nil {
		return nil, errors.New("session manager packet delivery callback is required")
	}

	heartbeatInterval := cfg.HeartbeatInterval
	if heartbeatInterval <= 0 {
		heartbeatInterval = defaultSessionHeartbeatInterval
	}
	heartbeatTimeout := cfg.HeartbeatTimeout
	if heartbeatTimeout <= 0 {
		heartbeatTimeout = defaultSessionHeartbeatTimeout
	}
	if heartbeatTimeout <= heartbeatInterval {
		return nil, errors.New("session heartbeat timeout must exceed its interval")
	}
	dialTimeout := cfg.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = defaultSessionDialTimeout
	}
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	tlsConfig := cfg.TLSConfig.Clone()
	tlsConfig.MinVersion = tls.VersionTLS13
	tlsConfig.NextProtos = []string{sessionALPN}
	quicConfig := normalizedSessionQUICConfig(cfg.QUICConfig)
	if err := validateCandidateSessionProfile(cfg.Candidates, tlsConfig); err != nil {
		return nil, fmt.Errorf("session manager candidate listener: %w", err)
	}
	if err := cfg.Candidates.claimSessionManager(); err != nil {
		return nil, fmt.Errorf("session manager candidate service: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager := &SessionManager{
		nodeID:               nodeID,
		networkID:            networkID,
		mtu:                  cfg.MTU,
		candidates:           cfg.Candidates,
		tlsConfig:            tlsConfig,
		quicConfig:           quicConfig,
		localVirtualIP:       cfg.LocalVirtualIP,
		localRoutes:          localRoutes,
		peerMember:           cfg.PeerMember,
		requestSession:       cfg.RequestSession,
		deliverPacket:        cfg.DeliverPacket,
		deliverPacketContext: cfg.DeliverPacketContext,
		sessionChanged:       cfg.SessionChanged,
		heartbeatInterval:    heartbeatInterval,
		heartbeatTimeout:     heartbeatTimeout,
		dialTimeout:          dialTimeout,
		now:                  now,
		logger:               cfg.Logger,
		reassembler:          NewReassembler(cfg.MTU),
		ctx:                  ctx,
		cancel:               cancel,
		pairs:                make(map[string]*sessionPair),
		coordinatorAvailable: true,
	}
	manager.wg.Add(2)
	go manager.acceptLoop()
	go manager.maintenanceLoop()
	return manager, nil
}

func validateCandidateSessionProfile(candidates *CandidateService, managerTLS *tls.Config) error {
	if candidates == nil || candidates.tlsConfig == nil || candidates.quicConfig == nil {
		return errors.New("effective TLS and QUIC configurations are unavailable")
	}
	listenerTLS := candidates.tlsConfig
	if listenerTLS.InsecureSkipVerify || listenerTLS.MinVersion < tls.VersionTLS13 || (listenerTLS.MaxVersion != 0 && listenerTLS.MaxVersion < tls.VersionTLS13) {
		return errors.New("listener must use verified TLS 1.3")
	}
	if !containsString(listenerTLS.NextProtos, sessionALPN) {
		return fmt.Errorf("listener must advertise ALPN %q", sessionALPN)
	}
	if listenerTLS.ClientAuth != tls.RequireAndVerifyClientCert || listenerTLS.RootCAs == nil || listenerTLS.ClientCAs == nil {
		return errors.New("listener must require the same mutual CA profile")
	}
	if len(listenerTLS.Certificates) == 0 || len(managerTLS.Certificates) == 0 ||
		len(listenerTLS.Certificates[0].Certificate) == 0 || len(managerTLS.Certificates[0].Certificate) == 0 ||
		!bytes.Equal(listenerTLS.Certificates[0].Certificate[0], managerTLS.Certificates[0].Certificate[0]) {
		return errors.New("listener and dialer certificates differ")
	}
	if managerTLS.RootCAs == nil || managerTLS.ClientCAs == nil ||
		!listenerTLS.RootCAs.Equal(managerTLS.RootCAs) || !listenerTLS.ClientCAs.Equal(managerTLS.ClientCAs) {
		return errors.New("listener and dialer CA pools differ")
	}
	listenerQUIC := candidates.quicConfig
	if !listenerQUIC.EnableDatagrams || listenerQUIC.Allow0RTT || listenerQUIC.KeepAlivePeriod != defaultSessionKeepAlive || listenerQUIC.MaxIdleTimeout != defaultSessionMaxIdle {
		return errors.New("listener QUIC profile does not enforce datagrams, no 0-RTT, keepalive, and idle timeout")
	}
	return nil
}

func normalizedSessionQUICConfig(input *quic.Config) *quic.Config {
	var config *quic.Config
	if input == nil {
		config = &quic.Config{}
	} else {
		config = input.Clone()
	}
	config.EnableDatagrams = true
	config.Allow0RTT = false
	config.KeepAlivePeriod = defaultSessionKeepAlive
	config.MaxIdleTimeout = defaultSessionMaxIdle
	return config
}

func (m *SessionManager) InstallOffer(offer proto.SessionOffer) error {
	now := m.clock()
	normalized, key, err := m.validateOffer(offer, now)
	if err != nil {
		return err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		zeroBytes(key)
		return ErrSessionManagerClosed
	}
	pair := m.ensurePairLocked(normalized.PeerNodeID)
	if pair.closed {
		m.mu.Unlock()
		zeroBytes(key)
		return fmt.Errorf("%w: peer %q is closed", ErrSessionOfferRejected, normalized.PeerNodeID)
	}
	if normalized.Generation <= pair.highestGeneration {
		m.mu.Unlock()
		zeroBytes(key)
		return fmt.Errorf("%w: generation %d is not newer than %d", ErrSessionOfferRejected, normalized.Generation, pair.highestGeneration)
	}
	if pair.attempt != nil {
		m.consumeAttemptLocked(pair.attempt)
	}
	pair.highestGeneration = normalized.Generation
	pair.attempt = &sessionAttempt{
		offer:     normalized,
		key:       key,
		startedCh: make(chan struct{}),
		done:      make(chan struct{}),
		punchDone: make(chan struct{}),
	}
	pair.requestInFlight = false
	pair.requestEpoch++
	m.cancelRequestRetryLocked(pair, false)
	if pair.active == nil {
		pair.snapshot.SessionID = normalized.SessionID
		pair.snapshot.Generation = normalized.Generation
		pair.snapshot.State = PathStatePreparing
		pair.snapshot.PathType = ""
		pair.snapshot.ErrorCode = ""
	}
	snapshot := m.snapshotLocked(pair)
	m.mu.Unlock()
	m.emit(snapshot)
	return nil
}

func (m *SessionManager) StartOffer(start proto.SessionStart) error {
	if strings.TrimSpace(start.SessionID) == "" || start.Generation == 0 {
		return fmt.Errorf("%w: session ID and generation are required", ErrSessionOfferRejected)
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrSessionManagerClosed
	}
	pair, attempt := m.findAttemptLocked(start.SessionID, start.Generation)
	if attempt == nil {
		m.mu.Unlock()
		return fmt.Errorf("%w: offer %q generation %d is missing or consumed", ErrSessionOfferRejected, start.SessionID, start.Generation)
	}
	if attempt.started {
		m.mu.Unlock()
		return fmt.Errorf("%w: offer %q generation %d already started", ErrSessionOfferRejected, start.SessionID, start.Generation)
	}
	if !m.clock().Before(attempt.offer.ExpiresAt) {
		m.consumeAttemptLocked(attempt)
		pair.attempt = nil
		request := false
		if pair.active == nil {
			pair.snapshot.PathType = ""
			pair.snapshot.ErrorCode = "session_authorization_failed"
			if m.coordinatorAvailable {
				pair.snapshot.State = PathStateReconnecting
				request = m.beginRequestLocked(pair)
			} else {
				pair.snapshot.State = PathStateWaitingCoordinator
			}
		}
		snapshot := m.snapshotLocked(pair)
		peerID := pair.peerID
		m.mu.Unlock()
		m.emit(snapshot)
		if request {
			m.invokeRequest(peerID)
		}
		return fmt.Errorf("%w: offer expired", ErrSessionOfferRejected)
	}
	attemptTimeout := m.dialTimeout
	if remaining := attempt.offer.ExpiresAt.Sub(m.clock()); remaining < attemptTimeout {
		attemptTimeout = remaining
	}
	attempt.ctx, attempt.cancel = context.WithTimeout(m.ctx, attemptTimeout)
	attempt.started = true
	close(attempt.startedCh)
	if pair.active == nil {
		pair.snapshot.State = PathStatePunching
		pair.snapshot.PathType = ""
		pair.snapshot.ErrorCode = ""
	}
	snapshot := m.snapshotLocked(pair)
	peerID := pair.peerID
	m.wg.Add(1)
	m.mu.Unlock()
	go m.runOffer(peerID, attempt)
	m.emit(snapshot)
	return nil
}

// AbortOffer consumes exactly one installed generation. It is intentionally
// separate from ClosePeer: a coordinator abort ends only the pending attempt
// and must never tear down an already-authorized session for the same peer.
func (m *SessionManager) AbortOffer(abort proto.SessionAbort) error {
	abort.SessionID = strings.TrimSpace(abort.SessionID)
	if abort.SessionID == "" || abort.Generation == 0 {
		return fmt.Errorf("%w: abort session ID and generation are required", ErrSessionOfferRejected)
	}
	request := false
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrSessionManagerClosed
	}
	pair, attempt := m.findAttemptLocked(abort.SessionID, abort.Generation)
	if attempt == nil {
		m.mu.Unlock()
		return fmt.Errorf("%w: abort target %q generation %d is not the current offer", ErrSessionOfferRejected, abort.SessionID, abort.Generation)
	}
	m.consumeAttemptLocked(attempt)
	pair.attempt = nil
	if pair.active == nil {
		pair.snapshot.PathType = ""
		pair.snapshot.ErrorCode = sanitizeSessionCode(abort.Code)
		if !m.coordinatorAvailable {
			pair.snapshot.State = PathStateWaitingCoordinator
			if pair.snapshot.ErrorCode == "" {
				pair.snapshot.ErrorCode = "control_unavailable"
			}
		} else {
			pair.snapshot.State = PathStateReconnecting
			request = m.beginRequestLocked(pair)
		}
	}
	snapshot := m.snapshotLocked(pair)
	m.mu.Unlock()
	m.emit(snapshot)
	if request {
		m.invokeRequest(pair.peerID)
	}
	return nil
}

// EnsureSession starts negotiation without needing a queued user packet. The
// pair's live transport, offer and request state coalesce repeated membership
// notifications with each other and with traffic-triggered requests.
func (m *SessionManager) EnsureSession(peerID string) error {
	peerID = strings.TrimSpace(peerID)
	if peerID == "" || peerID == m.nodeID {
		return errors.New("session peer ID is invalid")
	}
	if _, ok := m.peerMember(peerID); !ok {
		return fmt.Errorf("%w: peer %q is not authorized by the current member snapshot", ErrSessionNotReady, peerID)
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrSessionManagerClosed
	}
	pair := m.ensurePairLocked(peerID)
	if pair.closed || pair.active != nil || pair.attempt != nil || pair.requestInFlight {
		m.mu.Unlock()
		return nil
	}
	request := false
	if !m.coordinatorAvailable {
		pair.snapshot.State = PathStateWaitingCoordinator
		pair.snapshot.ErrorCode = "control_unavailable"
	} else {
		pair.snapshot.State = PathStateRequesting
		pair.snapshot.ErrorCode = ""
		request = m.beginRequestLocked(pair)
	}
	snapshot := m.snapshotLocked(pair)
	m.mu.Unlock()
	m.emit(snapshot)
	if request {
		m.invokeRequest(peerID)
	}
	return nil
}

func (m *SessionManager) Send(peerID string, packet []byte) error {
	peerID = strings.TrimSpace(peerID)
	if peerID == "" || peerID == m.nodeID {
		return errors.New("session peer ID is invalid")
	}
	if len(packet) == 0 || len(packet) > m.mtu {
		return fmt.Errorf("session packet length %d exceeds MTU %d", len(packet), m.mtu)
	}
	if _, ok := m.peerMember(peerID); !ok {
		return fmt.Errorf("%w: peer %q is not authorized by the current member snapshot", ErrSessionNotReady, peerID)
	}
	now := m.clock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrSessionManagerClosed
	}
	pair := m.ensurePairLocked(peerID)
	if pair.closed {
		m.mu.Unlock()
		return fmt.Errorf("%w: peer %q is closed", ErrSessionNotReady, peerID)
	}
	if pair.active != nil && isDirectSessionState(pair.snapshot.State) {
		session := pair.active
		m.mu.Unlock()
		dropped := session.enqueue(packet)
		if dropped {
			m.notifyPeer(peerID)
		}
		return nil
	}
	m.prunePendingLocked(pair, now)
	m.enqueuePendingLocked(pair, packet, now)
	request := false
	if !m.coordinatorAvailable {
		pair.snapshot.State = PathStateWaitingCoordinator
		pair.snapshot.ErrorCode = "control_unavailable"
	} else if pair.attempt == nil {
		pair.snapshot.State = PathStateRequesting
		pair.snapshot.ErrorCode = ""
		request = m.beginRequestLocked(pair)
	}
	snapshot := m.snapshotLocked(pair)
	m.mu.Unlock()
	m.emit(snapshot)
	if request {
		m.invokeRequest(peerID)
	}
	return nil
}

// FailRequest concludes a coordinator-rejected pending request without touching
// an installed offer or an active session. A later Send may request again.
func (m *SessionManager) FailRequest(peerID, code string) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrSessionManagerClosed
	}
	pair := m.pairs[strings.TrimSpace(peerID)]
	if pair == nil || pair.closed || pair.active != nil || pair.attempt != nil || !pair.requestInFlight || !m.coordinatorAvailable {
		m.mu.Unlock()
		return nil
	}
	pair.requestInFlight = false
	pair.requestEpoch++
	m.cancelRequestRetryLocked(pair, false)
	pair.snapshot.State = PathStateFailed
	pair.snapshot.PathType = ""
	pair.snapshot.ErrorCode = sanitizeSessionCode(code)
	snapshot := m.snapshotLocked(pair)
	m.mu.Unlock()
	m.emit(snapshot)
	return nil
}

func (m *SessionManager) ClosePeer(peerID, code string) error {
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return errors.New("session peer ID is required")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrSessionManagerClosed
	}
	pair := m.ensurePairLocked(peerID)
	pair.closed = true
	pair.requestEpoch++
	m.cancelRequestRetryLocked(pair, true)
	if pair.attempt != nil {
		m.consumeAttemptLocked(pair.attempt)
		pair.attempt = nil
	}
	session := pair.active
	pair.active = nil
	m.captureSessionCountersLocked(pair, session)
	pair.pending = nil
	pair.pendingBytes = 0
	pair.snapshot.State = PathStateClosed
	pair.snapshot.PathType = ""
	pair.snapshot.ErrorCode = sanitizeSessionCode(code)
	snapshot := m.snapshotLocked(pair)
	m.mu.Unlock()
	if session != nil {
		session.stop("closed")
	}
	m.emit(snapshot)
	return nil
}

// ReauthorizePeer permits new offers after the owner has closed the old peer
// and published a fresh authenticated online membership. It never revives a
// transport or pairing key. Keep the generation floor so old offers stay spent.
func (m *SessionManager) ReauthorizePeer(peerID string) error {
	peerID = strings.TrimSpace(peerID)
	member, authorized := m.peerMember(peerID)
	if peerID == "" || peerID == m.nodeID || !authorized || member.Status != "online" {
		return fmt.Errorf("%w: fresh online peer authorization is required", ErrSessionNotReady)
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrSessionManagerClosed
	}
	old := m.pairs[peerID]
	if old == nil || !old.closed {
		m.mu.Unlock()
		return nil
	}
	// Replace the owner instead of reusing it: an old retry worker may still
	// be exiting and must not consume or reset the new owner's timer.
	pair := &sessionPair{
		peerID: peerID, highestGeneration: old.highestGeneration,
		snapshot: SessionSnapshot{PeerNodeID: peerID, State: PathStateIdle},
	}
	m.pairs[peerID] = pair
	snapshot := m.snapshotLocked(pair)
	m.mu.Unlock()
	m.emit(snapshot)
	return nil
}

// InterruptPeer closes only the currently authenticated transport. Unlike
// ClosePeer, it does not revoke or permanently close the pair: sessionEnded
// consumes the active authorization and derives waiting_coordinator or
// reconnecting from current coordinator availability.
func (m *SessionManager) InterruptPeer(peerID, code string) error {
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return errors.New("session peer ID is required")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrSessionManagerClosed
	}
	pair := m.pairs[peerID]
	if pair == nil || pair.closed || pair.active == nil || !isDirectSessionState(pair.snapshot.State) {
		m.mu.Unlock()
		return fmt.Errorf("%w: peer %q has no active direct session", ErrSessionNotReady, peerID)
	}
	session := pair.active
	m.mu.Unlock()
	session.stop(code)
	return nil
}

func (m *SessionManager) SetCoordinatorAvailable(available bool) {
	var snapshots []SessionSnapshot
	var requests []string
	m.mu.Lock()
	if m.closed || m.coordinatorAvailable == available {
		m.mu.Unlock()
		return
	}
	m.coordinatorAvailable = available
	for _, pair := range m.pairs {
		if pair.closed {
			continue
		}
		if !available {
			if pair.attempt != nil && !pair.attempt.started {
				m.consumeAttemptLocked(pair.attempt)
				pair.attempt = nil
			}
			pair.requestInFlight = false
			pair.requestEpoch++
			m.cancelRequestRetryLocked(pair, false)
			if pair.active != nil || (pair.attempt != nil && pair.attempt.started) {
				continue
			}
			if len(pair.pending) > 0 || pair.snapshot.State != PathStateIdle {
				pair.snapshot.State = PathStateWaitingCoordinator
				pair.snapshot.ErrorCode = "control_unavailable"
				snapshots = append(snapshots, m.snapshotLocked(pair))
			}
			continue
		}
		if pair.active != nil || (pair.attempt != nil && pair.attempt.started) {
			continue
		}
		if pair.snapshot.State == PathStateWaitingCoordinator {
			pair.snapshot.State = PathStateReconnecting
			pair.snapshot.ErrorCode = ""
			if m.beginRequestLocked(pair) {
				requests = append(requests, pair.peerID)
			}
			snapshots = append(snapshots, m.snapshotLocked(pair))
		}
	}
	m.mu.Unlock()
	for _, snapshot := range snapshots {
		m.emit(snapshot)
	}
	for _, peerID := range requests {
		m.invokeRequest(peerID)
	}
}

func (m *SessionManager) ActiveSessions() proto.ActiveSessions {
	m.mu.Lock()
	defer m.mu.Unlock()
	sessions := make([]proto.ActiveSession, 0, len(m.pairs))
	for _, pair := range m.pairs {
		if pair.active == nil || !isDirectSessionState(pair.snapshot.State) {
			continue
		}
		sessions = append(sessions, proto.ActiveSession{
			SessionID:  pair.active.sessionID,
			Generation: pair.active.generation,
			PeerNodeID: pair.peerID,
			PathType:   string(pair.active.pathType),
		})
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].PeerNodeID < sessions[j].PeerNodeID })
	return proto.ActiveSessions{Sessions: sessions}
}

func (m *SessionManager) Snapshot(peerID string) (SessionSnapshot, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pair, ok := m.pairs[strings.TrimSpace(peerID)]
	if !ok {
		return SessionSnapshot{}, false
	}
	return m.snapshotLocked(pair), true
}

func (m *SessionManager) Close() error {
	if m == nil {
		return nil
	}
	var closeSnapshots []SessionSnapshot
	closedNow := false
	m.closeOnce.Do(func() {
		var sessions []*managedSession
		m.mu.Lock()
		m.closed = true
		m.cancel()
		for _, pair := range m.pairs {
			pair.closed = true
			pair.requestEpoch++
			m.cancelRequestRetryLocked(pair, true)
			if pair.attempt != nil {
				m.consumeAttemptLocked(pair.attempt)
				pair.attempt = nil
			}
			if pair.active != nil {
				sessions = append(sessions, pair.active)
				m.captureSessionCountersLocked(pair, pair.active)
				pair.active = nil
			}
			pair.pending = nil
			pair.pendingBytes = 0
			pair.snapshot.State = PathStateClosed
			pair.snapshot.PathType = ""
			closeSnapshots = append(closeSnapshots, m.snapshotLocked(pair))
		}
		m.mu.Unlock()
		for _, session := range sessions {
			session.stop("manager closed")
		}
		m.wg.Wait()
		m.candidates.releaseSessionManager()
		closedNow = true
	})
	if closedNow && m.sessionChanged != nil {
		// closeOnce is complete and all manager-owned workers have exited before
		// user code runs, so a close-state callback may safely call Close again.
		for _, snapshot := range closeSnapshots {
			m.sessionChanged(snapshot)
		}
	}
	return nil
}

func (m *SessionManager) acceptLoop() {
	defer m.wg.Done()
	for {
		conn, err := m.candidates.Listener().Accept(m.ctx)
		if err != nil {
			if m.ctx.Err() != nil || m.candidates.closed.Load() {
				return
			}
			continue
		}
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.handleIncoming(conn)
		}()
	}
}

func (m *SessionManager) maintenanceLoop() {
	defer m.wg.Done()
	ticker := time.NewTicker(reassemblySweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			now := m.clock()
			m.reassembler.Expire(now)
			var snapshots []SessionSnapshot
			var requests []string
			m.mu.Lock()
			for _, pair := range m.pairs {
				before := len(pair.pending)
				m.prunePendingLocked(pair, now)
				changed := len(pair.pending) != before
				if pair.attempt != nil && !now.Before(pair.attempt.offer.ExpiresAt) {
					m.consumeAttemptLocked(pair.attempt)
					pair.attempt = nil
					changed = true
					if pair.active == nil && !pair.closed {
						pair.snapshot.PathType = ""
						pair.snapshot.ErrorCode = "session_authorization_failed"
						if m.coordinatorAvailable {
							pair.snapshot.State = PathStateReconnecting
							if m.beginRequestLocked(pair) {
								requests = append(requests, pair.peerID)
							}
						} else {
							pair.snapshot.State = PathStateWaitingCoordinator
						}
					}
				}
				if changed {
					snapshots = append(snapshots, m.snapshotLocked(pair))
				}
			}
			m.mu.Unlock()
			for _, snapshot := range snapshots {
				m.emit(snapshot)
			}
			for _, peerID := range requests {
				m.invokeRequest(peerID)
			}
		}
	}
}

func (m *SessionManager) handleIncoming(conn *quic.Conn) {
	state := conn.ConnectionState()
	if len(state.TLS.PeerCertificates) == 0 {
		_ = conn.CloseWithError(sessionApplicationError, "peer certificate required")
		return
	}
	peerID := strings.TrimSpace(state.TLS.PeerCertificates[0].Subject.CommonName)
	m.mu.Lock()
	pair := m.pairs[peerID]
	var attempt *sessionAttempt
	if pair != nil {
		attempt = pair.attempt
	}
	m.mu.Unlock()
	if attempt == nil {
		_ = conn.CloseWithError(sessionApplicationError, "authorized offer required")
		return
	}
	if !m.clock().Before(attempt.offer.ExpiresAt) {
		_ = conn.CloseWithError(sessionApplicationError, "authorized offer expired")
		m.failAttempt(peerID, attempt, "session_authorization_failed")
		return
	}
	if err := m.validateQUICConnection(conn, attempt, true); err != nil {
		m.logHandshakeFailure(peerID, "incoming identity", conn.RemoteAddr(), err)
		_ = conn.CloseWithError(sessionApplicationError, "peer identity rejected")
		m.failAttempt(peerID, attempt, sessionFailureCode(err))
		return
	}
	if !m.waitForAttemptStart(attempt) {
		_ = conn.CloseWithError(sessionApplicationError, "session start required")
		return
	}
	if !m.claimAttempt(peerID, attempt, false) {
		_ = conn.CloseWithError(sessionApplicationError, "duplicate or wrong-role connection")
		return
	}
	pathType, ok := m.pathForRemote(attempt.offer.Candidates, conn.RemoteAddr())
	if !ok {
		_ = conn.CloseWithError(sessionApplicationError, "remote candidate rejected")
		m.logHandshakeFailure(peerID, "incoming endpoint", conn.RemoteAddr(), errors.New("remote endpoint does not match an offered candidate"))
		m.failAttempt(peerID, attempt, "candidate_unavailable")
		return
	}
	if err := m.authenticateConnection(peerID, attempt, conn, false, pathType); err != nil {
		m.logHandshakeFailure(peerID, "incoming authorization", conn.RemoteAddr(), err)
		_ = conn.CloseWithError(sessionApplicationError, "session authorization failed")
		m.failAttempt(peerID, attempt, sessionFailureCode(err))
	}
}

func (m *SessionManager) runOffer(peerID string, attempt *sessionAttempt) {
	defer m.wg.Done()
	key := attempt.keyCopy()
	if len(key) == 0 {
		m.finishPunch(attempt, errSessionAuthorization)
		m.failAttempt(peerID, attempt, "session_authorization_failed")
		return
	}
	err := func() error {
		defer zeroBytes(key)
		return m.candidates.punchWithKey(attempt.ctx, attempt.offer.SessionID, attempt.offer.Generation, key, attempt.offer.Candidates)
	}()
	m.finishPunch(attempt, err)
	if err != nil {
		m.failAttempt(peerID, attempt, "hole_punch_timeout")
		m.logHandshakeFailure(peerID, "punch", nil, err)
		return
	}
	if m.nodeID != attempt.offer.DialerNodeID {
		<-attempt.ctx.Done()
		m.mu.Lock()
		claimed := attempt.claimed
		m.mu.Unlock()
		if !claimed {
			m.failAttempt(peerID, attempt, "hole_punch_timeout")
		}
		return
	}
	m.dialOffer(peerID, attempt)
}

func (m *SessionManager) dialOffer(peerID string, attempt *sessionAttempt) {
	candidates := make([]proto.Candidate, 0, len(attempt.offer.Candidates))
	lanRemaining, publicRemaining := 0, 0
	for _, candidate := range attempt.offer.Candidates {
		if !candidate.ExpiresAt.IsZero() && !m.clock().Before(candidate.ExpiresAt) {
			continue
		}
		candidates = append(candidates, candidate)
		if candidate.Scope == "lan" {
			lanRemaining++
		} else if candidate.Scope == "public" {
			publicRemaining++
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Priority != candidates[j].Priority {
			return candidates[i].Priority > candidates[j].Priority
		}
		return candidates[i].Scope < candidates[j].Scope
	})
	deadline, hasDeadline := attempt.ctx.Deadline()
	var lanDeadline time.Time
	if hasDeadline && publicRemaining > 0 {
		// Keep at least half the overall window for public handshakes, even
		// when a machine advertises the maximum number of LAN addresses.
		lanDeadline = time.Now().Add(time.Until(deadline) / 2)
	}
	var lastErr error
	for index, candidate := range candidates {
		lanCount, publicCount := lanRemaining, publicRemaining
		if candidate.Scope == "lan" {
			lanRemaining--
		} else if candidate.Scope == "public" {
			publicRemaining--
		}
		if !candidate.ExpiresAt.IsZero() && !m.clock().Before(candidate.ExpiresAt) {
			continue
		}
		address, err := candidateUDPAddress(candidate)
		if err != nil {
			lastErr = err
			continue
		}
		tlsConfig := m.tlsConfigForPeer(peerID, attempt.offer.PeerFingerprint)
		// Reserve time for later candidates. A silent LAN address must not
		// consume the entire offer before its public endpoint can be tried.
		dialCtx := attempt.ctx
		cancelDial := func() {}
		if hasDeadline {
			budget := time.Until(deadline) / time.Duration(len(candidates)-index)
			if candidate.Scope == "lan" && !lanDeadline.IsZero() {
				budget = min(maxLANFallbackDialTime, time.Until(lanDeadline)/time.Duration(lanCount))
			} else if candidate.Scope == "public" {
				budget = time.Until(deadline) / time.Duration(publicCount)
			}
			if budget <= 0 {
				continue
			}
			dialCtx, cancelDial = context.WithTimeout(attempt.ctx, budget)
		}
		conn, err := m.candidates.Transport().Dial(dialCtx, address, tlsConfig, m.quicConfig.Clone())
		cancelDial()
		if err != nil {
			lastErr = err
			continue
		}
		if err := m.validateQUICConnection(conn, attempt, false); err != nil {
			lastErr = err
			_ = conn.CloseWithError(sessionApplicationError, "peer identity rejected")
			m.failAttempt(peerID, attempt, sessionFailureCode(err))
			return
		}
		if !m.claimAttempt(peerID, attempt, true) {
			_ = conn.CloseWithError(sessionApplicationError, "duplicate or superseded connection")
			return
		}
		pathType, ok := pathTypeForCandidate(candidate)
		if !ok {
			_ = conn.CloseWithError(sessionApplicationError, "candidate scope rejected")
			m.failAttempt(peerID, attempt, "candidate_unavailable")
			return
		}
		if err := m.authenticateConnection(peerID, attempt, conn, true, pathType); err != nil {
			_ = conn.CloseWithError(sessionApplicationError, "session authorization failed")
			m.failAttempt(peerID, attempt, sessionFailureCode(err))
		}
		return
	}
	if lastErr == nil {
		lastErr = errors.New("no live peer candidate")
	}
	m.logHandshakeFailure(peerID, "outgoing handshake", nil, lastErr)
	m.failAttempt(peerID, attempt, "quic_handshake_failed")
}

func (m *SessionManager) logHandshakeFailure(peerID, stage string, remote net.Addr, err error) {
	if m.logger != nil && !errors.Is(err, context.Canceled) {
		// Never log offers, pairing keys, credentials or packet contents.
		m.logger.Warn("direct handshake failed", "peer", peerID, "stage", stage, "remote", remote, "err", err)
	}
}

func (m *SessionManager) authenticateConnection(peerID string, attempt *sessionAttempt, conn *quic.Conn, outgoing bool, pathType PathType) error {
	var stream *quic.Stream
	var err error
	if outgoing {
		stream, err = conn.OpenStreamSync(attempt.ctx)
	} else {
		stream, err = conn.AcceptStream(attempt.ctx)
	}
	if err != nil {
		return fmt.Errorf("open SessionHello stream: %w", err)
	}
	writer := &sessionWireWriter{}
	key := attempt.keyCopy()
	if len(key) == 0 {
		return errSessionAuthorization
	}
	err = exchangeSessionHello(attempt.ctx, stream, writer, attempt.offer.SessionID, attempt.offer.Generation, m.nodeID, peerID, key)
	zeroBytes(key)
	if err != nil {
		return err
	}
	if !m.clock().Before(attempt.offer.ExpiresAt) {
		return fmt.Errorf("%w: offer expired before authorization completed", errSessionAuthorization)
	}
	if !m.activateSession(peerID, attempt, conn, stream, writer, pathType) {
		return errors.New("authenticated session was superseded")
	}
	return nil
}

func (m *SessionManager) activateSession(peerID string, attempt *sessionAttempt, conn *quic.Conn, stream *quic.Stream, writer *sessionWireWriter, pathType PathType) bool {
	session := newManagedSession(m, peerID, attempt.offer.SessionID, attempt.offer.Generation, pathType, conn, stream, writer)
	var old *managedSession
	var pending [][]byte
	m.mu.Lock()
	pair := m.pairs[peerID]
	if m.closed || pair == nil || pair.closed || pair.attempt != attempt || attempt.consumed {
		m.mu.Unlock()
		return false
	}
	if pair.active != nil {
		if pair.active.generation >= session.generation {
			m.mu.Unlock()
			return false
		}
		old = pair.active
		m.captureSessionCountersLocked(pair, old)
	}
	for _, queued := range pair.pending {
		if m.clock().Before(queued.queuedAt.Add(pendingPacketLifetime)) {
			pending = append(pending, queued.packet)
		} else {
			pair.pendingDropped++
		}
	}
	pair.pending = nil
	pair.pendingBytes = 0
	// Account for every session goroutine while holding m.mu. Close also takes
	// this lock before Wait, so it can never race a late WaitGroup.Add.
	m.wg.Add(4)
	pair.active = session
	pair.attempt = nil
	m.consumeAttemptLocked(attempt)
	pair.requestInFlight = false
	pair.requestAttempts = 0
	pair.requestEpoch++
	m.cancelRequestRetryLocked(pair, false)
	pair.snapshot = SessionSnapshot{
		PeerNodeID:     peerID,
		SessionID:      session.sessionID,
		Generation:     session.generation,
		State:          stateForPathType(pathType),
		PathType:       pathType,
		LastHeartbeat:  time.Unix(0, session.lastHeartbeat.Load()).UTC(),
		PendingDropped: pair.pendingDropped,
	}
	snapshot := m.snapshotLocked(pair)
	m.mu.Unlock()
	if old != nil {
		old.stop("superseded by newer generation")
	}
	session.start()
	for _, packet := range pending {
		session.enqueue(packet)
	}
	m.emit(snapshot)
	return true
}

func newManagedSession(manager *SessionManager, peerID, sessionID string, generation uint64, pathType PathType, conn *quic.Conn, stream *quic.Stream, writer *sessionWireWriter) *managedSession {
	ctx, cancel := context.WithCancel(manager.ctx)
	session := &managedSession{
		manager:    manager,
		peerID:     peerID,
		sessionID:  sessionID,
		generation: generation,
		pathType:   pathType,
		conn:       conn,
		stream:     stream,
		wireWriter: writer,
		ctx:        ctx,
		cancel:     cancel,
		sendQueue:  make(chan []byte, readySendQueueLimit),
	}
	var seed [8]byte
	if _, err := crand.Read(seed[:]); err == nil {
		session.packetID.Store(binary.BigEndian.Uint64(seed[:]))
	}
	session.lastHeartbeat.Store(manager.clock().UnixNano())
	return session
}

func (s *managedSession) start() {
	go s.sendLoop()
	go s.datagramLoop()
	go s.heartbeatWriteLoop()
	go s.heartbeatReadLoop()
}

func (s *managedSession) enqueue(packet []byte) bool {
	copyPacket := append([]byte(nil), packet...)
	dropped := false
	for {
		select {
		case s.sendQueue <- copyPacket:
			return dropped
		default:
		}
		select {
		case <-s.sendQueue:
			s.sendDropped.Add(1)
			dropped = true
		default:
		}
	}
}

func (s *managedSession) sendLoop() {
	defer s.manager.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case packet := <-s.sendQueue:
			fragments, err := FragmentPacket(s.packetID.Add(1), packet, s.manager.mtu)
			if err != nil {
				s.sendDropped.Add(1)
				continue
			}
			for _, fragment := range fragments {
				if err := s.conn.SendDatagram(fragment); err != nil {
					s.stop("direct_unreachable_no_relay")
					return
				}
			}
			s.bytesSent.Add(uint64(len(packet)))
			s.manager.notifyPeer(s.peerID)
		}
	}
}

func (s *managedSession) datagramLoop() {
	defer s.manager.wg.Done()
	for {
		fragment, err := s.conn.ReceiveDatagram(s.ctx)
		if err != nil {
			if s.ctx.Err() == nil {
				s.stop("direct_unreachable_no_relay")
			}
			return
		}
		packet, complete, err := s.manager.reassembler.addForGeneration(s.peerID, s.generation, fragment, s.manager.clock())
		if err != nil {
			s.inboundDropped.Add(1)
			s.manager.notifyPeer(s.peerID)
			continue
		}
		if !complete {
			continue
		}
		peer, ok := s.manager.peerMember(s.peerID)
		if !ok || ValidateInboundPacket(peer, s.manager.localVirtualIP, s.manager.localRoutes, packet) != nil {
			s.inboundDropped.Add(1)
			s.manager.notifyPeer(s.peerID)
			continue
		}
		if err := s.manager.invokeDeliverPacket(s.ctx, s.peerID, append([]byte(nil), packet...)); err != nil {
			s.inboundDropped.Add(1)
			s.manager.notifyPeer(s.peerID)
			continue
		}
		s.bytesReceived.Add(uint64(len(packet)))
		s.manager.notifyPeer(s.peerID)
	}
}

func (s *managedSession) heartbeatWriteLoop() {
	defer s.manager.wg.Done()
	ticker := time.NewTicker(s.manager.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			now := s.manager.clock()
			last := time.Unix(0, s.lastHeartbeat.Load())
			if now.Sub(last) >= s.manager.heartbeatTimeout {
				s.stop("direct_heartbeat_timeout")
				return
			}
			ping, err := newHeartbeatMessage(sessionWireTypePing)
			if err != nil {
				s.stop("direct_unreachable_no_relay")
				return
			}
			s.heartbeatMu.Lock()
			s.pendingPing = ping.Nonce
			s.pendingAt = now
			s.heartbeatMu.Unlock()
			if err := s.wireWriter.write(s.stream, ping); err != nil {
				s.stop("direct_unreachable_no_relay")
				return
			}
		}
	}
}

func (s *managedSession) heartbeatReadLoop() {
	defer s.manager.wg.Done()
	if err := s.readHeartbeats(); err != nil && s.ctx.Err() == nil {
		s.stop("direct_unreachable_no_relay")
	}
}

func (s *managedSession) readHeartbeats() error {
	for {
		message, err := readSessionWireMessage(s.stream)
		if err != nil {
			return err
		}
		if err := validateHeartbeatMessage(message); err != nil {
			return err
		}
		now := s.manager.clock()
		s.lastHeartbeat.Store(now.UnixNano())
		if message.Type == sessionWireTypePing {
			message.Type = sessionWireTypePong
			if err := s.wireWriter.write(s.stream, message); err != nil {
				return err
			}
		} else {
			s.heartbeatMu.Lock()
			if message.Nonce == s.pendingPing && !s.pendingAt.IsZero() {
				s.rtt.Store(now.Sub(s.pendingAt).Nanoseconds())
				s.pendingPing = ""
				s.pendingAt = time.Time{}
			}
			s.heartbeatMu.Unlock()
		}
		s.manager.notifyPeer(s.peerID)
	}
}

func (s *managedSession) stop(code string) {
	s.endOnce.Do(func() {
		s.cancel()
		_ = s.conn.CloseWithError(sessionApplicationError, sanitizeSessionCode(code))
		s.manager.sessionEnded(s, sanitizeSessionCode(code))
	})
}

func (m *SessionManager) sessionEnded(session *managedSession, code string) {
	request := false
	m.mu.Lock()
	pair := m.pairs[session.peerID]
	if pair == nil || pair.active != session {
		m.mu.Unlock()
		return
	}
	m.captureSessionCountersLocked(pair, session)
	pair.active = nil
	pair.snapshot.PathType = ""
	pair.snapshot.ErrorCode = sanitizeSessionCode(code)
	if pair.attempt != nil && !m.clock().Before(pair.attempt.offer.ExpiresAt) {
		m.consumeAttemptLocked(pair.attempt)
		pair.attempt = nil
	}
	if pair.attempt != nil {
		pair.snapshot.SessionID = pair.attempt.offer.SessionID
		pair.snapshot.Generation = pair.attempt.offer.Generation
		pair.snapshot.ErrorCode = ""
		switch {
		case pair.attempt.claimed:
			pair.snapshot.State = PathStateAuthenticating
		case pair.attempt.started:
			pair.snapshot.State = PathStatePunching
		default:
			pair.snapshot.State = PathStatePreparing
		}
	} else if m.closed || pair.closed {
		pair.snapshot.State = PathStateClosed
	} else if !m.coordinatorAvailable {
		pair.snapshot.State = PathStateWaitingCoordinator
	} else {
		pair.snapshot.State = PathStateReconnecting
		request = m.beginRequestLocked(pair)
	}
	snapshot := m.snapshotLocked(pair)
	m.mu.Unlock()
	m.emit(snapshot)
	if request {
		m.invokeRequest(session.peerID)
	}
}

func (m *SessionManager) validateOffer(offer proto.SessionOffer, now time.Time) (proto.SessionOffer, []byte, error) {
	offer.SessionID = strings.TrimSpace(offer.SessionID)
	offer.PeerNodeID = strings.TrimSpace(offer.PeerNodeID)
	offer.PeerFingerprint = strings.TrimSpace(offer.PeerFingerprint)
	offer.DialerNodeID = strings.TrimSpace(offer.DialerNodeID)
	if offer.SessionID == "" || offer.Generation == 0 || offer.PeerNodeID == "" || offer.PeerNodeID == m.nodeID {
		return proto.SessionOffer{}, nil, fmt.Errorf("%w: session, generation, and distinct peer are required", ErrSessionOfferRejected)
	}
	expectedDialer := m.nodeID
	if offer.PeerNodeID < expectedDialer {
		expectedDialer = offer.PeerNodeID
	}
	if offer.DialerNodeID != expectedDialer {
		return proto.SessionOffer{}, nil, fmt.Errorf("%w: dialer %q is not stable lower node %q", ErrSessionOfferRejected, offer.DialerNodeID, expectedDialer)
	}
	normalizedFingerprint, err := normalizeSHA256Fingerprint(offer.PeerFingerprint)
	if err != nil {
		return proto.SessionOffer{}, nil, fmt.Errorf("%w: peer fingerprint: %v", ErrSessionOfferRejected, err)
	}
	offer.PeerFingerprint = normalizedFingerprint
	if offer.ExpiresAt.IsZero() || !now.Before(offer.ExpiresAt.UTC()) {
		return proto.SessionOffer{}, nil, fmt.Errorf("%w: offer is expired", ErrSessionOfferRejected)
	}
	key, err := decodeSecret(offer.PairingKey)
	if err != nil {
		return proto.SessionOffer{}, nil, fmt.Errorf("%w: invalid pairing key", ErrSessionOfferRejected)
	}
	if len(offer.Candidates) == 0 || len(offer.Candidates) > maxPunchCandidates {
		zeroBytes(key)
		return proto.SessionOffer{}, nil, fmt.Errorf("%w: candidate count is outside bounds", ErrSessionOfferRejected)
	}
	candidates := append([]proto.Candidate(nil), offer.Candidates...)
	for index, candidate := range candidates {
		if _, err := candidateUDPAddress(candidate); err != nil {
			zeroBytes(key)
			return proto.SessionOffer{}, nil, fmt.Errorf("%w: candidate %d: %v", ErrSessionOfferRejected, index, err)
		}
		if _, ok := pathTypeForCandidate(candidate); !ok {
			zeroBytes(key)
			return proto.SessionOffer{}, nil, fmt.Errorf("%w: candidate %d has invalid scope", ErrSessionOfferRejected, index)
		}
	}
	offer.Candidates = candidates
	offer.ExpiresAt = offer.ExpiresAt.UTC()
	offer.PairingKey = ""
	return offer, key, nil
}

func (m *SessionManager) validateQUICConnection(conn *quic.Conn, attempt *sessionAttempt, inbound bool) error {
	state := conn.ConnectionState()
	if state.Used0RTT {
		return errors.New("0-RTT is forbidden")
	}
	if state.TLS.Version != tls.VersionTLS13 || state.TLS.NegotiatedProtocol != sessionALPN {
		return errors.New("TLS version or ALPN mismatch")
	}
	if !state.SupportsDatagrams.Local || !state.SupportsDatagrams.Remote {
		return errors.New("QUIC DATAGRAM support is required")
	}
	if len(state.TLS.PeerCertificates) == 0 {
		return errors.New("peer certificate is missing")
	}
	roots := m.tlsConfig.RootCAs
	usage := x509.ExtKeyUsageServerAuth
	if inbound {
		roots = m.tlsConfig.ClientCAs
		usage = x509.ExtKeyUsageClientAuth
	}
	return verifyPeerCertificateState(state.TLS, attempt.offer.PeerNodeID, attempt.offer.PeerFingerprint, roots, usage, m.clock())
}

func (m *SessionManager) tlsConfigForPeer(peerID, fingerprint string) *tls.Config {
	config := m.tlsConfig.Clone()
	config.MinVersion = tls.VersionTLS13
	config.ServerName = ""
	config.NextProtos = []string{sessionALPN}
	// Production device certificates are deliberately CN-only. Disable Go's
	// Web-PKI DNS-name check, then perform the complete Meshlink verification
	// (CA chain, ServerAuth EKU, validity, exact CN and offer fingerprint) below.
	config.InsecureSkipVerify = true
	previous := config.VerifyConnection
	config.VerifyConnection = func(state tls.ConnectionState) error {
		if err := verifyPeerCertificateState(state, peerID, fingerprint, config.RootCAs, x509.ExtKeyUsageServerAuth, m.clock()); err != nil {
			return err
		}
		if previous != nil {
			if err := previous(state); err != nil {
				return err
			}
		}
		return nil
	}
	return config
}

func verifyPeerCertificateState(state tls.ConnectionState, peerID, fingerprint string, roots *x509.CertPool, usage x509.ExtKeyUsage, now time.Time) error {
	if roots == nil {
		return errors.New("peer CA roots are missing")
	}
	if len(state.PeerCertificates) == 0 {
		return errors.New("peer certificate is missing")
	}
	leaf := state.PeerCertificates[0]
	if leaf.Subject.CommonName != peerID {
		return errors.New("peer certificate common name mismatch")
	}
	wantFingerprint, err := normalizeSHA256Fingerprint(fingerprint)
	if err != nil {
		return fmt.Errorf("peer certificate fingerprint: %w", err)
	}
	gotFingerprint := sha256.Sum256(leaf.Raw)
	if hex.EncodeToString(gotFingerprint[:]) != wantFingerprint {
		return errors.New("peer certificate fingerprint mismatch")
	}
	intermediates := x509.NewCertPool()
	for _, certificate := range state.PeerCertificates[1:] {
		intermediates.AddCert(certificate)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		CurrentTime:   now,
		KeyUsages:     []x509.ExtKeyUsage{usage},
	}); err != nil {
		return fmt.Errorf("verify peer certificate chain and usage: %w", err)
	}
	return nil
}

func normalizeSHA256Fingerprint(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) >= len("SHA256:") && strings.EqualFold(value[:len("SHA256:")], "SHA256:") {
		value = value[len("SHA256:"):]
		value = strings.ReplaceAll(value, ":", "")
	}
	value = strings.ToLower(value)
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != value {
		return "", errors.New("fingerprint must be a canonical SHA-256 digest")
	}
	return value, nil
}

func (m *SessionManager) waitForAttemptStart(attempt *sessionAttempt) bool {
	timer := time.NewTimer(m.dialTimeout)
	defer timer.Stop()
	select {
	case <-attempt.startedCh:
	case <-attempt.done:
		return false
	case <-timer.C:
		return false
	case <-m.ctx.Done():
		return false
	}
	select {
	case <-attempt.punchDone:
		return attempt.punchErr == nil
	case <-attempt.done:
		return false
	case <-attempt.ctx.Done():
		return false
	case <-m.ctx.Done():
		return false
	}
}

func (m *SessionManager) claimAttempt(peerID string, attempt *sessionAttempt, outgoing bool) bool {
	m.mu.Lock()
	pair := m.pairs[peerID]
	if m.closed || pair == nil || pair.attempt != attempt || attempt.consumed || !attempt.started || attempt.claimed {
		m.mu.Unlock()
		return false
	}
	properRole := (outgoing && m.nodeID == attempt.offer.DialerNodeID) || (!outgoing && peerID == attempt.offer.DialerNodeID)
	if !properRole {
		m.mu.Unlock()
		return false
	}
	attempt.claimed = true
	if pair.active == nil {
		pair.snapshot.State = PathStateAuthenticating
		pair.snapshot.PathType = ""
	}
	snapshot := m.snapshotLocked(pair)
	m.mu.Unlock()
	m.emit(snapshot)
	return true
}

func (m *SessionManager) failAttempt(peerID string, attempt *sessionAttempt, code string) {
	request := false
	m.mu.Lock()
	pair := m.pairs[peerID]
	if pair == nil || pair.attempt != attempt {
		m.mu.Unlock()
		return
	}
	m.consumeAttemptLocked(attempt)
	pair.attempt = nil
	if pair.active == nil {
		pair.snapshot.PathType = ""
		pair.snapshot.ErrorCode = sanitizeSessionCode(code)
		if m.closed || pair.closed {
			pair.snapshot.State = PathStateClosed
		} else if !m.coordinatorAvailable {
			pair.snapshot.State = PathStateWaitingCoordinator
		} else {
			pair.snapshot.State = PathStateReconnecting
			request = m.beginRequestLocked(pair)
		}
	}
	snapshot := m.snapshotLocked(pair)
	m.mu.Unlock()
	m.emit(snapshot)
	if request {
		m.invokeRequest(peerID)
	}
}

func (m *SessionManager) finishPunch(attempt *sessionAttempt, err error) {
	attempt.punchErr = err
	attempt.punchOnce.Do(func() { close(attempt.punchDone) })
}

func (m *SessionManager) consumeAttemptLocked(attempt *sessionAttempt) {
	if attempt == nil || attempt.consumed {
		return
	}
	attempt.consumed = true
	close(attempt.done)
	if attempt.cancel != nil {
		attempt.cancel()
	}
	attempt.keyMu.Lock()
	zeroBytes(attempt.key)
	attempt.key = nil
	attempt.keyMu.Unlock()
}

func (a *sessionAttempt) keyCopy() []byte {
	a.keyMu.Lock()
	defer a.keyMu.Unlock()
	if len(a.key) == 0 {
		return nil
	}
	return append([]byte(nil), a.key...)
}

func (m *SessionManager) findAttemptLocked(sessionID string, generation uint64) (*sessionPair, *sessionAttempt) {
	for _, pair := range m.pairs {
		if pair.attempt != nil && pair.attempt.offer.SessionID == sessionID && pair.attempt.offer.Generation == generation {
			return pair, pair.attempt
		}
	}
	return nil, nil
}

func (m *SessionManager) ensurePairLocked(peerID string) *sessionPair {
	if pair := m.pairs[peerID]; pair != nil {
		return pair
	}
	pair := &sessionPair{
		peerID: peerID,
		snapshot: SessionSnapshot{
			PeerNodeID: peerID,
			State:      PathStateIdle,
		},
	}
	m.pairs[peerID] = pair
	return pair
}

func (m *SessionManager) snapshotLocked(pair *sessionPair) SessionSnapshot {
	snapshot := pair.snapshot
	snapshot.PendingPackets = len(pair.pending)
	snapshot.PendingBytes = pair.pendingBytes
	snapshot.PendingDropped = pair.pendingDropped
	if pair.active != nil {
		snapshot.SessionID = pair.active.sessionID
		snapshot.Generation = pair.active.generation
		snapshot.PathType = pair.active.pathType
		snapshot.BytesSent = pair.active.bytesSent.Load()
		snapshot.BytesReceived = pair.active.bytesReceived.Load()
		snapshot.SendQueueDropped = pair.active.sendDropped.Load()
		snapshot.InboundDropped = pair.active.inboundDropped.Load()
		if nanos := pair.active.lastHeartbeat.Load(); nanos != 0 {
			snapshot.LastHeartbeat = time.Unix(0, nanos).UTC()
		}
		snapshot.RTT = time.Duration(pair.active.rtt.Load())
	}
	return snapshot
}

func (m *SessionManager) captureSessionCountersLocked(pair *sessionPair, session *managedSession) {
	if pair == nil || session == nil {
		return
	}
	pair.snapshot.SessionID = session.sessionID
	pair.snapshot.Generation = session.generation
	pair.snapshot.BytesSent = session.bytesSent.Load()
	pair.snapshot.BytesReceived = session.bytesReceived.Load()
	pair.snapshot.SendQueueDropped = session.sendDropped.Load()
	pair.snapshot.InboundDropped = session.inboundDropped.Load()
	if nanos := session.lastHeartbeat.Load(); nanos != 0 {
		pair.snapshot.LastHeartbeat = time.Unix(0, nanos).UTC()
	}
	pair.snapshot.RTT = time.Duration(session.rtt.Load())
}

func (m *SessionManager) notifyPeer(peerID string) {
	if m.sessionChanged == nil {
		return
	}
	m.mu.Lock()
	pair := m.pairs[peerID]
	if pair == nil {
		m.mu.Unlock()
		return
	}
	snapshot := m.snapshotLocked(pair)
	m.mu.Unlock()
	m.emit(snapshot)
}

func (m *SessionManager) emit(snapshot SessionSnapshot) {
	if m.sessionChanged != nil {
		m.invokeCallback(func() { m.sessionChanged(snapshot) })
	}
}

func (m *SessionManager) invokeDeliverPacket(ctx context.Context, peerID string, packet []byte) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.ctx.Done():
		return ErrSessionManagerClosed
	default:
	}
	result := make(chan error, 1)
	go func() {
		if m.deliverPacketContext != nil {
			result <- m.deliverPacketContext(ctx, peerID, packet)
		} else if err := ctx.Err(); err != nil {
			result <- err
		} else {
			result <- m.deliverPacket(peerID, packet)
		}
	}()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-m.ctx.Done():
		return ErrSessionManagerClosed
	}
}

func (m *SessionManager) invokeCallback(callback func()) {
	select {
	case <-m.ctx.Done():
		return
	default:
	}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		callback()
	}()
	select {
	case <-finished:
	case <-m.ctx.Done():
	}
}

func (m *SessionManager) clock() time.Time {
	return m.now().UTC()
}

func (m *SessionManager) prunePendingLocked(pair *sessionPair, now time.Time) {
	kept := pair.pending[:0]
	bytesKept := 0
	for _, queued := range pair.pending {
		if !now.Before(queued.queuedAt.Add(pendingPacketLifetime)) {
			pair.pendingDropped++
			continue
		}
		kept = append(kept, queued)
		bytesKept += len(queued.packet)
	}
	pair.pending = kept
	pair.pendingBytes = bytesKept
}

func (m *SessionManager) enqueuePendingLocked(pair *sessionPair, packet []byte, now time.Time) {
	copyPacket := append([]byte(nil), packet...)
	m.pendingSequence++
	pair.pending = append(pair.pending, pendingPacket{packet: copyPacket, queuedAt: now, sequence: m.pendingSequence})
	pair.pendingBytes += len(copyPacket)
	for len(pair.pending) > pendingPacketLimit || pair.pendingBytes > pendingByteLimit {
		pair.pendingBytes -= len(pair.pending[0].packet)
		pair.pending[0].packet = nil
		pair.pending = pair.pending[1:]
		pair.pendingDropped++
	}
	m.enforceGlobalPendingLocked()
}

func (m *SessionManager) enforceGlobalPendingLocked() {
	for {
		totalPackets := 0
		totalBytes := 0
		var oldestPair *sessionPair
		var oldestSequence uint64
		for _, pair := range m.pairs {
			totalPackets += len(pair.pending)
			totalBytes += pair.pendingBytes
			if len(pair.pending) == 0 {
				continue
			}
			sequence := pair.pending[0].sequence
			if oldestPair == nil || sequence < oldestSequence {
				oldestPair = pair
				oldestSequence = sequence
			}
		}
		if totalPackets <= globalPendingPacketLimit && totalBytes <= globalPendingByteLimit {
			return
		}
		if oldestPair == nil {
			return
		}
		oldestPair.pendingBytes -= len(oldestPair.pending[0].packet)
		oldestPair.pending[0].packet = nil
		oldestPair.pending = oldestPair.pending[1:]
		oldestPair.pendingDropped++
	}
}

func (m *SessionManager) beginRequestLocked(pair *sessionPair) bool {
	if pair.requestInFlight || !m.coordinatorAvailable || pair.closed || m.closed {
		return false
	}
	pair.requestInFlight = true
	pair.requestAttempts++
	pair.requestEpoch++
	delay := requestBackoff(pair.requestAttempts)
	pair.retryAt = time.Now().Add(delay)
	if pair.retryTimer == nil {
		pair.retryTimer = time.NewTimer(delay)
		pair.retryStop = make(chan struct{})
		m.wg.Add(1)
		go m.requestRetryAfter(pair, pair.retryTimer, pair.retryStop)
	} else {
		pair.retryTimer.Reset(delay)
	}
	return true
}

func (m *SessionManager) cancelRequestRetryLocked(pair *sessionPair, closeOwner bool) {
	if pair.retryTimer != nil {
		pair.retryTimer.Stop()
	}
	pair.retryAt = time.Time{}
	if closeOwner && pair.retryStop != nil {
		close(pair.retryStop)
		pair.retryStop = nil
	}
}

// A pair owns at most one reusable timer and worker, even when a coordinator
// repeatedly rejects requests. Reset/terminal paths stop the timer under mu;
// the idle owner is reused until ClosePeer/root Close ends it exactly once.
func (m *SessionManager) requestRetryAfter(pair *sessionPair, timer *time.Timer, stop <-chan struct{}) {
	defer m.wg.Done()
	defer timer.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-stop:
			return
		case <-timer.C:
		}
		request := false
		var snapshot SessionSnapshot
		var emit bool
		m.mu.Lock()
		// A tick already received before Stop/Reset cannot consume its successor.
		if !pair.retryAt.IsZero() && !time.Now().Before(pair.retryAt) && pair.requestInFlight && pair.attempt == nil && pair.active == nil && !pair.closed && m.coordinatorAvailable {
			m.prunePendingLocked(pair, m.clock())
			pair.requestInFlight = false
			if len(pair.pending) > 0 {
				request = m.beginRequestLocked(pair)
			} else {
				m.cancelRequestRetryLocked(pair, false)
				pair.requestEpoch++
				pair.requestAttempts = 0
				if pair.snapshot.State == PathStateRequesting {
					pair.snapshot.State = PathStateIdle
				}
				snapshot = m.snapshotLocked(pair)
				emit = true
			}
		}
		m.mu.Unlock()
		if emit {
			m.emit(snapshot)
		}
		if request {
			m.invokeRequest(pair.peerID)
		}
	}
}

func (m *SessionManager) invokeRequest(peerID string) {
	if m.requestSession != nil {
		m.invokeCallback(func() { m.requestSession(peerID) })
	}
}

func requestBackoff(attempt uint) time.Duration {
	if attempt <= 1 {
		return requestBackoffInitial
	}
	delay := requestBackoffInitial
	for index := uint(1); index < attempt && delay < requestBackoffMaximum; index++ {
		delay *= 2
		if delay > requestBackoffMaximum {
			return requestBackoffMaximum
		}
	}
	return delay
}

func (m *SessionManager) pathForRemote(candidates []proto.Candidate, remote net.Addr) (PathType, bool) {
	remoteAddress, err := addrPortFromNetAddr(remote)
	if err != nil {
		return "", false
	}
	for _, candidate := range candidates {
		address, err := netip.ParseAddr(candidate.Address)
		if err != nil {
			continue
		}
		if netip.AddrPortFrom(address.Unmap(), candidate.Port) != remoteAddress {
			continue
		}
		if !candidate.ExpiresAt.IsZero() && !m.clock().Before(candidate.ExpiresAt) {
			continue
		}
		return pathTypeForCandidate(candidate)
	}
	return "", false
}

func candidateUDPAddress(candidate proto.Candidate) (*net.UDPAddr, error) {
	address, err := netip.ParseAddr(strings.TrimSpace(candidate.Address))
	if err != nil || !address.Unmap().Is4() || candidate.Port == 0 {
		return nil, errors.New("candidate must contain an IPv4 address and non-zero port")
	}
	return net.UDPAddrFromAddrPort(netip.AddrPortFrom(address.Unmap(), candidate.Port)), nil
}

func pathTypeForCandidate(candidate proto.Candidate) (PathType, bool) {
	switch candidate.Scope {
	case "lan":
		return PathTypeLANDirect, true
	case "public":
		return PathTypePublicDirect, true
	default:
		return "", false
	}
}

func stateForPathType(pathType PathType) PathState {
	if pathType == PathTypePublicDirect {
		return PathStatePublicDirect
	}
	return PathStateLANDirect
}

func isDirectSessionState(state PathState) bool {
	return state == PathStateLANDirect || state == PathStatePublicDirect
}

func sessionFailureCode(err error) string {
	if errors.Is(err, errSessionAuthorization) || strings.Contains(strings.ToLower(err.Error()), "identity") || strings.Contains(strings.ToLower(err.Error()), "certificate") {
		return "session_authorization_failed"
	}
	return "quic_handshake_failed"
}

func sanitizeSessionCode(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return "direct_unreachable_no_relay"
	}
	if len(code) > 80 {
		code = code[:80]
	}
	for _, character := range code {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' && character != '-' {
			return "direct_unreachable_no_relay"
		}
	}
	return code
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
