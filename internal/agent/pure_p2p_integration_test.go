package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"meshlink/internal/certutil"
	"meshlink/internal/config"
	"meshlink/internal/networkstate"
	"meshlink/internal/p2p"
	"meshlink/internal/proto"
	"meshlink/internal/tlsutil"
)

const (
	integrationHeartbeatInterval = 40 * time.Millisecond
	integrationHeartbeatTimeout  = 400 * time.Millisecond
	integrationDialTimeout       = 3 * time.Second
	integrationWait              = 12 * time.Second
)

var (
	literalBToC = []byte{
		0x45, 0x00, 0x00, 0x14, 0x10, 0x01, 0x00, 0x00, 0x40, 0x11, 0x00, 0x00,
		10, 77, 0, 2, 10, 77, 0, 3,
	}
	literalCToB = []byte{
		0x45, 0x00, 0x00, 0x14, 0x10, 0x02, 0x00, 0x00, 0x40, 0x11, 0x00, 0x00,
		10, 77, 0, 3, 10, 77, 0, 2,
	}
	literalSpoofDToC = []byte{
		0x45, 0x00, 0x00, 0x14, 0x10, 0x03, 0x00, 0x00, 0x40, 0x11, 0x00, 0x00,
		10, 77, 0, 4, 10, 77, 0, 3,
	}
)

type payloadLedger struct {
	mu      sync.Mutex
	packets map[string][]byte
}

func (l *payloadLedger) add(packet []byte) {
	if len(packet) == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.packets == nil {
		l.packets = make(map[string][]byte)
	}
	l.packets[string(packet)] = append([]byte(nil), packet...)
}

func (l *payloadLedger) snapshot() [][]byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	packets := make([][]byte, 0, len(l.packets))
	for _, packet := range l.packets {
		packets = append(packets, append([]byte(nil), packet...))
	}
	return packets
}

type recordedNetworkFrame struct {
	transport, direction string
	connection           uint64
	payload              []byte
}

// controlPlaneRecorder is a transparent TCP/TLS and authenticated-probe
// boundary recorder in front of A. UDP sockets used for observed-address
// mappings forward only datagrams whose source is A; peer traffic sent to an
// observed mapping is dropped, so this recorder can never become a Relay.
type controlPlaneRecorder struct {
	backendTCP string
	backendUDP *net.UDPAddr
	tcp        net.Listener
	udp        *net.UDPConn
	addr       string

	closed     chan struct{}
	closeOnce  sync.Once
	wg         sync.WaitGroup
	nextID     atomic.Uint64
	dropped    atomic.Uint64
	dropProbes atomic.Bool

	mu       sync.Mutex
	frames   []recordedNetworkFrame
	conns    map[net.Conn]struct{}
	mappings map[string]*recordingUDPMapping
}

type recordingUDPMapping struct {
	id       uint64
	owner    *controlPlaneRecorder
	client   *net.UDPAddr
	clientID string
	conn     *net.UDPConn
	close    sync.Once
}

func listenDualProtocol() (net.Listener, *net.UDPConn, error) {
	var last error
	for attempt := 0; attempt < 100; attempt++ {
		tcp, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			last = err
			continue
		}
		udpAddr, err := net.ResolveUDPAddr("udp4", tcp.Addr().String())
		if err != nil {
			_ = tcp.Close()
			return nil, nil, err
		}
		udp, err := net.ListenUDP("udp4", udpAddr)
		if err == nil {
			return tcp, udp, nil
		}
		_ = tcp.Close()
		last = err
	}
	return nil, nil, last
}

func reserveDualProtocolAddress(t *testing.T) string {
	t.Helper()
	tcp, udp, err := listenDualProtocol()
	if err != nil {
		t.Fatal(err)
	}
	addr := tcp.Addr().String()
	_ = tcp.Close()
	_ = udp.Close()
	return addr
}

func newControlPlaneRecorder(t *testing.T, backend string) *controlPlaneRecorder {
	t.Helper()
	tcp, udp, err := listenDualProtocol()
	if err != nil {
		t.Fatal(err)
	}
	backendUDP, err := net.ResolveUDPAddr("udp4", backend)
	if err != nil {
		t.Fatal(err)
	}
	r := &controlPlaneRecorder{
		backendTCP: backend,
		backendUDP: backendUDP,
		tcp:        tcp,
		udp:        udp,
		addr:       tcp.Addr().String(),
		closed:     make(chan struct{}),
		conns:      make(map[net.Conn]struct{}),
		mappings:   make(map[string]*recordingUDPMapping),
	}
	r.wg.Add(2)
	go r.acceptTCP()
	go r.forwardUDP()
	return r
}

func (r *controlPlaneRecorder) record(transport, direction string, connection uint64, payload []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, recordedNetworkFrame{
		transport: transport, direction: direction, connection: connection,
		payload: append([]byte(nil), payload...),
	})
}

func (r *controlPlaneRecorder) track(conn net.Conn, add bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if add {
		r.conns[conn] = struct{}{}
	} else {
		delete(r.conns, conn)
	}
}

func (r *controlPlaneRecorder) acceptTCP() {
	defer r.wg.Done()
	for {
		client, err := r.tcp.Accept()
		if err != nil {
			return
		}
		r.wg.Add(1)
		go r.forwardTCPConnection(client)
	}
}

func (r *controlPlaneRecorder) forwardTCPConnection(client net.Conn) {
	defer r.wg.Done()
	id := r.nextID.Add(1)
	backend, err := net.DialTimeout("tcp4", r.backendTCP, 500*time.Millisecond)
	if err != nil {
		_ = client.Close()
		return
	}
	r.track(client, true)
	r.track(backend, true)
	defer func() {
		r.track(client, false)
		r.track(backend, false)
		_ = client.Close()
		_ = backend.Close()
	}()
	done := make(chan struct{}, 2)
	go func() {
		r.copyTCP(backend, client, id, "to_coordinator")
		closeTCPWrite(backend)
		done <- struct{}{}
	}()
	go func() {
		r.copyTCP(client, backend, id, "from_coordinator")
		closeTCPWrite(client)
		done <- struct{}{}
	}()
	// Preserve the response half after a request-side close. In particular,
	// A's terminal protocol error must reach a TLS client before teardown.
	<-done
	<-done
}

func closeTCPWrite(conn net.Conn) {
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.CloseWrite()
	}
}

func (r *controlPlaneRecorder) copyTCP(dst, src net.Conn, id uint64, direction string) {
	buffer := make([]byte, 32*1024)
	for {
		n, readErr := src.Read(buffer)
		if n > 0 {
			chunk := append([]byte(nil), buffer[:n]...)
			r.record("tcp", direction, id, chunk)
			for len(chunk) > 0 {
				written, writeErr := dst.Write(chunk)
				if writeErr != nil {
					return
				}
				chunk = chunk[written:]
			}
		}
		if readErr != nil {
			return
		}
	}
}

func (r *controlPlaneRecorder) forwardUDP() {
	defer r.wg.Done()
	buffer := make([]byte, 65535)
	for {
		n, client, err := r.udp.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		payload := append([]byte(nil), buffer[:n]...)
		r.record("udp", "to_coordinator", 0, payload)
		if r.dropProbes.Load() {
			continue
		}
		mapping, err := r.mapping(client)
		if err != nil {
			continue
		}
		if _, err := mapping.conn.WriteToUDP(payload, r.backendUDP); err != nil {
			r.removeMapping(mapping)
		}
	}
}

func (r *controlPlaneRecorder) mapping(client *net.UDPAddr) (*recordingUDPMapping, error) {
	clientID := client.String()
	r.mu.Lock()
	if mapping := r.mappings[clientID]; mapping != nil {
		r.mu.Unlock()
		return mapping, nil
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		r.mu.Unlock()
		return nil, err
	}
	mapping := &recordingUDPMapping{
		id: r.nextID.Add(1), owner: r, client: cloneUDPAddr(client), clientID: clientID, conn: conn,
	}
	r.mappings[clientID] = mapping
	r.mu.Unlock()
	r.wg.Add(1)
	go mapping.readResponses()
	return mapping, nil
}

func cloneUDPAddr(addr *net.UDPAddr) *net.UDPAddr {
	return &net.UDPAddr{IP: append(net.IP(nil), addr.IP...), Port: addr.Port, Zone: addr.Zone}
}

