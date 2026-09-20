# Meshlink Pure P2P Data Plane Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the self-hosted Hub forwarding data plane with on-demand, authenticated, long-lived peer-to-peer QUIC sessions while retaining the existing CA, device certificates, invitations, registry, and administrative lifecycle.

**Architecture:** The coordinator keeps only the mTLS control plane and an authenticated UDP observation socket. Each peer owns one UDP socket through one `quic.Transport`; that socket carries rendezvous probes, hole-punch packets, incoming and outgoing QUIC handshakes, peer heartbeats, and encrypted IP datagrams. A peer-level session manager, packet router, and status projection remain alive independently of any individual coordinator TCP connection.

**Tech Stack:** Go 1.26, standard-library TCP/TLS 1.3 and UDP, `github.com/quic-go/quic-go` v0.61.0, existing WireGuard TUN device package, framed strict JSON control messages, HMAC-SHA256 rendezvous/session authorization, Windows desktop HTML/JavaScript.

**Spec:** `docs/superpowers/specs/2026-09-14-pure-p2p-data-plane-design.md`

## Global Constraints

- The coordinator never opens a data-plane TUN, reads an inner IPv4 packet, accepts user payload, or forwards `TypePacket`.
- A v2 coordinator treats every `TypePacket` as a protocol violation, increments its violation counter, and immediately closes that control connection.
- Control Hello requires `protocol_version=2` and `role=peer`; v1 and v2 data protocols never run together or silently fall back.
- Every peer uses exactly one UDP socket for observation probes, hole punching, QUIC listening, and QUIC dialing.
- QUIC uses ALPN `meshlink-p2p/1`, TLS 1.3 or newer, mutual network-CA authentication, no 0-RTT, expected node ID and certificate fingerprint checks, and a per-generation 256-bit one-time pairing key.
- Inner IPv4 packets travel only in QUIC DATAGRAM frames; each encoded fragment is at most 1000 bytes.
- A pending target retains at most 64 packets or 256 KiB for at most 3 seconds; every session send queue and reassembly cache is bounded.
- A healthy direct session survives coordinator TCP/UDP shutdown. Once that direct session fails, its offer and pairing key are consumed and it cannot reconnect until an online coordinator grants a newer generation.
- Device pairs have independent contexts, heartbeats, queues, and failure transitions; no pair failure may cancel another pair.
- There is no Relay, TURN, TCP data channel, third-peer forwarding, Relay configuration, or automatic fallback in the self-hosted desktop and `mesh-agent` v2 runtime.
- `lan_direct` and `public_direct` are emitted only after a real QUIC connection and bidirectional `SessionHello` authorization complete.
- Existing CA files, device certificates, invitation data, registry membership, disable/remove behavior, virtual IPv4 addresses, routes, MTU, and peer TUN setup survive migration.
- Until three-machine Windows LAN and two-NAT validation is recorded, the only permitted release statement is “实现完成，公网实测待验收”.

---

### Task 1: Versioned configuration migration and coordinator process boundary

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/agent/agent.go`
- Modify: `internal/agent/agent_test.go`
- Modify: `internal/onboarding/manager.go`
- Modify: `internal/onboarding/enroll.go`
- Modify: `internal/onboarding/onboarding_test.go`
- Modify: `configs/hub.example.json`
- Modify: `configs/local-hub.example.json`
- Modify: `configs/spoke.example.json`
- Modify: `configs/local-spoke.example.json`

**Interfaces:**
- Produces: `config.ConfigVersion == 2`, `config.ControlProtocolV2 == "tcp_tls_control_v2"`, `config.P2PProtocolQUICUDPv1 == "quic_udp_v1"`.
- Produces: `type P2PConfig struct { Protocol string; Listen string }` and `Config.P2P P2PConfig`.
- Produces: `Config.NetworkCIDR string`, which is mandatory for `mode=hub`; peer `VirtualIP`, `Routes`, `MTU`, `Device`, and `Setup` remain mandatory/normalized only for `mode=spoke`.
- Produces: `config.Write(path string, cfg Config) error`, an atomic same-directory temp-file/rename writer used by migration and onboarding.
- Produces: `agent.WithDevice(device.Device)` for integration injection; production callers omit it.
- Consumes: existing JSON field names and relative certificate paths without changing their meaning.

- [ ] **Step 1: Write failing migration and process-boundary tests**

```go
func TestLoadMigratesV1SpokeOnceAndKeepsIdentity(t *testing.T) {
    // Write a versionless tcp_tls_v1 spoke JSON, call Load, and assert:
    // Version==2, transport protocol tcp_tls_control_v2,
    // P2P=={quic_udp_v1,0.0.0.0:0}, certificate/virtual IP/routes unchanged,
    // active.json.v1.bak equals the original bytes, and a second Load leaves it unchanged.
}

func TestLoadMigratesV1HubWithoutDataPlaneFields(t *testing.T) {
    // Include virtual_ip/device/setup in the v1 input; after Load assert the
    // coordinator retains identity/listen/certificates and derives 10.77.0.0/24,
    // while VirtualIP, Routes, MTU, Device, Setup, and P2P are zero values.
}

