package proto

import (
	"bytes"
	"errors"
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

func TestControlFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, TypeControl, []byte(`{"protocol_version":2,"type":"ping","body":{}}`)); err != nil {
		t.Fatal(err)
	}
	frame, err := Read(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Type != TypeControl {
		t.Fatalf("type = %d, want %d", frame.Type, TypeControl)
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

func TestSourceIPv4(t *testing.T) {
	packet := []byte{
		0x45, 0, 0, 20,
		0, 0, 0, 0,
		64, 6, 0, 0,
		10, 77, 0, 2,
		10, 77, 0, 1,
	}
	src, err := SourceIP(packet)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := src.String(), "10.77.0.2"; got != want {
		t.Fatalf("src = %s, want %s", got, want)
	}
}

func TestSourceAndDestinationRejectMalformedIPv4Headers(t *testing.T) {
	tests := []struct {
		name   string
		packet []byte
	}{
		{
			name: "IHL below five",
			packet: []byte{
				0x44, 0, 0, 20,
				0, 0, 0, 0,
				64, 6, 0, 0,
				10, 77, 0, 2,
				10, 77, 0, 1,
			},
		},
		{
			name: "options header truncated",
			packet: []byte{
				0x46, 0, 0, 24,
				0, 0, 0, 0,
				64, 6, 0, 0,
				10, 77, 0, 2,
				10, 77, 0, 1,
			},
		},
		{
			name: "total length below header",
			packet: []byte{
				0x45, 0, 0, 19,
				0, 0, 0, 0,
				64, 6, 0, 0,
				10, 77, 0, 2,
				10, 77, 0, 1,
			},
		},
		{
			name: "total length truncated",
			packet: []byte{
				0x45, 0, 0, 21,
				0, 0, 0, 0,
				64, 6, 0, 0,
				10, 77, 0, 2,
				10, 77, 0, 1,
			},
		},
		{
			name: "bytes beyond total length",
			packet: []byte{
				0x45, 0, 0, 20,
				0, 0, 0, 0,
				64, 6, 0, 0,
				10, 77, 0, 2,
				10, 77, 0, 1,
				0xff,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := SourceIP(tt.packet); !errors.Is(err, ErrNotIPPacket) {
				t.Fatalf("SourceIP error = %v, want ErrNotIPPacket", err)
			}
			if _, err := DestinationIP(tt.packet); !errors.Is(err, ErrNotIPPacket) {
				t.Fatalf("DestinationIP error = %v, want ErrNotIPPacket", err)
			}
		})
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
