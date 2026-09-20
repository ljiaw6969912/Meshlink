package proto

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"time"
)

const (
	ControlProtocolVersion = 2

	ControlTypeClientHello     = "client_hello"
	ControlTypeServerHello     = "server_hello"
	ControlTypeProbeCredential = "probe_credential"
	ControlTypeCandidateUpdate = "candidate_update"
	ControlTypeMemberSnapshot  = "member_snapshot"
	ControlTypeMemberDelta     = "member_delta"
	ControlTypeConnectRequest  = "connect_request"
	ControlTypeConnectPrepare  = "connect_prepare"
	ControlTypeConnectReady    = "connect_ready"
	ControlTypeSessionOffer    = "session_offer"
	ControlTypeSessionOfferAck = "session_offer_ack"
	ControlTypeSessionStart    = "session_start"
	ControlTypeSessionAbort    = "session_abort"
	ControlTypeSessionResult   = "session_result"
	ControlTypeActiveSessions  = "active_sessions"
	ControlTypeDisconnectPeer  = "disconnect_peer"
	ControlTypePing            = "ping"
	ControlTypePong            = "pong"
	ControlTypeError           = "error"
)

// ControlEnvelope is the only JSON body carried by a TypeControl frame.
type ControlEnvelope struct {
	ProtocolVersion int             `json:"protocol_version"`
	Type            string          `json:"type"`
	RequestID       string          `json:"request_id,omitempty"`
	Body            json.RawMessage `json:"body"`
}

type ServerHello struct {
	ProtocolVersion int      `json:"protocol_version"`
	NetworkCIDR     string   `json:"network_cidr"`
	MemberRevision  uint64   `json:"member_revision"`
	Capabilities    []string `json:"capabilities"`
}