func TestNewCoordinatorDoesNotOpenConfiguredDevice(t *testing.T) {
    cfg := coordinatorConfigForTest()
    cfg.Device.Type = "must-not-be-opened"
    cfg.Setup.Enabled = true
    got, err := New(&cfg, discardLogger())
    if err != nil || got.dev != nil { t.Fatalf("coordinator data device = %v, err = %v", got.dev, err) }
}
```

- [ ] **Step 2: Run the focused tests and verify RED**

Run: `go test ./internal/config ./internal/agent ./internal/onboarding -run 'TestLoadMigratesV1|TestNewCoordinator|TestGenerated.*V2' -count=1`

Expected: FAIL because version/P2P migration, atomic backup writing, and a device-free coordinator do not exist.

- [ ] **Step 3: Implement v2 normalization, atomic migration, and role-specific Agent construction**

```go
const (
    ConfigVersion = 2
    ControlProtocolV2 = "tcp_tls_control_v2"
    P2PProtocolQUICUDPv1 = "quic_udp_v1"
)

type P2PConfig struct {
    Protocol string `json:"protocol,omitempty"`
    Listen   string `json:"listen,omitempty"`
}

func Write(path string, cfg Config) error
func migrateV1(cfg Config) (Config, bool, error)
```

`Load` must parse the original bytes, migrate in memory, fully validate the migrated value, create `<path>.v1.bak` with `O_EXCL` semantics, then atomically write v2. If any backup or replacement write fails, return the error with the original active file unchanged. `Agent.New` opens/applies a device only in spoke mode; a hub receives `dev=nil`. Generated hub JSON contains coordinator identity, network CIDR, listen address, and certificates only. Generated peer JSON contains the new P2P section.

- [ ] **Step 4: Run focused and package tests and verify GREEN**

Run: `go test ./internal/config ./internal/agent ./internal/onboarding -count=1`

Expected: PASS, including existing invitation, certificate, join, leave, and device-registry tests.

- [ ] **Step 5: Commit**

```powershell
git add internal/config internal/agent/agent.go internal/agent/agent_test.go internal/onboarding configs
git commit -m "feat: migrate self-hosted configs to p2p v2"
```

### Task 2: Strict framed JSON control protocol v2

**Files:**
- Create: `internal/proto/control_v2.go`
- Create: `internal/proto/control_v2_test.go`
- Modify: `internal/proto/frame.go`
- Modify: `internal/proto/frame_test.go`
- Modify: `internal/proto/hello.go`

**Interfaces:**
- Produces: `proto.TypeControl byte = 7`; `TypePacket` remains defined only so v2 can explicitly reject legacy clients.
- Produces: `ControlEnvelope`, `MarshalControl`, `ParseControl`, and `DecodeControlBody[T]`.
- Produces exact body types: `ClientHello`, `ServerHello`, `ProbeCredential`, `Candidate`, `CandidateUpdate`, `Member`, `MemberSnapshot`, `MemberDelta`, `ConnectRequest`, `ConnectPrepare`, `ConnectReady`, `SessionOffer`, `SessionOfferAck`, `SessionStart`, `SessionAbort`, `SessionResult`, `ActiveSession`, `ActiveSessions`, `DisconnectPeer`, `ControlPing`, `ControlPong`, and `ControlError`.
- Consumes: the existing one-MiB framed transport bound from `frame.go`.

- [ ] **Step 1: Write failing protocol contract tests**

```go
func TestControlV2RoundTripSessionOffer(t *testing.T) {
    payload, err := MarshalControl(ControlTypeSessionOffer, "req-7", SessionOffer{
        SessionID: "session-1", Generation: 9, PeerNodeID: "node-c",
        DialerNodeID: "node-b", PairingKey: strings.Repeat("ab", 32),
    })
    if err != nil { t.Fatal(err) }
    env, err := ParseControl(payload)
    if err != nil { t.Fatal(err) }
    offer, err := DecodeControlBody[SessionOffer](env)
    if err != nil || offer.Generation != 9 { t.Fatalf("offer=%+v err=%v", offer, err) }
}

func TestControlV2RejectsUnknownTypeVersionFieldAndOversize(t *testing.T) {
    for _, raw := range [][]byte{
        []byte(`{"protocol_version":1,"type":"ping","body":{}}`),
        []byte(`{"protocol_version":2,"type":"future","body":{}}`),
        []byte(`{"protocol_version":2,"type":"ping","body":{},"future":true}`),
    } {
        if _, err := ParseControl(raw); err == nil { t.Fatalf("accepted %s", raw) }
    }
}
func TestClientHelloRequiresVersionTwoAndPeerRole(t *testing.T) {
    hello := ClientHello{ProtocolVersion: 1, Role: "hub", NodeID: "b", VirtualIP: "10.77.0.2", MTU: 1280}
    if err := hello.Validate(); err == nil { t.Fatal("legacy/non-peer hello accepted") }
}
```

The mutation each test catches is respectively a wrong message mapping, permissive forward/legacy parsing, or loss of the v2 role gate.

- [ ] **Step 2: Run the protocol tests and verify RED**

Run: `go test ./internal/proto -run 'TestControlV2|TestClientHelloRequires' -count=1`

Expected: FAIL because the v2 envelope and body types are absent.

- [ ] **Step 3: Implement the strict v2 codec and messages**

```go
type ControlEnvelope struct {
    ProtocolVersion int             `json:"protocol_version"`
    Type            string          `json:"type"`
    RequestID       string          `json:"request_id,omitempty"`
    Body            json.RawMessage `json:"body"`
}