func (m *recordingUDPMapping) readResponses() {
	defer m.owner.wg.Done()
	buffer := make([]byte, 65535)
	for {
		n, source, err := m.conn.ReadFromUDP(buffer)
		if err != nil {
			m.owner.removeMapping(m)
			return
		}
		if source.String() != m.owner.backendUDP.String() {
			// The coordinator may advertise its observed source. A peer punch or
			// QUIC packet that reaches that socket is deliberately not forwarded.
			m.owner.dropped.Add(1)
			continue
		}
		payload := append([]byte(nil), buffer[:n]...)
		m.owner.record("udp", "from_coordinator", m.id, payload)
		if _, err := m.owner.udp.WriteToUDP(payload, m.client); err != nil {
			m.owner.removeMapping(m)
			return
		}
	}
}

func (r *controlPlaneRecorder) removeMapping(mapping *recordingUDPMapping) {
	mapping.close.Do(func() {
		r.mu.Lock()
		if r.mappings[mapping.clientID] == mapping {
			delete(r.mappings, mapping.clientID)
		}
		r.mu.Unlock()
		_ = mapping.conn.Close()
	})
}

func (r *controlPlaneRecorder) Close() {
	r.closeOnce.Do(func() {
		close(r.closed)
		_ = r.tcp.Close()
		_ = r.udp.Close()
		r.mu.Lock()
		connections := make([]net.Conn, 0, len(r.conns))
		for conn := range r.conns {
			connections = append(connections, conn)
		}
		mappings := make([]*recordingUDPMapping, 0, len(r.mappings))
		for _, mapping := range r.mappings {
			mappings = append(mappings, mapping)
		}
		r.mu.Unlock()
		for _, conn := range connections {
			_ = conn.Close()
		}
		for _, mapping := range mappings {
			r.removeMapping(mapping)
		}
		r.wg.Wait()
	})
}

func (r *controlPlaneRecorder) snapshot() []recordedNetworkFrame {
	r.mu.Lock()
	defer r.mu.Unlock()
	frames := make([]recordedNetworkFrame, len(r.frames))
	for index, frame := range r.frames {
		frames[index] = frame
		frames[index].payload = append([]byte(nil), frame.payload...)
	}
	return frames
}

type recordedProbeWire struct {
	Kind            string `json:"kind"`
	Version         int    `json:"version"`
	ProbeID         string `json:"probe_id"`
	UnixMillis      int64  `json:"unix_millis"`
	Nonce           string `json:"nonce"`
	ObservedAddress string `json:"observed_address"`
	HMAC            string `json:"hmac"`
}

func decodeRecordedProbe(payload []byte, direction string) (recordedProbeWire, string, error) {
	if len(payload) < 2 || payload[0] != 0 {
		return recordedProbeWire{}, "", errors.New("missing non-QUIC rendezvous marker")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload[1:]))
	decoder.DisallowUnknownFields()
	var wire recordedProbeWire
	if err := decoder.Decode(&wire); err != nil {
		return recordedProbeWire{}, "", fmt.Errorf("decode probe: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return recordedProbeWire{}, "", errors.New("probe has trailing data")
	}
	wantKind := "probe_request"
	if direction == "from_coordinator" {
		wantKind = "probe_response"
	} else if direction != "to_coordinator" {
		return recordedProbeWire{}, "", fmt.Errorf("invalid probe direction %q", direction)
	}
	if wire.Kind != wantKind || wire.Version != proto.ControlProtocolVersion || wire.UnixMillis == 0 {
		return recordedProbeWire{}, "", fmt.Errorf("invalid probe header: %+v", wire)
	}
	probeID, err := base64.RawURLEncoding.DecodeString(wire.ProbeID)
	if err != nil || len(probeID) != 16 || base64.RawURLEncoding.EncodeToString(probeID) != wire.ProbeID {
		return recordedProbeWire{}, "", errors.New("probe ID is not canonical 16-byte base64url")
	}
	nonce, err := hex.DecodeString(wire.Nonce)
	if err != nil || len(nonce) != p2p.ProbeNonceSize || hex.EncodeToString(nonce) != wire.Nonce {
		return recordedProbeWire{}, "", errors.New("probe nonce is not canonical")
	}
	mac, err := hex.DecodeString(wire.HMAC)
	if err != nil || len(mac) != sha256.Size || hex.EncodeToString(mac) != wire.HMAC {
		return recordedProbeWire{}, "", errors.New("probe HMAC is not canonical")
	}
	if direction == "to_coordinator" {
		if wire.ObservedAddress != "" {
			return recordedProbeWire{}, "", errors.New("probe request disclosed an observed address")
		}
	} else {
		observed, err := netip.ParseAddrPort(wire.ObservedAddress)
		if err != nil || !observed.Addr().Unmap().Is4() || observed.Port() == 0 || observed.String() != wire.ObservedAddress {
			return recordedProbeWire{}, "", errors.New("probe response observed address is invalid")
		}
	}
	correlation := fmt.Sprintf("%s/%d/%s", wire.ProbeID, wire.UnixMillis, wire.Nonce)
	return wire, correlation, nil
}

func authenticatedProbeExchangeCount(frames []recordedNetworkFrame, credentials map[string]proto.ProbeCredential) (int, int, int, error) {
	requests := make(map[string]int)
	responses := make(map[string]int)
	requestCount := 0
	responseCount := 0
	for _, frame := range frames {
		if frame.transport != "udp" {
			continue
		}
		wire, correlation, err := decodeRecordedProbe(frame.payload, frame.direction)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("%s UDP frame: %w", frame.direction, err)
		}
		credential, ok := credentials[wire.ProbeID]
		if !ok {
			return 0, 0, 0, fmt.Errorf("probe credential %q was not observed on its authenticated control client", wire.ProbeID)
		}
		nonce, _ := hex.DecodeString(wire.Nonce)
		if frame.direction == "to_coordinator" {
			expected, err := p2p.EncodeProbeRequest(credential, time.UnixMilli(wire.UnixMillis), nonce)
			if err != nil || !bytes.Equal(expected, frame.payload) {
				return 0, 0, 0, fmt.Errorf("probe request %q failed credential HMAC validation: %v", correlation, err)
			}
		} else {
			response, err := p2p.DecodeProbeResponse(frame.payload, credential, time.UnixMilli(wire.UnixMillis))
			if err != nil || response.ProbeID != wire.ProbeID || response.UnixMillis != wire.UnixMillis || !bytes.Equal(response.Nonce, nonce) {
				return 0, 0, 0, fmt.Errorf("probe response %q failed credential HMAC validation: %v", correlation, err)
			}
		}
		if frame.direction == "to_coordinator" {
			requests[correlation]++
			requestCount++
		} else {
			responses[correlation]++
			responseCount++
		}
	}
	if requestCount == 0 {
		return 0, 0, 0, errors.New("no probe exchanges were recorded")
	}
	unmatched := 0
	for correlation, count := range requests {
		responseCount := responses[correlation]
		if count != 1 || responseCount > 1 {
			return 0, 0, 0, fmt.Errorf("probe correlation %q request=%d response=%d", correlation, count, responseCount)
		}
		if responseCount == 0 {
			unmatched++
		}
	}
	for correlation, count := range responses {
		if count != 1 || requests[correlation] != 1 {
			return 0, 0, 0, fmt.Errorf("probe response correlation %q request=%d response=%d", correlation, requests[correlation], count)
		}
	}
	return requestCount, responseCount, unmatched, nil
}

func captureProbeCredentials(peers map[string]*integrationPeer, credentials map[string]proto.ProbeCredential) {
	for _, peer := range peers {
		peer.runtime.control.refreshMu.Lock()
		credential := peer.runtime.control.credential
		peer.runtime.control.refreshMu.Unlock()
		if credential.ProbeID != "" {
			credentials[credential.ProbeID] = credential
		}
	}
}

func (r *controlPlaneRecorder) assertNoUserData(t *testing.T, packets [][]byte) {
	t.Helper()
	frames := r.snapshot()
	seen := map[string]bool{}
	streams := make(map[string][]byte)
	for _, frame := range frames {
		seen[frame.transport+":"+frame.direction] = true
		key := fmt.Sprintf("%s:%s:%d", frame.transport, frame.direction, frame.connection)
		streams[key] = append(streams[key], frame.payload...)
		if frame.transport == "udp" {
			if _, _, err := decodeRecordedProbe(frame.payload, frame.direction); err != nil {
				t.Fatalf("A-bound UDP %s was not a complete probe: %v (payload=%x)", frame.direction, err, frame.payload)
			}
		}
		for _, packet := range packets {
			if bytes.Contains(frame.payload, packet) {
				t.Fatalf("A-bound %s %s frame contained inner IPv4 payload %x", frame.transport, frame.direction, packet)
			}
		}
	}
	for _, required := range []string{"tcp:to_coordinator", "tcp:from_coordinator", "udp:to_coordinator", "udp:from_coordinator"} {
		if !seen[required] {
			t.Fatalf("A boundary did not record %s traffic", required)
		}
	}
	legacyPacketHeader := []byte{'M', 'S', 'H', '1', 1, proto.TypePacket}
	for key, stream := range streams {
		if bytes.Contains(stream, legacyPacketHeader) {
			t.Fatalf("A-bound recorded stream %s contained a legacy TypePacket frame", key)
		}
		for _, packet := range packets {
			if bytes.Contains(stream, packet) {
				t.Fatalf("A-bound recorded stream %s contained inner IPv4 payload %x", key, packet)
			}
		}
	}
}

