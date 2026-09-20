package p2p

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"meshlink/internal/proto"
)

const (
	ProbeNonceSize              = 16
	PunchNonceSize              = 16
	ProbeCredentialTTL          = 90 * time.Second
	MaxProbeNoncesPerCredential = 256

	nonQUICMarker          = byte(0x00)
	rendezvousVersion      = proto.ControlProtocolVersion
	probeRequestKind       = "probe_request"
	probeResponseKind      = "probe_response"
	punchKind              = "punch"
	probeIDRandomSize      = 16
	sharedSecretSize       = 32
	maxRendezvousPacketLen = 2048
	maxRendezvousStringLen = 512
	probeClockSkew         = 30 * time.Second
)

var (
	ErrProbeAuthentication = errors.New("probe authentication failed")
	ErrProbeExpired        = errors.New("probe credential expired")
	ErrProbeReplay         = errors.New("probe nonce replayed")
	ErrProbeNonceLimit     = errors.New("probe credential nonce limit exceeded")
	ErrProbeMalformed      = errors.New("malformed probe packet")
	ErrPunchAuthentication = errors.New("punch authentication failed")
	ErrPunchMalformed      = errors.New("malformed punch packet")
)

type ProbeResponse struct {
	ProbeID         string
	UnixMillis      int64
	Nonce           []byte
	ObservedAddress netip.AddrPort
}

type PunchPacket struct {
	SessionID  string
	Generation uint64
	Nonce      []byte
}

type ProbeAuthority struct {
	mu          sync.Mutex
	now         func() time.Time
	credentials map[string]*issuedProbeCredential
}

type issuedProbeCredential struct {
	key       []byte
	issuedAt  time.Time
	expiresAt time.Time
	nonces    map[string]struct{}
}

type probeWirePacket struct {
	Kind            string `json:"kind"`
	Version         int    `json:"version"`
	ProbeID         string `json:"probe_id"`
	UnixMillis      int64  `json:"unix_millis"`
	Nonce           string `json:"nonce"`
	ObservedAddress string `json:"observed_address"`
	HMAC            string `json:"hmac"`
}

type punchWirePacket struct {
	Kind       string `json:"kind"`
	Version    int    `json:"version"`
	SessionID  string `json:"session_id"`
	Generation uint64 `json:"generation"`
	Nonce      string `json:"nonce"`
	HMAC       string `json:"hmac"`
}

func NewProbeAuthority(now func() time.Time) *ProbeAuthority {
	if now == nil {
		now = time.Now
	}
	return &ProbeAuthority{
		now:         now,
		credentials: make(map[string]*issuedProbeCredential),
	}
}