func MarshalControl(messageType, requestID string, body any) ([]byte, error)
func ParseControl(payload []byte) (ControlEnvelope, error)
func DecodeControlBody[T any](env ControlEnvelope) (T, error)
```

Use `json.Decoder.DisallowUnknownFields`, reject trailing JSON, empty/unknown type, any envelope version other than 2, and payloads larger than the frame limit. `ClientHello.Validate` requires version 2, role `peer`, node ID, virtual IPv4, MTU, and capability `quic_udp_v1`. Candidates contain only address, port, UDP scope, priority, and expiry; member snapshots never contain physical addresses.

- [ ] **Step 4: Run protocol tests and verify GREEN**

Run: `go test ./internal/proto -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/proto
git commit -m "feat: define strict p2p control protocol v2"
```

### Task 3: One-socket UDP observation and authenticated hole punching

**Files:**
- Create: `internal/p2p/probe.go`
- Create: `internal/p2p/probe_test.go`
- Create: `internal/p2p/candidate_service.go`
- Create: `internal/p2p/candidate_service_test.go`
- Modify: `go.mod`
- Modify: `go.sum`
- Modify: `THIRD_PARTY_NOTICES.md`

**Interfaces:**
- Consumes: `proto.ProbeCredential`, `proto.Candidate`, `quic.Transport.ReadNonQUICPacket`, and `quic.Transport.WriteTo`.
- Produces: `ProbeAuthority.Issue`, `ProbeAuthority.Handle`, and authenticated request/response codecs whose first byte is `0x00` so quic-go demultiplexes them as non-QUIC.
- Produces: `CandidateService`, owning exactly one `*net.UDPConn`, one `*quic.Transport`, and one `*quic.Listener` for its lifetime.
- Produces: `CandidateService.Refresh(ctx, rendezvousAddr, credential) (CandidateSnapshot, error)`, `Punch(ctx, sessionID, generation, key, candidates)`, `Transport()`, `Listener()`, `LocalAddr()`, and `Close()`.

- [ ] **Step 1: Add `quic-go` v0.61.0 and write failing probe tests**

Run: `go get github.com/quic-go/quic-go@v0.61.0`

Then write tests that name these breaks:

```go
func TestProbeAuthorityAuthenticatesExpiryAndNonceReplay(t *testing.T) {
    // Issue at a fixed clock, accept one correctly signed nonce, reject the
    // byte-identical replay, advance beyond ExpiresAt, and reject a new nonce.
}
func TestProbeResponseBindsObservedSourceAddress(t *testing.T) {
    // Pass source 203.0.113.7:45678 to Handle and verify the signed response
    // yields exactly that address; changing one address byte must break HMAC.
}
func TestCandidateServiceProbePunchAndQUICShareOneSocket(t *testing.T) {
    // A real UDP rendezvous socket records request.RemoteAddr; Refresh returns
    // that same port, Punch leaves from that port, and service.Transport().Conn
    // is the exact UDPConn held by the service.
}
func TestCandidateServiceExcludesExpiredAndDuplicateCandidates(t *testing.T) {
    // Enumerate the same IPv4 twice, return one observed address, advance the
    // clock 121 seconds, and assert one live LAN plus no expired public entry.
}
```

- [ ] **Step 2: Run candidate tests and verify RED**

Run: `go test ./internal/p2p -run 'TestProbe|TestCandidateService' -count=1`

Expected: FAIL because authenticated rendezvous and the shared socket service are absent.

- [ ] **Step 3: Implement probe authority and candidate service**

```go
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