type integrationProcess struct {
	name     string
	cancel   context.CancelFunc
	done     chan error
	stopped  chan struct{}
	stopOnce sync.Once
	errMu    sync.Mutex
	err      error
}

func startIntegrationProcess(name string, run func(context.Context) error) *integrationProcess {
	ctx, cancel := context.WithCancel(context.Background())
	p := &integrationProcess{name: name, cancel: cancel, done: make(chan error, 1), stopped: make(chan struct{})}
	go func() { p.done <- run(ctx) }()
	return p
}

func (p *integrationProcess) stop() error {
	p.stopOnce.Do(func() {
		p.cancel()
		select {
		case err := <-p.done:
			p.errMu.Lock()
			p.err = err
			p.errMu.Unlock()
		case <-time.After(5 * time.Second):
			p.errMu.Lock()
			p.err = fmt.Errorf("%s did not stop within five seconds", p.name)
			p.errMu.Unlock()
		}
		close(p.stopped)
	})
	<-p.stopped
	p.errMu.Lock()
	defer p.errMu.Unlock()
	return p.err
}

type integrationPeer struct {
	id      string
	agent   *Agent
	runtime *peerRuntime
	device  *channelTUNDevice
	process *integrationProcess
}

type pureP2PHarness struct {
	t           *testing.T
	dir         string
	certDir     string
	backendAddr string
	recorder    *controlPlaneRecorder
	ledger      payloadLedger
	nodes       []map[string]any
	nodeByID    map[string]map[string]any
	peers       map[string]*integrationPeer
	coordinator *integrationProcess
	currentA    *Agent
	allA        []*Agent
	closeOnce   sync.Once
}

func newPureP2PHarness(t *testing.T) *pureP2PHarness {
	return newPureP2PHarnessWithPeers(t, []string{"B", "C", "D", "E", "M"})
}