func (a *ProbeAuthority) Issue() (proto.ProbeCredential, error) {
	if a == nil {
		return proto.ProbeCredential{}, errors.New("probe authority is nil")
	}
	probeIDBytes := make([]byte, probeIDRandomSize)
	if _, err := io.ReadFull(rand.Reader, probeIDBytes); err != nil {
		return proto.ProbeCredential{}, fmt.Errorf("generate probe ID: %w", err)
	}
	key := make([]byte, sharedSecretSize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return proto.ProbeCredential{}, fmt.Errorf("generate probe key: %w", err)
	}
	now := a.clock()
	credential := proto.ProbeCredential{
		ProbeID:   base64.RawURLEncoding.EncodeToString(probeIDBytes),
		Key:       encodeSecret(key),
		ExpiresAt: now.Add(ProbeCredentialTTL),
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.ensureInitializedLocked()
	a.pruneExpiredLocked(now, "")
	a.credentials[credential.ProbeID] = &issuedProbeCredential{
		key:       append([]byte(nil), key...),
		issuedAt:  now,
		expiresAt: credential.ExpiresAt,
		nonces:    make(map[string]struct{}),
	}
	return credential, nil
}

func (a *ProbeAuthority) Handle(packet []byte, source net.Addr) ([]byte, error) {
	if a == nil {
		return nil, errors.New("probe authority is nil")
	}
	request, nonce, err := decodeProbePacket(packet, probeRequestKind)
	if err != nil {
		return nil, err
	}
	observed, err := addrPortFromNetAddr(source)
	if err != nil {
		return nil, fmt.Errorf("%w: source address: %v", ErrProbeMalformed, err)
	}

	now := a.clock()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ensureInitializedLocked()
	credential, ok := a.credentials[request.ProbeID]
	if !ok {
		a.pruneExpiredLocked(now, "")
		return nil, ErrProbeAuthentication
	}
	if !verifyProbeMAC(credential.key, request, nonce) {
		return nil, ErrProbeAuthentication
	}
	if !now.Before(credential.expiresAt) {
		delete(a.credentials, request.ProbeID)
		return nil, ErrProbeExpired
	}
	requestTime := time.UnixMilli(request.UnixMillis).UTC()
	if requestTime.Before(credential.issuedAt.Add(-probeClockSkew)) || requestTime.After(credential.expiresAt) {
		return nil, ErrProbeExpired
	}
	nonceKey := string(nonce)
	if _, replayed := credential.nonces[nonceKey]; replayed {
		return nil, ErrProbeReplay
	}
	if len(credential.nonces) >= MaxProbeNoncesPerCredential {
		delete(a.credentials, request.ProbeID)
		return nil, ErrProbeNonceLimit
	}
	credential.nonces[nonceKey] = struct{}{}
	a.pruneExpiredLocked(now, request.ProbeID)

	response := probeWirePacket{
		Kind:            probeResponseKind,
		Version:         rendezvousVersion,
		ProbeID:         request.ProbeID,
		UnixMillis:      request.UnixMillis,
		Nonce:           request.Nonce,
		ObservedAddress: observed.String(),
	}
	response.HMAC = encodeMAC(probeMAC(credential.key, response.Kind, response.Version, response.ProbeID, response.UnixMillis, nonce, response.ObservedAddress))
	return marshalNonQUICPacket(response)
}

func EncodeProbeRequest(credential proto.ProbeCredential, at time.Time, nonce []byte) ([]byte, error) {
	if strings.TrimSpace(credential.ProbeID) == "" || len(credential.ProbeID) > maxRendezvousStringLen {
		return nil, fmt.Errorf("%w: invalid probe ID", ErrProbeMalformed)
	}
	if at.IsZero() {
		return nil, fmt.Errorf("%w: request time is required", ErrProbeMalformed)
	}
	at = at.UTC()
	if credential.ExpiresAt.IsZero() || !at.Before(credential.ExpiresAt.UTC()) {
		return nil, ErrProbeExpired
	}
	if len(nonce) != ProbeNonceSize {
		return nil, fmt.Errorf("%w: nonce must be %d bytes", ErrProbeMalformed, ProbeNonceSize)
	}
	key, err := decodeSecret(credential.Key)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid credential key", ErrProbeAuthentication)
	}
	request := probeWirePacket{
		Kind:       probeRequestKind,
		Version:    rendezvousVersion,
		ProbeID:    credential.ProbeID,
		UnixMillis: at.UnixMilli(),
		Nonce:      hex.EncodeToString(nonce),
	}
	request.HMAC = encodeMAC(probeMAC(key, request.Kind, request.Version, request.ProbeID, request.UnixMillis, nonce, ""))
	return marshalNonQUICPacket(request)
}

func DecodeProbeResponse(packet []byte, credential proto.ProbeCredential, now time.Time) (ProbeResponse, error) {
	response, nonce, err := decodeProbePacket(packet, probeResponseKind)
	if err != nil {
		return ProbeResponse{}, err
	}
	if response.ProbeID != credential.ProbeID {
		return ProbeResponse{}, ErrProbeAuthentication
	}
	if now.IsZero() || credential.ExpiresAt.IsZero() || !now.UTC().Before(credential.ExpiresAt.UTC()) {
		return ProbeResponse{}, ErrProbeExpired
	}
	key, err := decodeSecret(credential.Key)
	if err != nil || !verifyProbeMAC(key, response, nonce) {
		return ProbeResponse{}, ErrProbeAuthentication
	}
	observed, err := netip.ParseAddrPort(response.ObservedAddress)
	if err != nil || !observed.Addr().Unmap().Is4() || observed.Port() == 0 {
		return ProbeResponse{}, fmt.Errorf("%w: invalid observed address", ErrProbeMalformed)
	}
	observed = netip.AddrPortFrom(observed.Addr().Unmap(), observed.Port())
	if observed.String() != response.ObservedAddress {
		return ProbeResponse{}, fmt.Errorf("%w: observed address is not canonical", ErrProbeMalformed)
	}
	return ProbeResponse{
		ProbeID:         response.ProbeID,
		UnixMillis:      response.UnixMillis,
		Nonce:           append([]byte(nil), nonce...),
		ObservedAddress: observed,
	}, nil
}