func NewCandidateService(cfg CandidateServiceConfig) (*CandidateService, error)
```

Probe HMAC input is the canonical tuple `(kind, version, probe_id, unix_millis, nonce, observed_address)`. Credentials expire after 90 seconds; nonce entries expire with the credential. LAN enumeration includes up/non-loopback IPv4 addresses and the service's bound port; an explicitly bound non-unspecified IPv4 address is also included, enabling deterministic loopback integration without changing default enumeration. Candidate TTL is 120 seconds. Punch packets include session ID, generation, nonce, and HMAC under the pairing key; they may open NAT mappings but never establish a direct state.

- [ ] **Step 4: Run candidate and race tests and verify GREEN**

Run: `go test ./internal/p2p -run 'TestProbe|TestCandidateService' -count=1`

Run: `go test -race ./internal/p2p -run 'TestProbe|TestCandidateService' -count=1`

Expected: both PASS.

- [ ] **Step 5: Commit**

```powershell
git add go.mod go.sum THIRD_PARTY_NOTICES.md internal/p2p/probe* internal/p2p/candidate_service*
git commit -m "feat: add shared udp rendezvous service"
```

### Task 4: Bounded IP datagrams, longest-prefix routing, and anti-spoofing

**Files:**
- Create: `internal/p2p/datagram.go`
- Create: `internal/p2p/datagram_test.go`
- Create: `internal/p2p/router.go`
- Create: `internal/p2p/router_test.go`
- Modify: `internal/proto/ippacket.go`
- Modify: `internal/proto/frame_test.go`

**Interfaces:**
- Produces: `FragmentPacket(packetID uint64, packet []byte, mtu int) ([][]byte, error)` with every result length `<=1000`.
- Produces: `Reassembler.Add(peerID string, fragment []byte, now time.Time) ([]byte, bool, error)` and `Expire(now)` with per-peer and global byte/count/time limits.
- Produces: `RouteTable.Replace([]proto.Member) error`, `RouteTable.Lookup(netip.Addr) (proto.Member, bool)`, and literal longest-prefix semantics.
- Produces: `ValidateInboundPacket(peer proto.Member, localVirtualIP netip.Addr, localRoutes []netip.Prefix, packet []byte) error`.
- Extends: `proto.SourceIP` alongside existing `DestinationIP`.

- [ ] **Step 1: Write failing framing and routing tests**

```go
func TestFragmentRoundTripOutOfOrderWithinOneThousandBytes(t *testing.T) {
    // Fragment a literal 1280-byte packet, assert each encoded fragment <=1000,
    // feed them in reverse order, and compare the assembled bytes to the literal.
}
func TestReassemblerRejectsConflictingDuplicateAndExpiresMissingFragments(t *testing.T) {
    // Add index 0 twice with different payload bytes and expect an error; add a
    // partial packet, advance four seconds, call Expire, and assert zero usage.
}
func TestReassemblerEnforcesPerPeerAndGlobalMemoryBounds(t *testing.T) {
    // Fill exact configured limits, add one packet for the same peer and one for
    // another peer, and assert oldest entries are evicted without exceeding bytes.
}
func TestRouteTableUsesLongestPrefixAndRejectsEqualPrefixConflict(t *testing.T) {
    // Install B=10.0.0.0/8 and C=10.77.0.0/24; 10.77.0.9 resolves to C.
    // A second owner of 10.77.0.0/24 makes Replace return route_conflict.
}
func TestValidateInboundPacketRejectsSpoofedSourceAndForeignDestination(t *testing.T) {
    // C may send from 10.77.0.3 to local 10.77.0.2; source 10.77.0.4 and
    // destination 10.77.0.9 are each rejected with literal packet fixtures.
}
```

Expected packet bytes and route winners must be literal fixtures, not produced by helpers under test.

- [ ] **Step 2: Run focused tests and verify RED**

Run: `go test ./internal/p2p ./internal/proto -run 'TestFragment|TestReassembler|TestRouteTable|TestValidateInbound|TestSource' -count=1`

Expected: FAIL because these APIs do not exist.

- [ ] **Step 3: Implement bounded fragmentation, reassembly, and route authorization**

The binary fragment header is version (1 byte), packet ID (8), fragment index (2), fragment count (2), original length (2), and payload length (2), big-endian. Reject zero count, index outside count, original length above configured MTU or 9000, encoded length mismatch, fragment count above 16, and two different bytes for the same packet/index. Default reassembly expiry is 3 seconds, per-peer maximum is 64 incomplete packets/256 KiB, and global maximum is 1024 packets/8 MiB.

- [ ] **Step 4: Run focused and package tests and verify GREEN**

Run: `go test ./internal/p2p ./internal/proto -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/p2p/datagram* internal/p2p/router* internal/proto/ippacket.go internal/proto/frame_test.go
git commit -m "feat: add bounded p2p packet routing"
```

### Task 5: Authorized long-lived QUIC session manager

**Files:**
- Create: `internal/p2p/session_manager.go`
- Create: `internal/p2p/session_wire.go`
- Create: `internal/p2p/session_manager_test.go`
- Create: `internal/p2p/testcert_test.go`
- Modify: `internal/p2p/status.go`
- Modify: `internal/p2p/model.go`

**Interfaces:**
- Consumes: Task 3 `CandidateService`, Task 4 datagram/reassembly/router primitives, `proto.SessionOffer`, and real `*quic.Conn` objects.
- Produces: `SessionManager.InstallOffer`, `StartOffer`, `Send`, `ClosePeer`, `SetCoordinatorAvailable`, `ActiveSessions`, `Snapshot`, and `Close`.
- Produces callbacks `RequestSession(peerID string)`, `DeliverPacket(peerID string, packet []byte) error`, and `SessionChanged(SessionSnapshot)`.
- Produces exact runtime states `idle`, `requesting`, `preparing`, `punching`, `authenticating`, `lan_direct`, `public_direct`, `reconnecting`, `waiting_coordinator`, `failed`, and `closed`.

- [ ] **Step 1: Write failing real-QUIC tests**

```go
func TestSessionManagersExchangeEncryptedDatagramsOverRealQUIC(t *testing.T) {
    // Create one CA and two device certs, two real CandidateServices, install
    // matching offers, start both, wait for ready, then assert literal IPv4
    // packets travel in both directions and both QUIC connections stay open.
}
func TestSessionManagerRejectsWrongCAFingerprintNodeKeyAndGeneration(t *testing.T) {
    // Table cases independently swap CA, expected SHA256 fingerprint, node ID,
    // one pairing-key byte, and generation; none may reach a direct snapshot.
}
func TestSessionManagerDoesNotMarkCandidateOrTLSOnlyProbeDirect(t *testing.T) {
    // Refresh valid candidates and complete bare QUIC TLS without SessionHello;
    // snapshots remain punching/authenticating and Send never delivers a packet.
}
func TestSessionFailureWaitsForCoordinatorAndConsumesOffer(t *testing.T) {
    // Establish generation 7, set coordinator unavailable, close QUIC, and assert
    // waiting_coordinator. Calling StartOffer again with generation 7 is rejected.
}
func TestPairFailureDoesNotCancelAnotherReadyPair(t *testing.T) {
    // Build B-C and D-E managers, close only B-C, then exchange three D-E packets
    // and assert D-E session ID, generation, and direct state never change.
}
```

- [ ] **Step 2: Run session tests and verify RED**

Run: `go test ./internal/p2p -run 'TestSessionManager' -count=1`

Expected: FAIL because there is no production QUIC session.

- [ ] **Step 3: Implement mutual TLS and bidirectional SessionHello authorization**

```go
type SessionManagerConfig struct {
    NodeID, NetworkID string
    MTU               int
    Candidates        *CandidateService
    RequestSession    func(string)
    DeliverPacket     func(string, []byte) error
    SessionChanged    func(SessionSnapshot)
    HeartbeatInterval time.Duration
    HeartbeatTimeout  time.Duration
    DialTimeout       time.Duration
}

