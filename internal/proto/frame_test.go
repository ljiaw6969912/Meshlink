package proto

import (
	"bytes"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte("hello")
	if err := Write(&buf, TypeHello, payload); err != nil {
		t.Fatal(err)
	}
	frame, err := Read(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Type != TypeHello {
		t.Fatalf("type = %d, want %d", frame.Type, TypeHello)
	}
	if string(frame.Payload) != string(payload) {
		t.Fatalf("payload = %q, want %q", frame.Payload, payload)
	}
}

func TestDestinationIPv4(t *testing.T) {
	packet := []byte{
		0x45, 0, 0, 20,
		0, 0, 0, 0,
		64, 6, 0, 0,
		10, 77, 0, 2,
		10, 77, 0, 1,
	}
	dst, err := DestinationIP(packet)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := dst.String(), "10.77.0.1"; got != want {
		t.Fatalf("dst = %s, want %s", got, want)
	}
}

func TestDestinationRejectsIPv6(t *testing.T) {
	packet := []byte{
		0x60, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 1,
	}
	if _, err := DestinationIP(packet); err == nil {
		t.Fatal("expected IPv6 packet to be rejected")
	}
}