func EncodePunchPacket(sessionID string, generation uint64, key string, nonce []byte) ([]byte, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || len(sessionID) > maxRendezvousStringLen || generation == 0 || len(nonce) != PunchNonceSize {
		return nil, fmt.Errorf("%w: invalid session, generation, or nonce", ErrPunchMalformed)
	}
	keyBytes, err := decodeSecret(key)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid pairing key", ErrPunchAuthentication)
	}
	punch := punchWirePacket{
		Kind:       punchKind,
		Version:    rendezvousVersion,
		SessionID:  sessionID,
		Generation: generation,
		Nonce:      hex.EncodeToString(nonce),
	}
	punch.HMAC = encodeMAC(punchMAC(keyBytes, punch.Kind, punch.Version, punch.SessionID, punch.Generation, nonce))
	return marshalNonQUICPacket(punch)
}

func DecodePunchPacket(packet []byte, key string) (PunchPacket, error) {
	var punch punchWirePacket
	if err := unmarshalNonQUICPacket(packet, &punch, ErrPunchMalformed); err != nil {
		return PunchPacket{}, err
	}
	if punch.Kind != punchKind || punch.Version != rendezvousVersion || strings.TrimSpace(punch.SessionID) == "" || len(punch.SessionID) > maxRendezvousStringLen || punch.Generation == 0 {
		return PunchPacket{}, ErrPunchMalformed
	}
	nonce, err := decodeNonce(punch.Nonce, PunchNonceSize)
	if err != nil {
		return PunchPacket{}, fmt.Errorf("%w: %v", ErrPunchMalformed, err)
	}
	keyBytes, err := decodeSecret(key)
	if err != nil {
		return PunchPacket{}, fmt.Errorf("%w: invalid pairing key", ErrPunchAuthentication)
	}
	want := punchMAC(keyBytes, punch.Kind, punch.Version, punch.SessionID, punch.Generation, nonce)
	if !validEncodedMAC(punch.HMAC, want) {
		return PunchPacket{}, ErrPunchAuthentication
	}
	return PunchPacket{SessionID: punch.SessionID, Generation: punch.Generation, Nonce: append([]byte(nil), nonce...)}, nil
}

func (a *ProbeAuthority) clock() time.Time {
	if a.now == nil {
		return time.Now().UTC()
	}
	return a.now().UTC()
}

func (a *ProbeAuthority) ensureInitializedLocked() {
	if a.credentials == nil {
		a.credentials = make(map[string]*issuedProbeCredential)
	}
}

func (a *ProbeAuthority) pruneExpiredLocked(now time.Time, keepProbeID string) {
	for probeID, credential := range a.credentials {
		if probeID != keepProbeID && !now.Before(credential.expiresAt) {
			delete(a.credentials, probeID)
		}
	}
}

func decodeProbePacket(packet []byte, expectedKind string) (probeWirePacket, []byte, error) {
	var wire probeWirePacket
	if err := unmarshalNonQUICPacket(packet, &wire, ErrProbeMalformed); err != nil {
		return probeWirePacket{}, nil, err
	}
	if wire.Kind != expectedKind || wire.Version != rendezvousVersion || strings.TrimSpace(wire.ProbeID) == "" || len(wire.ProbeID) > maxRendezvousStringLen || wire.UnixMillis == 0 {
		return probeWirePacket{}, nil, ErrProbeMalformed
	}
	if expectedKind == probeRequestKind && wire.ObservedAddress != "" {
		return probeWirePacket{}, nil, ErrProbeMalformed
	}
	if expectedKind == probeResponseKind && wire.ObservedAddress == "" {
		return probeWirePacket{}, nil, ErrProbeMalformed
	}
	nonce, err := decodeNonce(wire.Nonce, ProbeNonceSize)
	if err != nil {
		return probeWirePacket{}, nil, fmt.Errorf("%w: %v", ErrProbeMalformed, err)
	}
	return wire, nonce, nil
}