type SessionSnapshot struct {
    PeerNodeID, SessionID, ErrorCode string
    Generation                       uint64
    State                            PathState
    PathType                         PathType
    RTT                              time.Duration
    LastHeartbeat                    time.Time
    BytesSent, BytesReceived         uint64
}
```

Configure ALPN `meshlink-p2p/1`, TLS 1.3, mutual CA verification, `EnableDatagrams=true`, `KeepAlivePeriod=15s`, `MaxIdleTimeout=45s`, and no early dialing API. The stable lower node ID is the dialer. The dialer opens one bidirectional stream and both sides exchange length-bounded `SessionHello` values. HMAC covers session ID, generation, from/to node IDs, and each side's nonce. Only after both hellos validate may a session become direct or accept/send DATAGRAMs. Consume and zero the pairing key on success or failure. Heartbeats use the authenticated stream every 10 seconds with a 35-second timeout and never reference coordinator context.

- [ ] **Step 4: Implement bounded pending and per-session send queues**

`Send` queues at most 64 packets/256 KiB/3 seconds while requesting; one request callback is emitted per peer with exponential backoff. A ready session has a separate 256-packet bounded send queue; overflow drops the oldest packet and increments a controlled counter. Closing one session cancels only that pair's context. With coordinator unavailable it transitions to `waiting_coordinator`; with coordinator available it transitions to `reconnecting` and requests a new offer.

- [ ] **Step 5: Run session and race tests and verify GREEN**

Run: `go test ./internal/p2p -run 'TestSessionManager' -count=1`

Run: `go test -race ./internal/p2p -run 'TestSessionManager' -count=1`

Expected: both PASS and the tests use real UDP sockets and real QUIC, not a fake dialer.

- [ ] **Step 6: Commit**

```powershell
git add internal/p2p
git commit -m "feat: carry mesh packets over authorized quic sessions"
```

### Task 6: Coordinator control server, probe service, and negotiation state machine

**Files:**
- Replace: `internal/agent/hub.go`
- Create: `internal/agent/coordinator.go`
- Create: `internal/agent/coordinator_test.go`
- Modify: `internal/agent/hub_test.go`
- Modify: `internal/onboarding/device_admin.go`
- Modify: `internal/onboarding/device_admin_test.go`
- Modify: `internal/agent/status.go`

**Interfaces:**
- Consumes: v2 control messages, `p2p.ProbeAuthority`, existing enrollment HTTP multiplexing, existing CA/cert/registry files.
- Produces: a coordinator registry keyed by exact node ID and certificate fingerprint, short-lived probe credentials, candidate TTL storage, member revisions, per-unordered-pair single-flight negotiations, generation allocation, dual-Ack start, and revocation messages.
- Produces: `CoordinatorMetrics` with control connections/reconnects, probe success/failure, candidate refreshes, negotiation counts, and `TypePacketViolations`; it has no user-byte counter.
- Produces: `onboarding.Manager.LookupDevice(nodeID string)` so coordinator identity and route admission use the persisted registry.

- [ ] **Step 1: Write failing coordinator tests**

```go
func TestCoordinatorRejectsV1AndTypePacketWithoutForwarding(t *testing.T) {
    // A v1 Hello receives upgrade_required and closes. A valid v2 peer sending
    // TypePacket closes, increments TypePacketViolations once, and reaches no sink.
}
func TestCoordinatorBindsHelloNodeAndFingerprintToRegistry(t *testing.T) {
    // Registry B has fingerprint F and IP .2; reject hello node C, fingerprint G,
    // or virtual IP .9, and accept only the exact registered tuple.
}
func TestCoordinatorMemberSnapshotHasNoPhysicalAddresses(t *testing.T) {
    // Connect B and C from literal remote sockets; decode B's snapshot and assert
    // C identity/routes/status exist while neither remote socket string appears.
}
func TestCoordinatorConnectSingleFlightRequiresBothReadyAndBothAck(t *testing.T) {
    // Send simultaneous B->C and C->B requests, observe one prepare ID, withhold
    // C Ready then C Ack, and assert Start appears only after both stages finish.
}
func TestCoordinatorOffersNewerGenerationAndOneTimeKey(t *testing.T) {
    // Complete two negotiations for {B,C}; second generation is greater, session
    // IDs differ, each decoded key is 32 bytes, and B/C receive the same offer key.
}
func TestCoordinatorRevocationTargetsOnlyAffectedPairs(t *testing.T) {
    // Report B-C and D-E active, disable C, run registry reconciliation, and assert
    // only B receives DisconnectPeer(C); D and E controls remain open.
}
```

- [ ] **Step 2: Run coordinator tests and verify RED**

Run: `go test ./internal/agent -run 'TestCoordinator' -count=1`

Expected: FAIL because the Hub still routes `TypePacket` and has no v2 coordinator.

- [ ] **Step 3: Implement the data-free coordinator**

`runHub` binds TCP4 and UDP4 to the configured numeric port, starts the authenticated UDP probe loop, and accepts enrollment HTTP or mTLS v2 controls. It never starts `deviceToPeers`. A missing/legacy Hello receives `control_upgrade_required`; a v2 `TypePacket` increments metrics and closes immediately. Candidate maps are never included in member snapshots and enter offers only after authorization. A pair negotiation expires after 15 seconds, preparation after 5 seconds, and offer Ack after 5 seconds. Session IDs and 32-byte keys come from `crypto/rand`; generations are strictly increasing per pair and seeded above `time.Now().UnixNano()` so a process restart does not reuse a practical prior generation.

- [ ] **Step 4: Implement control reconnect and revocation convergence**

The newest connection for one node atomically replaces the prior one. `ActiveSessions` never reauthorizes an expired connection; it only lets the coordinator send `DisconnectPeer` for a disabled/removed or identity-mismatched member. A two-second registry watcher pushes disconnects for newly disabled/removed devices and closes their control sockets while leaving unrelated negotiations and pairs untouched.

- [ ] **Step 5: Run coordinator, enrollment, and race tests and verify GREEN**

Run: `go test ./internal/agent ./internal/onboarding -count=1`

Run: `go test -race ./internal/agent -run 'TestCoordinator' -count=1`

Expected: both PASS.

- [ ] **Step 6: Commit**

```powershell
git add internal/agent internal/onboarding/device_admin*
git commit -m "feat: turn hub into p2p coordinator"
```

### Task 7: Peer control client, TUN packet routing, and status decoupling

**Files:**
- Replace: `internal/agent/spoke.go`
- Create: `internal/agent/control_client.go`
- Create: `internal/agent/peer_runtime.go`
- Create: `internal/agent/peer_runtime_test.go`
- Modify: `internal/agent/agent.go`
- Modify: `internal/agent/status.go`
- Modify: `internal/agent/status_test.go`
- Modify: `internal/networkstate/state.go`
- Modify: `internal/networkstate/state_test.go`

**Interfaces:**
- Consumes: Task 5 session manager and Task 6 coordinator messages.
- Produces: a root-owned peer runtime whose `CandidateService`, `SessionManager`, TUN reader, and status store outlive every individual coordinator connection.
- Produces: a reconnecting `ControlClient` that sends ClientHello/CandidateUpdate/ActiveSessions, answers prepare/offer/start/abort/disconnect/ping, and never reads or writes `TypePacket`.
- Produces: status fields `coordinator_state`, `p2p_listen`, and real per-peer session snapshots.

- [ ] **Step 1: Write failing peer lifecycle tests**

```go
func TestControlDisconnectDoesNotCancelReadySessionContext(t *testing.T) {
    // Cancel only a control-connection context and assert the established session
    // context is not done and a literal peer datagram is still delivered.
}
func TestFirstTunPacketTriggersOneConnectRequestAndBoundedQueue(t *testing.T) {
    // Send 100 packets for C before a session; observe one ConnectRequest and
    // queue stats <=64 packets/256KiB, with packets older than 3s removed.
}
func TestMemberPresenceWithoutHandshakeNeverProjectsDirect(t *testing.T) {
    // Apply an online MemberSnapshot with LAN/public candidates unavailable and
    // assert C is idle/unknown with empty PathType, never lan/public direct.
}
func TestDirectFailureTransitionsByCoordinatorAvailability(t *testing.T) {
    // Close a ready session once with control up (reconnecting + one request) and
    // once with control down (waiting_coordinator + zero request).
}
func TestControlReconnectReportsActiveAndRequestsWaitingPairs(t *testing.T) {
    // Reconnect with one healthy B-D session and one waiting B-C pair; first write
    // contains B-D ActiveSessions and exactly one fresh ConnectRequest(C).
}
func TestInboundSpoofNeverReachesDevice(t *testing.T) {
    // Deliver an authenticated C datagram whose inner source is D; device write
    // channel stays empty and the peer spoof-drop counter increments.
}
```

- [ ] **Step 2: Run peer tests and verify RED**

Run: `go test ./internal/agent -run 'TestControlDisconnect|TestFirstTun|TestMemberPresence|TestDirectFailure|TestControlReconnect|TestInboundSpoof' -count=1`

Expected: FAIL because the spoke control connection still owns all device traffic and status.

- [ ] **Step 3: Implement the root-owned peer runtime**

```go
type controlSender interface {
    Send(messageType, requestID string, body any) error
    Available() bool
}