type ProbeCredential struct {
	ProbeID   string    `json:"probe_id"`
	Key       string    `json:"key"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Candidate intentionally contains no node identity or physical-address bundle.
// It is disclosed only inside an authorized session offer.
type Candidate struct {
	Address   string    `json:"address"`
	Port      uint16    `json:"port"`
	Scope     string    `json:"scope"`
	Priority  int       `json:"priority"`
	ExpiresAt time.Time `json:"expires_at"`
}

type CandidateUpdate struct {
	Revision   uint64      `json:"revision"`
	Candidates []Candidate `json:"candidates"`
}

// Member is safe to place in membership snapshots: it never includes a
// candidate, socket, probe credential, or any other physical address.
type Member struct {
	NodeID      string   `json:"node_id"`
	VirtualIP   string   `json:"virtual_ip"`
	Routes      []string `json:"routes"`
	Status      string   `json:"status"`
	Fingerprint string   `json:"fingerprint"`
}

type MemberSnapshot struct {
	Revision uint64   `json:"revision"`
	Members  []Member `json:"members"`
}

type MemberDelta struct {
	Revision       uint64   `json:"revision"`
	Members        []Member `json:"members"`
	RemovedNodeIDs []string `json:"removed_node_ids"`
}

type ConnectRequest struct {
	TargetNodeID string `json:"target_node_id"`
}

type ConnectPrepare struct {
	SessionID  string `json:"session_id"`
	Generation uint64 `json:"generation"`
	PeerNodeID string `json:"peer_node_id"`
	RequestID  string `json:"request_id"`
}

type ConnectReady struct {
	SessionID         string `json:"session_id"`
	Generation        uint64 `json:"generation"`
	CandidateRevision uint64 `json:"candidate_revision"`
	Ready             bool   `json:"ready"`
}

type SessionOffer struct {
	SessionID       string      `json:"session_id"`
	Generation      uint64      `json:"generation"`
	PeerNodeID      string      `json:"peer_node_id"`
	PeerFingerprint string      `json:"peer_fingerprint"`
	DialerNodeID    string      `json:"dialer_node_id"`
	Candidates      []Candidate `json:"candidates"`
	ExpiresAt       time.Time   `json:"expires_at"`
	PairingKey      string      `json:"pairing_key"`
}

type SessionOfferAck struct {
	SessionID  string `json:"session_id"`
	Generation uint64 `json:"generation"`
	Ready      bool   `json:"ready"`
}

type SessionStart struct {
	SessionID  string `json:"session_id"`
	Generation uint64 `json:"generation"`
}

type SessionAbort struct {
	SessionID  string `json:"session_id"`
	Generation uint64 `json:"generation"`
	Code       string `json:"code"`
}

type SessionResult struct {
	SessionID  string `json:"session_id"`
	Generation uint64 `json:"generation"`
	Success    bool   `json:"success"`
	Code       string `json:"code,omitempty"`
}

type ActiveSession struct {
	SessionID  string `json:"session_id"`
	Generation uint64 `json:"generation"`
	PeerNodeID string `json:"peer_node_id"`
	PathType   string `json:"path_type"`
}

type ActiveSessions struct {
	Sessions []ActiveSession `json:"sessions"`
}

type DisconnectPeer struct {
	NodeID string `json:"node_id"`
	Code   string `json:"code"`
}

type ControlPing struct {
	Nonce string `json:"nonce"`
}

type ControlPong struct {
	Nonce string `json:"nonce"`
}

type ControlError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

var controlBodyTypes = map[string]reflect.Type{
	ControlTypeClientHello:     reflect.TypeFor[ClientHello](),
	ControlTypeServerHello:     reflect.TypeFor[ServerHello](),
	ControlTypeProbeCredential: reflect.TypeFor[ProbeCredential](),
	ControlTypeCandidateUpdate: reflect.TypeFor[CandidateUpdate](),
	ControlTypeMemberSnapshot:  reflect.TypeFor[MemberSnapshot](),
	ControlTypeMemberDelta:     reflect.TypeFor[MemberDelta](),
	ControlTypeConnectRequest:  reflect.TypeFor[ConnectRequest](),
	ControlTypeConnectPrepare:  reflect.TypeFor[ConnectPrepare](),
	ControlTypeConnectReady:    reflect.TypeFor[ConnectReady](),
	ControlTypeSessionOffer:    reflect.TypeFor[SessionOffer](),
	ControlTypeSessionOfferAck: reflect.TypeFor[SessionOfferAck](),
	ControlTypeSessionStart:    reflect.TypeFor[SessionStart](),
	ControlTypeSessionAbort:    reflect.TypeFor[SessionAbort](),
	ControlTypeSessionResult:   reflect.TypeFor[SessionResult](),
	ControlTypeActiveSessions:  reflect.TypeFor[ActiveSessions](),
	ControlTypeDisconnectPeer:  reflect.TypeFor[DisconnectPeer](),
	ControlTypePing:            reflect.TypeFor[ControlPing](),
	ControlTypePong:            reflect.TypeFor[ControlPong](),
	ControlTypeError:           reflect.TypeFor[ControlError](),
}

// MarshalControl builds a bounded, version-two envelope and rejects accidental
// type/body mismatches before they reach the control connection.
func MarshalControl(messageType, requestID string, body any) ([]byte, error) {
	want, ok := controlBodyTypes[messageType]
	if !ok || messageType == "" {
		return nil, fmt.Errorf("unknown control type %q", messageType)
	}
	if body == nil || reflect.TypeOf(body) != want {
		return nil, fmt.Errorf("control type %q requires body %s", messageType, want)
	}
	bodyPayload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal control body %q: %w", messageType, err)
	}
	payload, err := json.Marshal(ControlEnvelope{
		ProtocolVersion: ControlProtocolVersion,
		Type:            messageType,
		RequestID:       requestID,
		Body:            bodyPayload,
	})
	if err != nil {
		return nil, err
	}
	if len(payload) > MaxPayload {
		return nil, fmt.Errorf("control payload too large: %d", len(payload))
	}
	return payload, nil
}

// ParseControl rejects legacy, future, malformed, and extension-bearing
// envelopes. A future version must introduce a new frame/parser instead of
// silently sharing this v2 contract.
func ParseControl(payload []byte) (ControlEnvelope, error) {
	if len(payload) > MaxPayload {
		return ControlEnvelope{}, fmt.Errorf("control payload too large: %d", len(payload))
	}
	var env ControlEnvelope
	if err := decodeStrictJSON(payload, &env); err != nil {
		return ControlEnvelope{}, fmt.Errorf("decode control envelope: %w", err)
	}
	if err := validateControlEnvelope(env); err != nil {
		return ControlEnvelope{}, err
	}
	return env, nil
}

// DecodeControlBody validates the envelope/body mapping and decodes the body
// with the same no-unknown-fields and no-trailing-data rules as the envelope.
func DecodeControlBody[T any](env ControlEnvelope) (T, error) {
	var body T
	if err := validateControlEnvelope(env); err != nil {
		return body, err
	}
	want := reflect.TypeFor[T]()
	if got := controlBodyTypes[env.Type]; got != want {
		return body, fmt.Errorf("control type %q cannot decode as %s", env.Type, want)
	}
	if err := decodeStrictJSON(env.Body, &body); err != nil {
		return body, fmt.Errorf("decode control body %q: %w", env.Type, err)
	}
	return body, nil
}

func validateControlEnvelope(env ControlEnvelope) error {
	if env.ProtocolVersion != ControlProtocolVersion {
		return fmt.Errorf("unsupported control protocol version %d", env.ProtocolVersion)
	}
	if env.Type == "" {
		return fmt.Errorf("control type is required")
	}
	if _, ok := controlBodyTypes[env.Type]; !ok {
		return fmt.Errorf("unknown control type %q", env.Type)
	}
	if len(env.Body) == 0 || bytes.Equal(bytes.TrimSpace(env.Body), []byte("null")) {
		return fmt.Errorf("control body is required")
	}
	return nil
}

func decodeStrictJSON(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return err
	}
	return nil
}