func verifyProbeMAC(key []byte, packet probeWirePacket, nonce []byte) bool {
	want := probeMAC(key, packet.Kind, packet.Version, packet.ProbeID, packet.UnixMillis, nonce, packet.ObservedAddress)
	return validEncodedMAC(packet.HMAC, want)
}

func probeMAC(key []byte, kind string, version int, probeID string, unixMillis int64, nonce []byte, observedAddress string) []byte {
	h := hmac.New(sha256.New, key)
	writeCanonicalString(h, kind)
	writeCanonicalUint64(h, uint64(version))
	writeCanonicalString(h, probeID)
	writeCanonicalUint64(h, uint64(unixMillis))
	writeCanonicalBytes(h, nonce)
	writeCanonicalString(h, observedAddress)
	return h.Sum(nil)
}

func punchMAC(key []byte, kind string, version int, sessionID string, generation uint64, nonce []byte) []byte {
	h := hmac.New(sha256.New, key)
	writeCanonicalString(h, kind)
	writeCanonicalUint64(h, uint64(version))
	writeCanonicalString(h, sessionID)
	writeCanonicalUint64(h, generation)
	writeCanonicalBytes(h, nonce)
	return h.Sum(nil)
}

func writeCanonicalString(w io.Writer, value string) {
	writeCanonicalBytes(w, []byte(value))
}

func writeCanonicalBytes(w io.Writer, value []byte) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	_, _ = w.Write(length[:])
	_, _ = w.Write(value)
}

func writeCanonicalUint64(w io.Writer, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = w.Write(encoded[:])
}

func marshalNonQUICPacket(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(encoded)+1 > maxRendezvousPacketLen {
		return nil, errors.New("rendezvous packet is too large")
	}
	return append([]byte{nonQUICMarker}, encoded...), nil
}

func unmarshalNonQUICPacket(packet []byte, value any, malformed error) error {
	if len(packet) < 2 || len(packet) > maxRendezvousPacketLen || packet[0] != nonQUICMarker {
		return malformed
	}
	decoder := json.NewDecoder(bytes.NewReader(packet[1:]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("%w: %v", malformed, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing data", malformed)
	}
	return nil
}

func decodeNonce(encoded string, size int) ([]byte, error) {
	decoded, err := hex.DecodeString(encoded)
	if err != nil || len(decoded) != size || hex.EncodeToString(decoded) != encoded {
		return nil, fmt.Errorf("nonce must be canonical %d-byte hex", size)
	}
	return decoded, nil
}

func encodeSecret(secret []byte) string {
	return hex.EncodeToString(secret)
}

func decodeSecret(encoded string) ([]byte, error) {
	if decoded, err := hex.DecodeString(encoded); err == nil && len(decoded) == sharedSecretSize && hex.EncodeToString(decoded) == encoded {
		return decoded, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(decoded) != sharedSecretSize || base64.RawURLEncoding.EncodeToString(decoded) != encoded {
		return nil, errors.New("secret must encode exactly 32 bytes")
	}
	return decoded, nil
}

func encodeMAC(mac []byte) string {
	return hex.EncodeToString(mac)
}

func validEncodedMAC(encoded string, want []byte) bool {
	got, err := hex.DecodeString(encoded)
	return err == nil && len(got) == sha256.Size && hex.EncodeToString(got) == encoded && hmac.Equal(got, want)
}

func addrPortFromNetAddr(addr net.Addr) (netip.AddrPort, error) {
	if addr == nil {
		return netip.AddrPort{}, errors.New("address is nil")
	}
	if udpAddr, ok := addr.(*net.UDPAddr); ok {
		value := udpAddr.AddrPort()
		ip := value.Addr().Unmap()
		if !ip.Is4() || value.Port() == 0 {
			return netip.AddrPort{}, errors.New("address must be IPv4 with a non-zero port")
		}
		return netip.AddrPortFrom(ip, value.Port()), nil
	}
	value, err := netip.ParseAddrPort(addr.String())
	if err != nil {
		return netip.AddrPort{}, err
	}
	ip := value.Addr().Unmap()
	if !ip.Is4() || value.Port() == 0 {
		return netip.AddrPort{}, errors.New("address must be IPv4 with a non-zero port")
	}
	return netip.AddrPortFrom(ip, value.Port()), nil
}