type peerRuntime struct {
    candidates *p2p.CandidateService
    sessions   *p2p.SessionManager
    routes     *p2p.RouteTable
    // control availability is atomic and never owns sessions.
}
```

Start the UDP service before TCP control. The TUN reader looks up the destination in the latest member routes and calls `sessions.Send`; it never has a control-write path. On member snapshot, replace only routing/presence metadata. Offer handling installs the offer before Ack. Start handling calls `StartOffer`. When control ends, set coordinator `reconnecting` and call `sessions.SetCoordinatorAvailable(false)` without canceling any session. On reconnect, send current candidates plus active sessions, then call `SetCoordinatorAvailable(true)` so waiting pairs request a fresh generation.

- [ ] **Step 4: Project real session events and remove default direct status**

Member presence projects `idle`/`offline_or_unknown`; requesting, preparing, punching, authenticating, direct, waiting, failure, and closed come only from session events. A healthy direct peer remains online if the coordinator state is reconnecting. Remove every `DefaultPathType: lan_direct` fallback in agent/onboarding status normalization. Controlled errors use the stable codes in the design, especially `direct_unreachable_no_relay`.

- [ ] **Step 5: Run peer, status, and race tests and verify GREEN**

Run: `go test ./internal/agent ./internal/networkstate -count=1`

Run: `go test -race ./internal/agent -run 'TestControlDisconnect|TestFirstTun|TestDirectFailure|TestControlReconnect' -count=1`

Expected: both PASS.

- [ ] **Step 6: Commit**

```powershell
git add internal/agent internal/networkstate
git commit -m "feat: route tun traffic through independent peer sessions"
```

### Task 8: Real-socket A/B/C/D/E fault-recovery integration proof

**Files:**
- Create: `internal/agent/pure_p2p_integration_test.go`
- Create: `internal/agent/test_device_test.go`
- Modify: `internal/agent/coordinator.go`
- Modify: `internal/agent/peer_runtime.go`
- Modify: `internal/p2p/session_manager.go`

**Interfaces:**
- Consumes: full production Agent startup, real TCP/TLS, real UDP sockets, real QUIC, generated CA/device certificates, and channel-backed test TUN devices.
- Produces: automated evidence for AC-01 through AC-06, AC-10 through AC-14, and no-Relay operation.

- [ ] **Step 1: Write the failing end-to-end fault matrix**

```go
func TestPureP2PDataPlaneFaultMatrix(t *testing.T) {
    // Start A plus B,C,D,E with one real CA, real certs, registry entries,
    // real TCP/UDP listeners, and real quic-go transports.
    // 1. Inject literal B->C and C->B IPv4 packets and observe delivery.
    // 2. Assert A metrics saw no TypePacket and expose no user-byte metric.
    // 3. Keep B<->C traffic active, stop A for >3 peer heartbeat intervals,
    //    and assert bidirectional delivery plus direct state.
    // 4. While A is down close only B<->C; assert waiting_coordinator and
    //    no delivery/new QUIC generation.
    // 5. Restart A; assert a larger generation and restored B<->C delivery.
    // 6. During B<->C failure/recovery continuously exchange D<->E packets;
    //    assert its session ID/state and every sequence delivery remain stable.
}

