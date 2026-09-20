package p2p

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"testing"
	"time"
)

func TestProbeAuthorityAuthenticatesExpiryAndNonceReplay(t *testing.T) {
	clock := newTestClock(time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC))
	authority := NewProbeAuthority(clock.Now)
	credential, err := authority.Issue()
	if err != nil {
		t.Fatalf("issue credential: %v", err)
	}
	if got, want := credential.ExpiresAt, clock.Now().Add(90*time.Second); !got.Equal(want) {
		t.Fatalf("credential expiry = %s, want %s", got, want)
	}

	nonce := bytes.Repeat([]byte{0x11}, ProbeNonceSize)
	request, err := EncodeProbeRequest(credential, clock.Now(), nonce)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	if request[0] != 0x00 {
		t.Fatalf("request first byte = %#x, want non-QUIC marker 0x00", request[0])
	}
	source := &net.UDPAddr{IP: net.ParseIP("203.0.113.7"), Port: 45678}
	if _, err := authority.Handle(request, source); err != nil {
		t.Fatalf("handle authenticated request: %v", err)
	}
	if _, err := authority.Handle(request, source); !errors.Is(err, ErrProbeReplay) {
		t.Fatalf("replayed request error = %v, want %v", err, ErrProbeReplay)
	}

	newNonce := bytes.Repeat([]byte{0x22}, ProbeNonceSize)
	requestBeforeExpiry, err := EncodeProbeRequest(credential, credential.ExpiresAt.Add(-time.Millisecond), newNonce)
	if err != nil {
		t.Fatalf("encode request before expiry: %v", err)
	}
	clock.Set(credential.ExpiresAt.Add(time.Millisecond))
	if _, err := authority.Handle(requestBeforeExpiry, source); !errors.Is(err, ErrProbeExpired) {
		t.Fatalf("expired credential error = %v, want %v", err, ErrProbeExpired)
	}
}

func TestProbeAuthorityBoundsNonceReplayState(t *testing.T) {
	now := time.Date(2026, 9, 14, 8, 15, 0, 0, time.UTC)
	authority := NewProbeAuthority(func() time.Time { return now })
	credential, err := authority.Issue()
	if err != nil {
		t.Fatalf("issue credential: %v", err)
	}
	source := &net.UDPAddr{IP: net.ParseIP("203.0.113.9"), Port: 45679}

	for sequence := uint64(0); sequence < MaxProbeNoncesPerCredential; sequence++ {
		nonce := make([]byte, ProbeNonceSize)
		binary.BigEndian.PutUint64(nonce[ProbeNonceSize-8:], sequence)
		request, err := EncodeProbeRequest(credential, now, nonce)
		if err != nil {
			t.Fatalf("encode request %d: %v", sequence, err)
		}
		if _, err := authority.Handle(request, source); err != nil {
			t.Fatalf("handle request %d: %v", sequence, err)
		}
	}

	overflowNonce := make([]byte, ProbeNonceSize)
	binary.BigEndian.PutUint64(overflowNonce[ProbeNonceSize-8:], MaxProbeNoncesPerCredential)
	overflow, err := EncodeProbeRequest(credential, now, overflowNonce)
	if err != nil {
		t.Fatalf("encode overflow request: %v", err)
	}
	if _, err := authority.Handle(overflow, source); !errors.Is(err, ErrProbeNonceLimit) {
		t.Fatalf("overflow request error = %v, want %v", err, ErrProbeNonceLimit)
	}
	if _, err := authority.Handle(overflow, source); !errors.Is(err, ErrProbeAuthentication) {
		t.Fatalf("request after credential revocation error = %v, want %v", err, ErrProbeAuthentication)
	}
}

func TestProbeResponseBindsObservedSourceAddress(t *testing.T) {
	now := time.Date(2026, 9, 14, 8, 30, 0, 0, time.UTC)
	authority := NewProbeAuthority(func() time.Time { return now })
	credential, err := authority.Issue()
	if err != nil {
		t.Fatalf("issue credential: %v", err)
	}
	nonce := bytes.Repeat([]byte{0x33}, ProbeNonceSize)
	request, err := EncodeProbeRequest(credential, now, nonce)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	response, err := authority.Handle(request, &net.UDPAddr{IP: net.ParseIP("203.0.113.7"), Port: 45678})
	if err != nil {
		t.Fatalf("handle request: %v", err)
	}
	if response[0] != 0x00 {
		t.Fatalf("response first byte = %#x, want non-QUIC marker 0x00", response[0])
	}

	decoded, err := DecodeProbeResponse(response, credential, now)
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got, want := decoded.ObservedAddress.String(), "203.0.113.7:45678"; got != want {
		t.Fatalf("observed address = %q, want %q", got, want)
	}
	if !bytes.Equal(decoded.Nonce, nonce) {
		t.Fatalf("response nonce = %x, want %x", decoded.Nonce, nonce)
	}

	tampered := bytes.Replace(response, []byte("203.0.113.7:45678"), []byte("203.0.113.8:45678"), 1)
	if bytes.Equal(tampered, response) {
		t.Fatal("test setup did not alter the observed address")
	}
	if _, err := DecodeProbeResponse(tampered, credential, now); !errors.Is(err, ErrProbeAuthentication) {
		t.Fatalf("tampered response error = %v, want %v", err, ErrProbeAuthentication)
	}
}