func newPureP2PHarnessWithPeers(t *testing.T, peerIDs []string) *pureP2PHarness {
	t.Helper()
	dir := t.TempDir()
	certDir := filepath.Join(dir, "certs")
	if _, err := certutil.InitCA(certutil.CAOptions{OutDir: certDir}); err != nil {
		t.Fatal(err)
	}
	h := &pureP2PHarness{
		t: t, dir: dir, certDir: certDir, backendAddr: reserveDualProtocolAddress(t),
		nodeByID: make(map[string]map[string]any), peers: make(map[string]*integrationPeer),
	}
	identities := make([]string, 0, len(peerIDs)+2)
	identities = append(identities, "coordinator")
	identities = append(identities, peerIDs...)
	identities = append(identities, "legacy")
	seenIdentities := make(map[string]bool, len(identities))
	for _, id := range identities {
		if id == "" || seenIdentities[id] {
			t.Fatalf("invalid duplicate integration identity %q", id)
		}
		seenIdentities[id] = true
		if _, err := certutil.Issue(certutil.IssueOptions{
			OutDir: certDir, Name: id, CAPath: filepath.Join(certDir, "ca.pem"),
			CAKeyPath: filepath.Join(certDir, "ca-key.pem"), IPAddrs: []string{"127.0.0.1"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for index, id := range peerIDs {
		if index >= 253 {
			t.Fatal("integration harness supports at most 253 peers")
		}
		_, fingerprint := certInfoFromFile(filepath.Join(certDir, id+".pem"))
		node := map[string]any{
			"node_id": id, "virtual_ip": fmt.Sprintf("10.77.0.%d", index+2),
			"cert_fingerprint": fingerprint, "routes": []string{},
		}
		h.nodes = append(h.nodes, node)
		h.nodeByID[id] = node
	}
	h.saveRegistry()
	h.recorder = newControlPlaneRecorder(t, h.backendAddr)
	h.startCoordinator()
	t.Cleanup(h.Close)
	return h
}

func (h *pureP2PHarness) saveRegistry() {
	h.t.Helper()
	configDir := filepath.Join(h.dir, "configs")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		h.t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"nodes": h.nodes})
	if err != nil {
		h.t.Fatal(err)
	}
	path := filepath.Join(configDir, "devices.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o600); err != nil {
		h.t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		h.t.Fatal(err)
	}
}

func (h *pureP2PHarness) startCoordinator() {
	h.t.Helper()
	if h.coordinator != nil {
		h.t.Fatal("coordinator already running")
	}
	cfg := coordinatorConfigForTest()
	cfg.Listen = h.backendAddr
	cfg.Transport.Listen = h.backendAddr
	cfg.CAFile = filepath.Join(h.certDir, "ca.pem")
	cfg.CertFile = filepath.Join(h.certDir, "coordinator.pem")
	cfg.KeyFile = filepath.Join(h.certDir, "coordinator-key.pem")
	a, err := New(&cfg, discardLogger(), WithBaseDir(h.dir))
	if err != nil {
		h.t.Fatal(err)
	}
	process := startIntegrationProcess("coordinator", a.Run)
	h.currentA = a
	h.allA = append(h.allA, a)
	h.coordinator = process
	integrationEventually(h.t, integrationWait, func() (bool, string) {
		select {
		case err := <-process.done:
			process.done <- err
			return false, fmt.Sprintf("coordinator exited early: %v", err)
		default:
		}
		status := a.status.snapshot()
		return status.NetworkState == networkstate.Connected, fmt.Sprintf("coordinator state=%s", status.NetworkState)
	})
}

func (h *pureP2PHarness) stopCoordinator() {
	h.t.Helper()
	if h.coordinator == nil {
		return
	}
	if err := h.coordinator.stop(); err != nil && !errors.Is(err, context.Canceled) {
		h.t.Fatalf("stop coordinator: %v", err)
	}
	h.coordinator = nil
	h.currentA = nil
}

func (h *pureP2PHarness) startPeers(ids ...string) {
	h.t.Helper()
	for _, id := range ids {
		h.startPeer(id)
	}
	for _, id := range ids {
		peer := h.peers[id]
		integrationEventually(h.t, integrationWait, func() (bool, string) {
			status := peer.agent.status.snapshot()
			return status.CoordinatorState == networkstate.Connected,
				fmt.Sprintf("%s coordinator state=%s", id, status.CoordinatorState)
		})
	}
	for _, id := range ids {
		peer := h.peers[id]
		integrationEventually(h.t, integrationWait, func() (bool, string) {
			for _, other := range ids {
				if id == other {
					continue
				}
				if _, ok := peer.runtime.member(other); !ok {
					return false, fmt.Sprintf("%s has not learned %s", id, other)
				}
			}
			return true, ""
		})
	}
}

func (h *pureP2PHarness) startPeer(id string) {
	h.t.Helper()
	if h.peers[id] != nil {
		h.t.Fatalf("peer %s already started", id)
	}
	node := h.nodeByID[id]
	if node == nil {
		h.t.Fatalf("unknown integration peer %s", id)
	}
	virtualIP := node["virtual_ip"].(string)
	cfg := &config.Config{
		Version: config.ConfigVersion, NodeID: id, Mode: "spoke",
		Transport: config.TransportConfig{Protocol: config.ControlProtocolV2, Connect: h.recorder.addr, ServerName: "127.0.0.1"},
		Connect:   h.recorder.addr, ServerName: "127.0.0.1",
		CAFile: filepath.Join(h.certDir, "ca.pem"), CertFile: filepath.Join(h.certDir, id+".pem"),
		KeyFile: filepath.Join(h.certDir, id+"-key.pem"), VirtualIP: virtualIP, MTU: 1280,
		Device: config.DeviceConfig{Type: "null", Name: "integration-" + id},
		P2P:    config.P2PConfig{Protocol: config.P2PProtocolQUICUDPv1, Listen: "127.0.0.1:0"},
	}
	for _, route := range node["routes"].([]string) {
		cfg.Routes = append(cfg.Routes, config.Route{CIDR: route})
	}
	device := newChannelTUNDevice("integration-"+id, 1280, 4096, h.ledger.add)
	a, err := New(cfg, discardLogger(), WithDevice(device))
	if err != nil {
		h.t.Fatal(err)
	}
	integrationConfigurePeerSessionTimings(a, integrationHeartbeatInterval, integrationHeartbeatTimeout, integrationDialTimeout)
	peer := &integrationPeer{id: id, agent: a, device: device}
	peer.process = startIntegrationProcess("peer "+id, a.Run)
	integrationEventually(h.t, integrationWait, func() (bool, string) {
		peer.runtime = integrationCurrentPeerRuntime(a)
		return peer.runtime != nil, fmt.Sprintf("%s production peer runtime has not started", id)
	})
	h.peers[id] = peer
}

func integrationConfigurePeerSessionTimings(a *Agent, heartbeatInterval, heartbeatTimeout, dialTimeout time.Duration) {
	a.peerRuntimeMu.Lock()
	defer a.peerRuntimeMu.Unlock()
	a.peerTimings = peerSessionTimings{
		heartbeatInterval: heartbeatInterval,
		heartbeatTimeout:  heartbeatTimeout,
		dialTimeout:       dialTimeout,
	}
}

func integrationCurrentPeerRuntime(a *Agent) *peerRuntime {
	a.peerRuntimeMu.RLock()
	defer a.peerRuntimeMu.RUnlock()
	return a.peerRuntime
}

func (h *pureP2PHarness) disablePeer(id string) {
	h.t.Helper()
	node := h.nodeByID[id]
	if node == nil {
		h.t.Fatalf("unknown peer %s", id)
	}
	node["disabled"] = true
	h.saveRegistry()
}

func (h *pureP2PHarness) Close() {
	h.closeOnce.Do(func() {
		for _, peer := range h.peers {
			if err := peer.process.stop(); err != nil && !errors.Is(err, context.Canceled) {
				var permanent *controlCompatibilityError
				if !errors.As(err, &permanent) {
					h.t.Errorf("stop peer %s: %v", peer.id, err)
				}
			}
		}
		if h.coordinator != nil {
			if err := h.coordinator.stop(); err != nil && !errors.Is(err, context.Canceled) {
				h.t.Errorf("stop coordinator: %v", err)
			}
		}
		h.recorder.Close()
	})
}

func integrationEventually(t *testing.T, timeout time.Duration, condition func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := "condition was false"
	for time.Now().Before(deadline) {
		if ok, detail := condition(); ok {
			return
		} else if detail != "" {
			last = detail
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition did not converge within %v: %s", timeout, last)
}

func isDirectSnapshot(snapshot p2p.SessionSnapshot) bool {
	return snapshot.State == p2p.PathStateLANDirect || snapshot.State == p2p.PathStatePublicDirect
}

func waitDirectPair(t *testing.T, left *integrationPeer, rightID string, right *integrationPeer, leftID string) (p2p.SessionSnapshot, p2p.SessionSnapshot) {
	t.Helper()
	var leftSnapshot, rightSnapshot p2p.SessionSnapshot
	integrationEventually(t, integrationWait, func() (bool, string) {
		var leftOK, rightOK bool
		leftSnapshot, leftOK = left.runtime.sessions.Snapshot(rightID)
		rightSnapshot, rightOK = right.runtime.sessions.Snapshot(leftID)
		ready := leftOK && rightOK && isDirectSnapshot(leftSnapshot) && isDirectSnapshot(rightSnapshot) &&
			leftSnapshot.SessionID != "" && leftSnapshot.SessionID == rightSnapshot.SessionID &&
			leftSnapshot.Generation != 0 && leftSnapshot.Generation == rightSnapshot.Generation
		return ready, fmt.Sprintf("%s=%+v %s=%+v", left.id, leftSnapshot, right.id, rightSnapshot)
	})
	return leftSnapshot, rightSnapshot
}

func waitPairState(t *testing.T, left *integrationPeer, rightID string, right *integrationPeer, leftID string, state p2p.PathState) (p2p.SessionSnapshot, p2p.SessionSnapshot) {
	t.Helper()
	var leftSnapshot, rightSnapshot p2p.SessionSnapshot
	integrationEventually(t, integrationWait, func() (bool, string) {
		leftSnapshot, _ = left.runtime.sessions.Snapshot(rightID)
		rightSnapshot, _ = right.runtime.sessions.Snapshot(leftID)
		return leftSnapshot.State == state && rightSnapshot.State == state,
			fmt.Sprintf("%s=%+v %s=%+v", left.id, leftSnapshot, right.id, rightSnapshot)
	})
	return leftSnapshot, rightSnapshot
}

func injectAndExpect(t *testing.T, from, to *integrationPeer, packet []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := from.device.inject(ctx, packet); err != nil {
		t.Fatalf("inject %s -> %s: %v", from.id, to.id, err)
	}
	if err := receiveExactPacket(ctx, to.device, packet); err != nil {
		t.Fatalf("deliver %s -> %s: %v", from.id, to.id, err)
	}
}

func sequenceIPv4Packet(source, destination byte, sequence uint32) []byte {
	packet := []byte{
		0x45, 0x00, 0x00, 0x18, 0x20, 0x01, 0x00, 0x00, 0x40, 0x11, 0x00, 0x00,
		10, 77, 0, source, 10, 77, 0, destination, 0, 0, 0, 0,
	}
	binary.BigEndian.PutUint32(packet[20:24], sequence)
	return packet
}

type continuousPairProof struct {
	cancel context.CancelFunc
	done   chan error
	count  atomic.Uint64
}

func startContinuousPairProof(h *pureP2PHarness, left, right *integrationPeer, baselineLeft, baselineRight p2p.SessionSnapshot) *continuousPairProof {
	ctx, cancel := context.WithCancel(context.Background())
	proof := &continuousPairProof{cancel: cancel, done: make(chan error, 1)}
	go func() {
		for sequence := uint32(1); ; sequence++ {
			select {
			case <-ctx.Done():
				proof.done <- nil
				return
			default:
			}
			leftToRight := sequenceIPv4Packet(4, 5, sequence)
			rightToLeft := sequenceIPv4Packet(5, 4, sequence)
			// Once a sequence starts, finish both directions before honoring stop.
			// Otherwise cancellation can leave a successfully injected packet in
			// the device queue and make the next assertion observe a false reorder.
			iteration, iterationCancel := context.WithTimeout(context.Background(), 2*time.Second)
			if err := left.device.inject(iteration, leftToRight); err == nil {
				err = receiveExactPacket(iteration, right.device, leftToRight)
				if err == nil {
					err = right.device.inject(iteration, rightToLeft)
				}
				if err == nil {
					err = receiveExactPacket(iteration, left.device, rightToLeft)
				}
				if err != nil {
					iterationCancel()
					proof.done <- fmt.Errorf("D/E sequence %d: %w", sequence, err)
					return
				}
			} else {
				iterationCancel()
				proof.done <- fmt.Errorf("inject D/E sequence %d: %w", sequence, err)
				return
			}
			iterationCancel()
			leftSnapshot, leftOK := left.runtime.sessions.Snapshot(right.id)
			rightSnapshot, rightOK := right.runtime.sessions.Snapshot(left.id)
			if !leftOK || !rightOK || !isDirectSnapshot(leftSnapshot) || !isDirectSnapshot(rightSnapshot) ||
				leftSnapshot.SessionID != baselineLeft.SessionID || rightSnapshot.SessionID != baselineRight.SessionID ||
				leftSnapshot.Generation != baselineLeft.Generation || rightSnapshot.Generation != baselineRight.Generation {
				proof.done <- fmt.Errorf("D/E session regressed at sequence %d: D=%+v E=%+v", sequence, leftSnapshot, rightSnapshot)
				return
			}
			proof.count.Store(uint64(sequence))
			timer := time.NewTimer(10 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				proof.done <- nil
				return
			case <-timer.C:
			}
		}
	}()
	return proof
}

func (p *continuousPairProof) stop(t *testing.T, minimum uint64) {
	t.Helper()
	p.cancel()
	select {
	case err := <-p.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("continuous D/E proof did not stop")
	}
	if count := p.count.Load(); count < minimum {
		t.Fatalf("continuous D/E proof delivered %d sequences, want at least %d", count, minimum)
	}
}

func interruptRealPeerSession(t *testing.T, manager *p2p.SessionManager, peerID string) {
	t.Helper()
	if err := manager.InterruptPeer(peerID, "direct_unreachable_no_relay"); err != nil {
		t.Fatalf("interrupt active QUIC session with %s: %v", peerID, err)
	}
}

func equalDirectIdentity(got, want p2p.SessionSnapshot) bool {
	return got.SessionID == want.SessionID && got.Generation == want.Generation && got.State == want.State && got.PathType == want.PathType
}

func assertNoCoordinatorDataMetrics(t *testing.T, coordinators []*Agent) {
	t.Helper()
	for index, coordinator := range coordinators {
		if coordinator.dev != nil {
			t.Fatalf("coordinator run %d acquired a packet device", index+1)
		}
		status := coordinator.status.snapshot()
		if status.CoordinatorMetrics.TypePacketViolations != 0 {
			t.Fatalf("coordinator run %d saw %d TypePacket frames", index+1, status.CoordinatorMetrics.TypePacketViolations)
		}
		encoded, err := json.Marshal(status.CoordinatorMetrics)
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(encoded))
		if strings.Contains(lower, "user_byte") || strings.Contains(lower, "packet_byte") || strings.Contains(lower, "payload_byte") {
			t.Fatalf("coordinator exposed a user-payload byte metric: %s", encoded)
		}
	}
}

func assertNoRelayRuntime(t *testing.T, h *pureP2PHarness) {
	t.Helper()
	for _, peer := range h.peers {
		encoded, err := json.Marshal(peer.agent.cfg)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(string(encoded)), "relay") {
			t.Fatalf("peer %s received Relay configuration: %s", peer.id, encoded)
		}
		for _, other := range h.peers {
			if peer == other {
				continue
			}
			if snapshot, ok := peer.runtime.sessions.Snapshot(other.id); ok && isDirectSnapshot(snapshot) &&
				snapshot.PathType != p2p.PathTypeLANDirect && snapshot.PathType != p2p.PathTypePublicDirect {
				t.Fatalf("peer %s used a non-direct path to %s: %+v", peer.id, other.id, snapshot)
			}
		}
	}
}

func TestLANPeersEstablishEveryPairWhenCoordinatorUDPIsUnavailable(t *testing.T) {
	peerIDs := []string{"lan-01", "lan-02", "lan-03", "lan-04", "lan-05", "lan-06", "lan-07", "lan-08"}
	h := newPureP2PHarnessWithPeers(t, peerIDs)
	h.recorder.dropProbes.Store(true)
	h.startPeers(peerIDs...)
	integrationEventually(t, integrationWait, func() (bool, string) {
		direct := 0
		for _, id := range peerIDs {
			for _, other := range peerIDs {
				if id == other {
					continue
				}
				snapshot, ok := h.peers[id].runtime.sessions.Snapshot(other)
				if ok && isDirectSnapshot(snapshot) {
					direct++
				}
			}
		}
		return direct == 56, fmt.Sprintf("direct=%d/56 coordinator=%+v", direct, h.currentA.status.snapshot().CoordinatorMetrics)
	})
	for _, peer := range h.peers {
		if packets := peer.device.drain(); len(packets) != 0 {
			t.Fatalf("idle peer %s received business packets", peer.id)
		}
	}
	if packets := h.ledger.snapshot(); len(packets) != 0 {
		t.Fatalf("LAN connection required %d business packets", len(packets))
	}
	if metrics := h.currentA.status.snapshot().CoordinatorMetrics; metrics.ProbeSuccesses != 0 || metrics.NegotiationsTimedOut != 0 {
		t.Fatalf("LAN sessions relied on coordinator UDP or exceeded phase deadlines: %+v", metrics)
	}
	assertNoCoordinatorDataMetrics(t, h.allA)
	assertNoRelayRuntime(t, h)
}

func TestTwentyIdlePeersEstablishEveryDirectPairWithoutTraffic(t *testing.T) {
	const peerCount = 20
	peerIDs := make([]string, peerCount)
	for index := range peerIDs {
		peerIDs[index] = fmt.Sprintf("idle-%02d", index+1)
	}
	h := newPureP2PHarnessWithPeers(t, peerIDs)
	h.startPeers(peerIDs...)
	integrationEventually(t, 20*time.Second, func() (bool, string) {
		for _, id := range peerIDs {
			peer := h.peers[id]
			for _, other := range peerIDs {
				if id == other {
					continue
				}
				snapshot, ok := peer.runtime.sessions.Snapshot(other)
				if !ok || !isDirectSnapshot(snapshot) {
					return false, fmt.Sprintf("idle pair %s/%s is not direct: %+v; metrics=%+v", id, other, snapshot, h.currentA.status.snapshot().CoordinatorMetrics)
				}
			}
		}
		return true, ""
	})
	metricsBefore := h.currentA.status.snapshot().CoordinatorMetrics
	before := make(map[string]proto.ActiveSessions)
	for _, id := range peerIDs {
		peer := h.peers[id]
		before[id] = peer.runtime.sessions.ActiveSessions()
		if len(before[id].Sessions) != peerCount-1 {
			t.Fatalf("idle peer %s has %d active peers", id, len(before[id].Sessions))
		}
		// Periodic coordinator contact must not replace healthy pair sessions.
		if err := peer.runtime.control.refresh(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		peer.runtime.connectOnlineMembers()
		// A second refresh is an ordered control barrier after any request.
		if err := peer.runtime.control.refresh(context.Background(), false); err != nil {
			t.Fatal(err)
		}
	}
	integrationEventually(t, time.Second, func() (bool, string) {
		metrics := h.currentA.status.snapshot().CoordinatorMetrics
		return metrics.CandidateRefreshes >= metricsBefore.CandidateRefreshes+2*peerCount,
			fmt.Sprintf("coordinator has not processed all refreshes: %+v", metrics)
	})
	if metrics := h.currentA.status.snapshot().CoordinatorMetrics; metrics.NegotiationsRequested != metricsBefore.NegotiationsRequested {
		t.Fatalf("refresh requested replacement sessions for healthy pairs: before=%+v after=%+v", metricsBefore, metrics)
	}
	for _, id := range peerIDs {
		peer := h.peers[id]
		active := peer.runtime.sessions.ActiveSessions().Sessions
		for index, original := range before[id].Sessions {
			if len(active) != peerCount-1 || active[index] != original {
				t.Fatalf("membership refresh changed healthy pair for %s: before=%+v after=%+v", id, before[id], active)
			}
		}
		for _, member := range peer.agent.status.snapshot().Peers {
			if member.Status != "online" || member.Session == nil || member.PathType == "" {
				t.Fatalf("idle peer %s did not display %s online: %+v", id, member.NodeID, member)
			}
		}
		if packets := peer.device.drain(); len(packets) != 0 {
			t.Fatalf("idle peer %s received %d unexpected TUN packets", id, len(packets))
		}
	}
	if packets := h.ledger.snapshot(); len(packets) != 0 {
		t.Fatalf("idle peer run injected %d business packets", len(packets))
	}
	credentials := make(map[string]proto.ProbeCredential)
	captureProbeCredentials(h.peers, credentials)
	integrationEventually(t, time.Second, func() (bool, string) {
		metrics := h.currentA.status.snapshot().CoordinatorMetrics
		requests, responses, unmatched, err := authenticatedProbeExchangeCount(h.recorder.snapshot(), credentials)
		return err == nil && uint64(responses) == metrics.ProbeSuccesses && uint64(unmatched) == metrics.ProbeFailures && uint64(requests) == metrics.ProbeSuccesses+metrics.ProbeFailures,
			fmt.Sprintf("authenticated probe requests=%d responses=%d unmatched=%d metrics=%+v error=%v", requests, responses, unmatched, metrics, err)
	})
	assertNoCoordinatorDataMetrics(t, h.allA)
	h.recorder.assertNoUserData(t, nil)
	assertNoRelayRuntime(t, h)
}
func TestPureP2PDataPlaneFaultMatrix(t *testing.T) {
	h := newPureP2PHarness(t)
	h.startPeers("B", "C", "D", "E")
	b, c, d, e := h.peers["B"], h.peers["C"], h.peers["D"], h.peers["E"]

	injectAndExpect(t, b, c, literalBToC)
	beforeB, beforeC := waitDirectPair(t, b, "C", c, "B")
	injectAndExpect(t, c, b, literalCToB)
	if beforeB.Generation != beforeC.Generation || beforeB.SessionID != beforeC.SessionID {
		t.Fatalf("B/C did not share one generation: B=%+v C=%+v", beforeB, beforeC)
	}

	// A packet authenticated on B's session but claiming D's inner source must
	// never be delivered to C's TUN (AC-13).
	beforeDrop, _ := c.runtime.sessions.Snapshot("B")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	if err := b.device.inject(ctx, literalSpoofDToC); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	assertChannelQuiet(t, c.device, 4*integrationHeartbeatInterval)
	integrationEventually(t, time.Second, func() (bool, string) {
		snapshot, _ := c.runtime.sessions.Snapshot("B")
		return snapshot.InboundDropped > beforeDrop.InboundDropped, fmt.Sprintf("C snapshot=%+v", snapshot)
	})

	initialDEPacket := sequenceIPv4Packet(4, 5, 0)
	injectAndExpect(t, d, e, initialDEPacket)
	beforeD, beforeE := waitDirectPair(t, d, "E", e, "D")
	_ = d.device.drain()
	_ = e.device.drain()
	continuous := startContinuousPairProof(h, d, e, beforeD, beforeE)

	h.stopCoordinator()
	for _, peer := range []*integrationPeer{b, c, d, e} {
		integrationEventually(t, integrationWait, func() (bool, string) {
			state := peer.agent.status.snapshot().CoordinatorState
			return state == networkstate.Reconnecting, fmt.Sprintf("%s coordinator state=%s", peer.id, state)
		})
	}
	time.Sleep(4*integrationHeartbeatInterval + 30*time.Millisecond)
	injectAndExpect(t, b, c, literalBToC)
	injectAndExpect(t, c, b, literalCToB)
	afterOutageB, afterOutageC := waitDirectPair(t, b, "C", c, "B")
	if !equalDirectIdentity(afterOutageB, beforeB) || !equalDirectIdentity(afterOutageC, beforeC) {
		t.Fatalf("A outage changed B/C direct identity: before B=%+v C=%+v after B=%+v C=%+v", beforeB, beforeC, afterOutageB, afterOutageC)
	}
	if !afterOutageB.LastHeartbeat.After(beforeB.LastHeartbeat) || !afterOutageC.LastHeartbeat.After(beforeC.LastHeartbeat) {
		t.Fatalf("B/C did not cross peer heartbeat intervals while A was offline: before B=%v C=%v after B=%v C=%v",
			beforeB.LastHeartbeat, beforeC.LastHeartbeat, afterOutageB.LastHeartbeat, afterOutageC.LastHeartbeat)
	}

	interruptRealPeerSession(t, b.runtime.sessions, "C")
	waitingB, waitingC := waitPairState(t, b, "C", c, "B", p2p.PathStateWaitingCoordinator)
	if waitingB.Generation != beforeB.Generation || waitingC.Generation != beforeC.Generation ||
		waitingB.SessionID != beforeB.SessionID || waitingC.SessionID != beforeC.SessionID {
		t.Fatalf("offline direct failure invented a generation: B=%+v C=%+v", waitingB, waitingC)
	}
	for _, entry := range []struct {
		manager *p2p.SessionManager
		start   proto.SessionStart
	}{{b.runtime.sessions, proto.SessionStart{SessionID: beforeB.SessionID, Generation: beforeB.Generation}},
		{c.runtime.sessions, proto.SessionStart{SessionID: beforeC.SessionID, Generation: beforeC.Generation}}} {
		if err := entry.manager.StartOffer(entry.start); !errors.Is(err, p2p.ErrSessionOfferRejected) {
			t.Fatalf("consumed authorization was reusable: %v", err)
		}
		for _, active := range entry.manager.ActiveSessions().Sessions {
			if active.SessionID == entry.start.SessionID {
				t.Fatalf("failed B/C retained an active authorization: %+v", active)
			}
		}
	}
	waitingPacket := sequenceIPv4Packet(2, 3, 0xfeed)
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	if err := b.device.inject(ctx, waitingPacket); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	assertChannelQuiet(t, c.device, 4*integrationHeartbeatInterval)
	time.Sleep(3 * integrationHeartbeatInterval)
	stillWaitingB, _ := b.runtime.sessions.Snapshot("C")
	stillWaitingC, _ := c.runtime.sessions.Snapshot("B")
	if stillWaitingB.State != p2p.PathStateWaitingCoordinator || stillWaitingC.State != p2p.PathStateWaitingCoordinator ||
		stillWaitingB.Generation != beforeB.Generation || stillWaitingC.Generation != beforeC.Generation {
		t.Fatalf("B/C self-authorized while A was offline: B=%+v C=%+v", stillWaitingB, stillWaitingC)
	}

	h.startCoordinator()
	afterRestartB, afterRestartC := waitDirectPair(t, b, "C", c, "B")
	if afterRestartB.Generation <= beforeB.Generation || afterRestartC.Generation <= beforeC.Generation ||
		afterRestartB.SessionID == beforeB.SessionID || afterRestartC.SessionID == beforeC.SessionID {
		t.Fatalf("coordinator return did not grant a fresh generation: before B=%+v C=%+v after B=%+v C=%+v",
			beforeB, beforeC, afterRestartB, afterRestartC)
	}
	// The packet queued while A was unavailable is authorized only after the
	// fresh generation reaches direct; require that exact deferred delivery.
	assertChannelPacket(t, c.device, waitingPacket, 3*time.Second)
	_ = b.device.drain()
	injectAndExpect(t, b, c, literalBToC)
	injectAndExpect(t, c, b, literalCToB)
	continuous.stop(t, 10)
	afterD, afterE := waitDirectPair(t, d, "E", e, "D")
	if !equalDirectIdentity(afterD, beforeD) || !equalDirectIdentity(afterE, beforeE) ||
		afterD.BytesSent < beforeD.BytesSent || afterD.BytesReceived < beforeD.BytesReceived ||
		afterE.BytesSent < beforeE.BytesSent || afterE.BytesReceived < beforeE.BytesReceived {
		t.Fatalf("D/E regressed during B/C recovery: before D=%+v E=%+v after D=%+v E=%+v", beforeD, beforeE, afterD, afterE)
	}

	assertNoCoordinatorDataMetrics(t, h.allA)
	h.recorder.assertNoUserData(t, h.ledger.snapshot())
	assertNoRelayRuntime(t, h)
}

func TestConcurrentBidirectionalFirstPacketCreatesOnePairGeneration(t *testing.T) {
	h := newPureP2PHarness(t)
	h.startPeers("B", "C")
	b, c := h.peers["B"], h.peers["C"]
	barrier := make(chan struct{})
	errorsOut := make(chan error, 2)
	for _, entry := range []struct {
		device *channelTUNDevice
		packet []byte
	}{{b.device, literalBToC}, {c.device, literalCToB}} {
		entry := entry
		go func() {
			<-barrier
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			errorsOut <- entry.device.inject(ctx, entry.packet)
		}()
	}
	close(barrier)
	for range 2 {
		if err := <-errorsOut; err != nil {
			t.Fatal(err)
		}
	}
	assertChannelPacket(t, c.device, literalBToC, 5*time.Second)
	assertChannelPacket(t, b.device, literalCToB, 5*time.Second)
	bSnapshot, cSnapshot := waitDirectPair(t, b, "C", c, "B")
	if bSnapshot.SessionID != cSnapshot.SessionID || bSnapshot.Generation != cSnapshot.Generation {
		t.Fatalf("concurrent first packets created different sessions: B=%+v C=%+v", bSnapshot, cSnapshot)
	}
	integrationEventually(t, 3*time.Second, func() (bool, string) {
		metrics := h.currentA.status.snapshot().CoordinatorMetrics
		return metrics.NegotiationsSucceeded == 1,
			fmt.Sprintf("coordinator metrics=%+v", metrics)
	})
	metrics := h.currentA.status.snapshot().CoordinatorMetrics
	if metrics.NegotiationsPrepared != 1 || metrics.NegotiationsOffered != 1 || metrics.NegotiationsStarted != 1 {
		t.Fatalf("concurrent first packets created more than one pair generation: %+v", metrics)
	}
	assertNoCoordinatorDataMetrics(t, h.allA)
	h.recorder.assertNoUserData(t, h.ledger.snapshot())
	assertNoRelayRuntime(t, h)
}

func TestRevocationAfterCoordinatorReturnClosesOnlyAffectedPair(t *testing.T) {
	h := newPureP2PHarness(t)
	h.startPeers("B", "C", "D", "E")
	b, c, d, e := h.peers["B"], h.peers["C"], h.peers["D"], h.peers["E"]
	injectAndExpect(t, b, c, literalBToC)
	beforeB, beforeC := waitDirectPair(t, b, "C", c, "B")
	injectAndExpect(t, d, e, sequenceIPv4Packet(4, 5, 0))
	beforeD, beforeE := waitDirectPair(t, d, "E", e, "D")
	_ = d.device.drain()
	_ = e.device.drain()
	continuous := startContinuousPairProof(h, d, e, beforeD, beforeE)

	h.stopCoordinator()
	for _, peer := range []*integrationPeer{b, c, d, e} {
		integrationEventually(t, integrationWait, func() (bool, string) {
			state := peer.agent.status.snapshot().CoordinatorState
			return state == networkstate.Reconnecting, fmt.Sprintf("%s coordinator state=%s", peer.id, state)
		})
	}
	h.disablePeer("C")
	time.Sleep(4*integrationHeartbeatInterval + 30*time.Millisecond)
	injectAndExpect(t, b, c, literalBToC)
	offlineB, offlineC := waitDirectPair(t, b, "C", c, "B")
	if !equalDirectIdentity(offlineB, beforeB) || !equalDirectIdentity(offlineC, beforeC) {
		t.Fatalf("offline revocation incorrectly propagated before A returned: B=%+v C=%+v", offlineB, offlineC)
	}

	h.startCoordinator()
	integrationEventually(t, integrationWait, func() (bool, string) {
		snapshot, ok := b.runtime.sessions.Snapshot("C")
		return ok && snapshot.State == p2p.PathStateClosed && snapshot.ErrorCode == "peer_revoked",
			fmt.Sprintf("B/C after revocation=%+v", snapshot)
	})
	integrationEventually(t, integrationWait, func() (bool, string) {
		snapshot, ok := c.runtime.sessions.Snapshot("B")
		return ok && !isDirectSnapshot(snapshot), fmt.Sprintf("revoked C still direct to B: %+v", snapshot)
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	if err := b.device.inject(ctx, sequenceIPv4Packet(2, 3, 0xdead)); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	assertChannelNoPacketOrClosed(t, c.device, 4*integrationHeartbeatInterval)
	continuous.stop(t, 10)
	afterD, afterE := waitDirectPair(t, d, "E", e, "D")
	if !equalDirectIdentity(afterD, beforeD) || !equalDirectIdentity(afterE, beforeE) {
		t.Fatalf("C revocation disturbed D/E: before D=%+v E=%+v after D=%+v E=%+v", beforeD, beforeE, afterD, afterE)
	}
	injectAndExpect(t, d, e, sequenceIPv4Packet(4, 5, 0xf00d))
	if snapshot, _ := b.runtime.sessions.Snapshot("C"); isDirectSnapshot(snapshot) {
		t.Fatalf("revoked B/C remained direct: %+v", snapshot)
	}
	assertNoCoordinatorDataMetrics(t, h.allA)
	h.recorder.assertNoUserData(t, h.ledger.snapshot())
	assertNoRelayRuntime(t, h)
}

func TestRevocationAfterCoordinatorReturnBoundsForgedRelationshipEvidence(t *testing.T) {
	f := newCoordinatorFixture(t, true)
	f.nodes[1]["disabled"] = true // C retains a CA-valid certificate but is revoked.
	f.saveRegistry()
	for batch := 0; batch < 4; batch++ {
		client := f.dial("C")
		client.send(proto.ControlTypeClientHello, f.hello("C"))
		client.send(proto.ControlTypeCandidateUpdate, proto.CandidateUpdate{Revision: 1})
		sessions := make([]proto.ActiveSession, 1024)
		for index := range sessions {
			sequence := batch*len(sessions) + index
			sessions[index] = proto.ActiveSession{
				SessionID: fmt.Sprintf("forged-session-%04d", sequence), Generation: uint64(sequence + 1),
				PeerNodeID: fmt.Sprintf("forged-peer-%04d", sequence), PathType: "quic_udp",
			}
		}
		client.send(proto.ControlTypeActiveSessions, proto.ActiveSessions{Sessions: sessions})
		controlError := decodeCoordinatorBody[proto.ControlError](t, client.want(proto.ControlTypeError))
		if controlError.Code != "identity_mismatch" {
			t.Fatalf("revoked C error=%+v", controlError)
		}
		client.closed()
	}
	f.coordinator.mu.Lock()
	pendingClaims := len(f.coordinator.revokedStartupClaims[sha256.Sum256([]byte("C"))])
	totalClaims := f.coordinator.revokedStartupClaimCount
	retainedRelationships := len(f.coordinator.relationships)
	f.coordinator.mu.Unlock()
	if pendingClaims != maxRevokedStartupClaimsPerNode || totalClaims != maxRevokedStartupClaimsPerNode {
		t.Fatalf("revoked C claims were not bounded across reconnects: per-node=%d total=%d", pendingClaims, totalClaims)
	}
	if retainedRelationships != 0 {
		t.Fatalf("unvalidated startup claims consumed %d global relationships", retainedRelationships)
	}

	// The forged evidence must not consume the global relationship budget or
	// prevent an unrelated pair of currently admitted registry members.
	f.nodes[1]["disabled"] = false
	f.saveRegistry()
	b, _ := f.connect("B")
	c, _ := f.connect("C")
	started := coordinatorCompleteSession(t, b, c)
	if started.SessionID == "" || started.Generation == 0 {
		t.Fatalf("legitimate B/C authorization failed after forged evidence: %+v", started)
	}
}

func TestRevocationAfterCoordinatorReturnDeduplicatesSurvivorEvidence(t *testing.T) {
	f := newCoordinatorFixture(t, true)
	b, _ := f.connect("B")
	f.nodes[1]["disabled"] = true
	f.saveRegistry()

	repeated := make([]proto.ActiveSession, 1024)
	for index := range repeated {
		repeated[index] = proto.ActiveSession{
			SessionID: "same-revoked-session", Generation: 1,
			PeerNodeID: "B", PathType: "quic_udp",
		}
	}
	for reconnect := 0; reconnect < 3; reconnect++ {
		client := f.dial("C")
		client.send(proto.ControlTypeClientHello, f.hello("C"))
		client.send(proto.ControlTypeCandidateUpdate, proto.CandidateUpdate{Revision: 1})
		client.send(proto.ControlTypeActiveSessions, proto.ActiveSessions{Sessions: repeated})
		controlError := decodeCoordinatorBody[proto.ControlError](t, client.want(proto.ControlTypeError))
		if controlError.Code != "identity_mismatch" {
			t.Fatalf("revoked C error=%+v", controlError)
		}
		client.closed()
	}

	if got := decodeCoordinatorBody[proto.DisconnectPeer](t, b.want(proto.ControlTypeDisconnectPeer)); got.NodeID != "C" {
		t.Fatalf("wrong survivor revocation: %+v", got)
	}
	// Duplicate evidence for one pair must enqueue one notification, not fill
	// the survivor's bounded writer queue and close a healthy control session.
	b.barrier()
	b.absent(proto.ControlTypeDisconnectPeer)
	f.coordinator.mu.Lock()
	claims := len(f.coordinator.revokedStartupClaims[sha256.Sum256([]byte("C"))])
	f.coordinator.mu.Unlock()
	if claims != 1 {
		t.Fatalf("duplicate survivor evidence retained %d claims, want 1", claims)
	}
}

func TestRevocationAfterCoordinatorReturnDeliversLargeRetainedSetWithoutDisconnect(t *testing.T) {
	f := newCoordinatorFixture(t, true)
	f.coordinator.mu.Lock()
	for index := 0; index < maxCoordinatorRelationships; index++ {
		revokedNodeID := fmt.Sprintf("revoked-%04d", index)
		f.coordinator.relationships[unorderedPair("B", revokedNodeID)] = &coordinatorRelationship{
			revoked: true, revokedNodeID: revokedNodeID,
		}
	}
	f.coordinator.mu.Unlock()

	b, _ := f.connect("B")
	seen := make(map[string]struct{}, maxCoordinatorRelationships)
	for range maxCoordinatorRelationships {
		revocation := decodeCoordinatorBody[proto.DisconnectPeer](t, b.want(proto.ControlTypeDisconnectPeer))
		if !strings.HasPrefix(revocation.NodeID, "revoked-") || revocation.Code != "member_revoked" {
			t.Fatalf("invalid retained revocation: %+v", revocation)
		}
		if _, duplicate := seen[revocation.NodeID]; duplicate {
			t.Fatalf("duplicate retained revocation for %q", revocation.NodeID)
		}
		seen[revocation.NodeID] = struct{}{}
	}
	// Delivery of a set larger than the per-peer output queue must not make
	// the coordinator evict the healthy survivor control connection.
	b.barrier()
}

func TestRevocationAfterCoordinatorReturnKeepsClaimWhenSourceReadmitsFirst(t *testing.T) {
	f := newCoordinatorFixture(t, true)
	f.nodes[1]["disabled"] = true
	f.saveRegistry()
	oldC := f.dial("C")
	oldC.send(proto.ControlTypeClientHello, f.hello("C"))
	oldC.send(proto.ControlTypeCandidateUpdate, proto.CandidateUpdate{Revision: 1})
	oldC.send(proto.ControlTypeActiveSessions, proto.ActiveSessions{Sessions: []proto.ActiveSession{{
		SessionID: "old-session", Generation: 7, PeerNodeID: "B", PathType: "quic_udp",
	}}})
	oldC.want(proto.ControlTypeError)
	oldC.closed()

	// A replacement identity for the same node ID may arrive before the old
	// session's survivor. That admission cannot erase the only old-session
	// revocation evidence retained after a coordinator restart.
	f.nodes[1]["disabled"] = false
	f.nodes[1]["routes"] = []string{"192.168.99.0/24"}
	f.saveRegistry()
	newC, _ := f.connect("C")
	b, _ := f.connect("B")
	if got := decodeCoordinatorBody[proto.DisconnectPeer](t, b.want(proto.ControlTypeDisconnectPeer)); got.NodeID != "C" {
		t.Fatalf("source-first readmission erased old C revocation: %+v", got)
	}
	b.barrier()
	newC.barrier()
}

func TestRevocationAfterActiveSessionsDeliversLargeNewRevokedSetWithoutDisconnect(t *testing.T) {
	f := newCoordinatorFixture(t, true)
	b, _ := f.connect("B")
	const revokedCount = 1024
	sessions := make([]proto.ActiveSession, revokedCount)
	for index := range sessions {
		sessions[index] = proto.ActiveSession{
			SessionID: fmt.Sprintf("old-%04d", index), Generation: uint64(index + 1),
			PeerNodeID: fmt.Sprintf("removed-%04d", index), PathType: "quic_udp",
		}
	}
	b.send(proto.ControlTypeActiveSessions, proto.ActiveSessions{Sessions: sessions})
	seen := make(map[string]struct{}, revokedCount)
	for range revokedCount {
		revocation := decodeCoordinatorBody[proto.DisconnectPeer](t, b.want(proto.ControlTypeDisconnectPeer))
		if !strings.HasPrefix(revocation.NodeID, "removed-") || revocation.Code != "member_revoked" {
			t.Fatalf("invalid report-derived revocation: %+v", revocation)
		}
		if _, duplicate := seen[revocation.NodeID]; duplicate {
			t.Fatalf("duplicate report-derived revocation for %q", revocation.NodeID)
		}
		seen[revocation.NodeID] = struct{}{}
	}
	b.barrier()
}

func TestCoordinatorActiveSessionsPreservesLongRegisteredTargetCompatibility(t *testing.T) {
	f := newCoordinatorFixture(t, true)
	longID := strings.Repeat("x", 1024)
	f.nodes = append(f.nodes, map[string]any{
		"node_id": longID, "virtual_ip": "10.77.0.99",
		"cert_fingerprint": strings.Repeat("a", 64), "routes": []string{},
	})
	f.saveRegistry()
	b, _ := f.connect("B")
	b.send(proto.ControlTypeActiveSessions, proto.ActiveSessions{Sessions: []proto.ActiveSession{{
		SessionID: "long-target", Generation: 1, PeerNodeID: longID, PathType: "quic_udp",
	}}})
	b.barrier()
	f.coordinator.mu.Lock()
	_, becameKnown := f.coordinator.known[longID]
	_, becameRelationship := f.coordinator.relationships[unorderedPair("B", longID)]
	f.coordinator.mu.Unlock()
	if !becameKnown || !becameRelationship {
		t.Fatalf("long existing identity lost on upgrade: known=%v relationship=%v", becameKnown, becameRelationship)
	}
	b.absent(proto.ControlTypeDisconnectPeer)
}

func (h *pureP2PHarness) dialTLSClient(t *testing.T, id string) net.Conn {
	t.Helper()
	tlsConfig, err := tlsutil.ClientConfig(
		filepath.Join(h.certDir, "ca.pem"), filepath.Join(h.certDir, id+".pem"),
		filepath.Join(h.certDir, id+"-key.pem"), "127.0.0.1",
	)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp4", h.recorder.addr, tlsConfig)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func writeV2Control(t *testing.T, conn net.Conn, typ string, body any) {
	t.Helper()
	payload, err := proto.MarshalControl(typ, "integration", body)
	if err != nil {
		t.Fatal(err)
	}
	if err := proto.Write(conn, proto.TypeControl, payload); err != nil {
		t.Fatal(err)
	}
}

func readControlError(t *testing.T, conn net.Conn, timeout time.Duration) proto.ControlError {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		frame, err := proto.Read(conn)
		if err != nil {
			t.Fatalf("controlled error was not delivered before close: %v", err)
		}
		if frame.Type != proto.TypeControl {
			t.Fatalf("controlled error used legacy frame type %d", frame.Type)
		}
		envelope, err := proto.ParseControl(frame.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if envelope.Type != proto.ControlTypeError {
			continue
		}
		controlError, err := proto.DecodeControlBody[proto.ControlError](envelope)
		if err != nil {
			t.Fatal(err)
		}
		return controlError
	}
	t.Fatal("timed out waiting for controlled error")
	return proto.ControlError{}
}

func assertTLSConnectionClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err := proto.Read(conn)
	if err == nil {
		t.Fatal("protocol-violating control connection remained open")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatal("protocol-violating control connection did not close")
	}
	if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, net.ErrClosed) && !strings.Contains(strings.ToLower(err.Error()), "closed") {
		// TLS close_notify and reset errors are both controlled terminal closes.
		var opErr *net.OpError
		if !errors.As(err, &opErr) {
			t.Fatalf("unexpected terminal read error: %v", err)
		}
	}
}

func TestLegacyAndMaliciousControlClientsCannotSendData(t *testing.T) {
	h := newPureP2PHarness(t)
	h.startPeers("B", "C")

	legacy := h.dialTLSClient(t, "legacy")
	legacyHello, err := (proto.Hello{NodeID: "legacy", VirtualIP: "10.77.0.9", MTU: 1280}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := proto.Write(legacy, proto.TypeHello, legacyHello); err != nil {
		t.Fatal(err)
	}
	upgrade := readControlError(t, legacy, 3*time.Second)
	if upgrade.Code != "control_upgrade_required" || upgrade.Message == "" {
		t.Fatalf("legacy client error=%+v", upgrade)
	}
	assertTLSConnectionClosed(t, legacy)
	_ = legacy.Close()

	malicious := h.dialTLSClient(t, "M")
	writeV2Control(t, malicious, proto.ControlTypeClientHello, proto.ClientHello{
		ProtocolVersion: 2, Role: "peer", NodeID: "M", VirtualIP: "10.77.0.6", MTU: 1280,
		Capabilities: []string{"quic_udp_v1"}, Routes: []string{},
	})
	// Admission must complete so this is a malicious v2 control client, not a
	// legacy framing error. readControlError below tolerates queued snapshots.
	_ = malicious.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		frame, err := proto.Read(malicious)
		if err != nil {
			t.Fatalf("malicious v2 admission: %v", err)
		}
		envelope, err := proto.ParseControl(frame.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if envelope.Type == proto.ControlTypeServerHello {
			break
		}
	}
	if err := proto.Write(malicious, proto.TypePacket, literalBToC); err != nil {
		t.Fatal(err)
	}
	violation := readControlError(t, malicious, 3*time.Second)
	if violation.Code != "protocol_violation" || violation.Message == "" {
		t.Fatalf("malicious TypePacket error=%+v", violation)
	}
	assertTLSConnectionClosed(t, malicious)
	_ = malicious.Close()

	integrationEventually(t, 3*time.Second, func() (bool, string) {
		metrics := h.currentA.status.snapshot().CoordinatorMetrics
		return metrics.TypePacketViolations == 1, fmt.Sprintf("metrics=%+v", metrics)
	})
	assertChannelQuiet(t, h.peers["B"].device, 4*integrationHeartbeatInterval)
	assertChannelQuiet(t, h.peers["C"].device, 4*integrationHeartbeatInterval)
	assertNoRelayRuntime(t, h)
}