func TestConcurrentBidirectionalFirstPacketCreatesOnePairGeneration(t *testing.T) {
    // Release B->C and C->B TUN writes on one barrier and assert both direct
    // snapshots carry the same single session ID and generation.
}
func TestRevocationAfterCoordinatorReturnClosesOnlyAffectedPair(t *testing.T) {
    // Stop A, disable C in the registry, restart A, and assert B-C closes after
    // ActiveSessions reconciliation while D-E continues sequence delivery.
}
func TestLegacyAndMaliciousControlClientsCannotSendData(t *testing.T) {
    // Use real TLS clients for a v1 Hello and a v2 TypePacket; assert readable
    // upgrade/protocol errors, closed sockets, violation metrics, and no peer write.
}
```

- [ ] **Step 2: Run integration tests and verify RED**

Run: `go test ./internal/agent -run 'TestPureP2P|TestConcurrentBidirectional|TestRevocationAfter|TestLegacyAndMalicious' -count=1 -timeout=90s`

Expected: FAIL at the first missing or incorrect production behavior; no fake dialer or probe-only success is accepted.

- [ ] **Step 3: Make the minimum production corrections exposed by the real-socket test**

Corrections stay inside the listed coordinator/runtime/session files. Implement the production event path `coordinator.handleConnectRequest -> negotiation.ready -> negotiation.acked -> SessionStart`, `peerRuntime.handleControl -> SessionManager.InstallOffer/StartOffer`, and `SessionManager.sessionEnded -> waiting_coordinator|reconnecting`. Resolve duplicate QUIC connections by retaining the authenticated connection for the greatest generation, then the connection initiated by the offer's fixed dialer. Do not add bypass hooks that let tests mark a session direct.

- [ ] **Step 4: Run integration tests repeatedly and under the race detector**

Run: `go test ./internal/agent -run 'TestPureP2P|TestConcurrentBidirectional|TestRevocationAfter|TestLegacyAndMalicious' -count=5 -timeout=180s`

Run: `go test -race ./internal/agent -run 'TestPureP2P|TestConcurrentBidirectional|TestRevocationAfter|TestLegacyAndMalicious' -count=1 -timeout=180s`

Expected: both PASS with real QUIC sessions remaining open across coordinator shutdown.

- [ ] **Step 5: Commit**

```powershell
git add internal/agent internal/p2p/session_manager.go
git commit -m "test: prove pure p2p fault recovery"
```

### Task 9: Desktop truthfulness, advanced P2P settings, and migration documentation

**Files:**
- Modify: `internal/onboarding/status.go`
- Modify: `internal/onboarding/onboarding_test.go`
- Modify: `internal/ui/static/app.js`
- Modify: `internal/ui/static/index.html`
- Modify: `internal/ui/static/style.css`
- Modify: `internal/ui/server_test.go`
- Modify: `README.md`
- Modify: `DEPLOY.zh-CN.md`
- Modify: `docs/ops/private-deployment.zh-CN.md`
- Modify: `docs/qa/e2e-matrix.zh-CN.md`

**Interfaces:**
- Consumes: runtime `coordinator_state`, `p2p_listen`, and real peer path state.
- Produces: separate coordinator and peer-path labels; no self-hosted Relay entry/config/fallback copy.
- Produces: documented v1 backup/migration behavior and honest “no Relay” reachability limitations.

- [ ] **Step 1: Write failing status and static UI behavior tests**

```go
func TestDevicesKeepHealthyDirectPeerOnlineWhenCoordinatorReconnects(t *testing.T) {
    // Load runtime JSON with coordinator=reconnecting and C=lan_direct plus recent
    // heartbeat; Devices returns C online/direct and preserves coordinator state.
}
func TestDevicesNeverInferDirectFromMemberOnlineStatus(t *testing.T) {
    // Runtime JSON with C status online but empty session fields returns empty
    // path type and idle/offline_or_unknown rather than a direct state.
}
func TestStaticUISeparatesCoordinatorAndDirectState(t *testing.T) {
    // Assert stable DOM fields for coordinator state and P2P path, then check the
    // mapping source contains distinct labels for reconnecting and lan/public direct.
}
func TestStaticUISelfHostedFlowHasNoRelayEntryOrFallbackCopy(t *testing.T) {
    // Parse embedded index HTML and assert no visible entry targets relayView and
    // no self-hosted form exposes relay address/allow/fallback controls.
}
func TestStaticUIShowsP2PListenAndNoRelaySetting(t *testing.T) {
    // Assert advanced diagnostics has p2pListen and its renderer consumes
    // p2p_listen, while no advanced input name or id contains allowRelay.
}
```

The static tests may assert stable DOM IDs and user-visible behavior mappings, but must not assert an entire source file verbatim.

- [ ] **Step 2: Run onboarding/UI tests and verify RED**

Run: `go test ./internal/onboarding ./internal/ui -run 'TestDevices|TestStaticUI' -count=1`

Expected: FAIL because online members still default to direct and the self-hosted Relay surface remains visible.

- [ ] **Step 3: Implement truthful desktop projections**

Map `lan_direct` to “局域网直连”, `public_direct` to “公网直连”, negotiation states to “正在协商”, `waiting_coordinator` to “等待协调服务器”, `failed` with `direct_unreachable_no_relay` to “直连失败 · 本版本未启用中继”, and absent sessions to “离线或未知”. Display coordinator state separately as “已连接/重连中/已断开”. When coordinator is down but direct heartbeat is current, append “协调服务器离线，当前直连不受影响”. Remove the visible self-hosted Relay card/view/bindings and never render Relay usage/fallback for the self-hosted device list. Show the bound P2P UDP listen address under advanced diagnostics.

- [ ] **Step 4: Update deployment and QA documentation**

Document TCP and UDP use of the same coordinator port, peer UDP firewall requirements, automatic `.v1.bak`, no Hub TUN, no Relay, no TCP fallback, stable error codes, and the exact three-machine LAN/two-NAT/manual packet-capture procedure from the spec. State that local automated QUIC tests do not satisfy public-NAT acceptance.

- [ ] **Step 5: Run UI/onboarding tests and verify GREEN**

Run: `go test ./internal/onboarding ./internal/ui -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```powershell
git add internal/onboarding/status.go internal/onboarding/onboarding_test.go internal/ui README.md DEPLOY.zh-CN.md docs/ops/private-deployment.zh-CN.md docs/qa/e2e-matrix.zh-CN.md
git commit -m "feat: show truthful pure p2p desktop state"
```

