package p2p

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

const (
	sessionALPN             = "meshlink-p2p/1"
	sessionWireVersion      = 1
	sessionWireMaxFrameSize = 4096
	sessionHelloNonceSize   = 32
	heartbeatNonceSize      = 16
	sessionWireTypeHello    = "hello"
	sessionWireTypePing     = "ping"
	sessionWireTypePong     = "pong"
)

var errSessionAuthorization = errors.New("session_authorization_failed")

type sessionWireMessage struct {
	Version    int    `json:"version"`
	Type       string `json:"type"`
	SessionID  string `json:"session_id,omitempty"`
	Generation uint64 `json:"generation,omitempty"`
	FromNodeID string `json:"from_node_id,omitempty"`
	ToNodeID   string `json:"to_node_id,omitempty"`
	Nonce      string `json:"nonce"`
	MAC        string `json:"mac,omitempty"`
}

type sessionWireWriter struct {
	mu sync.Mutex
}

func (w *sessionWireWriter) write(writer io.Writer, message sessionWireMessage) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return writeSessionWireMessage(writer, message)
}

func newSessionHello(sessionID string, generation uint64, fromNodeID, toNodeID string, key []byte) (sessionWireMessage, error) {
	nonce := make([]byte, sessionHelloNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return sessionWireMessage{}, fmt.Errorf("generate session hello nonce: %w", err)
	}
	message := sessionWireMessage{
		Version:    sessionWireVersion,
		Type:       sessionWireTypeHello,
		SessionID:  sessionID,
		Generation: generation,
		FromNodeID: fromNodeID,
		ToNodeID:   toNodeID,
		Nonce:      hex.EncodeToString(nonce),
	}
	message.MAC = sessionHelloMAC(message, key)
	return message, nil
}

func validateSessionHello(message sessionWireMessage, sessionID string, generation uint64, fromNodeID, toNodeID string, key []byte) error {
	if message.Version != sessionWireVersion || message.Type != sessionWireTypeHello {
		return fmt.Errorf("%w: unexpected hello version or type", errSessionAuthorization)
	}
	if message.SessionID != sessionID || message.Generation != generation || message.FromNodeID != fromNodeID || message.ToNodeID != toNodeID {
		return fmt.Errorf("%w: hello session or identity mismatch", errSessionAuthorization)
	}
	nonce, err := decodeCanonicalHex(message.Nonce, sessionHelloNonceSize)
	if err != nil {
		return fmt.Errorf("%w: %v", errSessionAuthorization, err)
	}
	_ = nonce
	want, err := decodeCanonicalHex(message.MAC, sha256.Size)
	if err != nil {
		return fmt.Errorf("%w: %v", errSessionAuthorization, err)
	}
	computed, err := hex.DecodeString(sessionHelloMAC(message, key))
	if err != nil || !hmac.Equal(want, computed) {
		return fmt.Errorf("%w: hello HMAC mismatch", errSessionAuthorization)
	}
	return nil
}

func sessionHelloMAC(message sessionWireMessage, key []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("meshlink-session-hello-v1"))
	writeSessionMACField(mac, message.SessionID)
	var generation [8]byte
	binary.BigEndian.PutUint64(generation[:], message.Generation)
	_, _ = mac.Write(generation[:])
	writeSessionMACField(mac, message.FromNodeID)
	writeSessionMACField(mac, message.ToNodeID)
	writeSessionMACField(mac, message.Nonce)
	return hex.EncodeToString(mac.Sum(nil))
}

func writeSessionMACField(writer io.Writer, value string) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = io.WriteString(writer, value)
}

func newHeartbeatMessage(messageType string) (sessionWireMessage, error) {
	if messageType != sessionWireTypePing && messageType != sessionWireTypePong {
		return sessionWireMessage{}, fmt.Errorf("invalid heartbeat type %q", messageType)
	}
	nonce := make([]byte, heartbeatNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return sessionWireMessage{}, fmt.Errorf("generate heartbeat nonce: %w", err)
	}
	return sessionWireMessage{
		Version: sessionWireVersion,
		Type:    messageType,
		Nonce:   hex.EncodeToString(nonce),
	}, nil
}

func validateHeartbeatMessage(message sessionWireMessage) error {
	if message.Version != sessionWireVersion || (message.Type != sessionWireTypePing && message.Type != sessionWireTypePong) {
		return errors.New("unexpected session control message")
	}
	if message.SessionID != "" || message.Generation != 0 || message.FromNodeID != "" || message.ToNodeID != "" || message.MAC != "" {
		return errors.New("heartbeat contains hello-only fields")
	}
	if _, err := decodeCanonicalHex(message.Nonce, heartbeatNonceSize); err != nil {
		return err
	}
	return nil
}

func writeSessionWireMessage(writer io.Writer, message sessionWireMessage) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("marshal session wire message: %w", err)
	}
	if len(payload) == 0 || len(payload) > sessionWireMaxFrameSize {
		return fmt.Errorf("session wire frame length %d exceeds bounds", len(payload))
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if err := writeFull(writer, header[:]); err != nil {
		return fmt.Errorf("write session wire header: %w", err)
	}
	if err := writeFull(writer, payload); err != nil {
		return fmt.Errorf("write session wire payload: %w", err)
	}
	return nil
}

func readSessionWireMessage(reader io.Reader) (sessionWireMessage, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return sessionWireMessage{}, fmt.Errorf("read session wire header: %w", err)
	}
	length := int(binary.BigEndian.Uint32(header[:]))
	if length <= 0 || length > sessionWireMaxFrameSize {
		return sessionWireMessage{}, fmt.Errorf("session wire frame length %d exceeds bounds", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return sessionWireMessage{}, fmt.Errorf("read session wire payload: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var message sessionWireMessage
	if err := decoder.Decode(&message); err != nil {
		return sessionWireMessage{}, fmt.Errorf("decode session wire message: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return sessionWireMessage{}, errors.New("decode session wire message: trailing JSON")
	}
	if message.Version != sessionWireVersion || strings.TrimSpace(message.Type) == "" {
		return sessionWireMessage{}, errors.New("invalid session wire version or type")
	}
	return message, nil
}

func exchangeSessionHello(ctx context.Context, stream interface {
	io.Reader
	io.Writer
	SetDeadline(time.Time) error
}, writer *sessionWireWriter, sessionID string, generation uint64, localNodeID, peerNodeID string, key []byte) error {
	deadline := time.Now().Add(10 * time.Second)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := stream.SetDeadline(deadline); err != nil {
		return fmt.Errorf("set SessionHello deadline: %w", err)
	}
	defer func() { _ = stream.SetDeadline(time.Time{}) }()
	local, err := newSessionHello(sessionID, generation, localNodeID, peerNodeID, key)
	if err != nil {
		return err
	}
	if err := writer.write(stream, local); err != nil {
		return err
	}
	remote, err := readSessionWireMessage(stream)
	if err != nil {
		return err
	}
	if err := validateSessionHello(remote, sessionID, generation, peerNodeID, localNodeID, key); err != nil {
		return err
	}
	return nil
}

func decodeCanonicalHex(value string, size int) ([]byte, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != size || hex.EncodeToString(decoded) != value {
		return nil, fmt.Errorf("value must be canonical %d-byte hex", size)
	}
	return decoded, nil
}

func writeFull(writer io.Writer, payload []byte) error {
	for len(payload) > 0 {
		written, err := writer.Write(payload)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		payload = payload[written:]
	}
	return nil
}