### Task 10: Whole-repository verification and release evidence

**Files:**
- Create: `docs/qa/pure-p2p-data-plane-implementation-2026-09-14.md`
- Modify only if a verification failure has a test-first production fix: files named by that failure.

**Interfaces:**
- Consumes: all prior tasks and their test evidence.
- Produces: reproducible automated evidence, Windows binaries, and an explicit public-NAT acceptance status.

- [ ] **Step 1: Run formatting, dependency, vet, and full tests**

Run:

```powershell
$changedGo = git diff --name-only 4360b00 -- '*.go'
if ($changedGo) { gofmt -w $changedGo }
go mod tidy
go vet ./...
go test ./... -count=1
```

Expected: every command exits 0 and `go mod tidy` leaves only the intentional quic-go dependency graph.

- [ ] **Step 2: Run the full race suite**

Run: `go test -race ./... -count=1 -timeout=10m`

Expected: exit 0 with no race report.

- [ ] **Step 3: Build every Windows command**

Run:

```powershell
$out = Join-Path $env:TEMP 'meshlink-p2p-windows-build'
New-Item -ItemType Directory -Force $out | Out-Null
go build -o (Join-Path $out 'mesh-agent.exe') ./cmd/mesh-agent
go build -o (Join-Path $out 'mesh-desktop.exe') ./cmd/mesh-desktop
go build -o (Join-Path $out 'mesh-ui.exe') ./cmd/mesh-ui
go build -o (Join-Path $out 'meshctl.exe') ./cmd/meshctl
go build -o (Join-Path $out 'mesh-cloudhub.exe') ./cmd/mesh-cloudhub
go build -o (Join-Path $out 'mesh-update-server.exe') ./cmd/mesh-update-server
```

Expected: all six binaries exist and each command exits 0.

- [ ] **Step 4: Record evidence without overstating physical validation**

Write the exact commands, dates, exit codes, integration scenarios, dependency version, and artifact names to the QA record. If no three-machine/two-NAT environment was available, the status line must be exactly `实现完成，公网实测待验收`; list the unexecuted physical steps without marking them passed.

- [ ] **Step 5: Re-run the completion gate after documentation**

Run: `go test ./... -count=1`

Run: `git status --short`

Expected: tests exit 0; status contains only intentional plan/code/docs changes and no build artifacts.

- [ ] **Step 6: Commit**

```powershell
git add go.mod go.sum docs/qa/pure-p2p-data-plane-implementation-2026-09-14.md
git add -u
git commit -m "qa: record pure p2p implementation evidence"
```
