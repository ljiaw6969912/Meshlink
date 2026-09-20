package p2p

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

func TestFragmentRoundTripOutOfOrderWithinOneThousandBytes(t *testing.T) {
	packet := []byte("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if got, want := len(packet), 1280; got != want {
		t.Fatalf("literal packet length = %d, want %d", got, want)
	}

	fragments, err := FragmentPacket(0x0102030405060708, packet, 1280)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(fragments), 2; got != want {
		t.Fatalf("fragment count = %d, want %d", got, want)
	}
	for i, fragment := range fragments {
		if len(fragment) > 1000 {
			t.Fatalf("fragment %d length = %d, want <= 1000", i, len(fragment))
		}
	}
	wantFirstHeader := []byte{
		0x01,
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x00, 0x00,
		0x00, 0x02,
		0x05, 0x00,
		0x03, 0xd7,
	}
	if !bytes.Equal(fragments[0][:17], wantFirstHeader) {
		t.Fatalf("first header = %x, want %x", fragments[0][:17], wantFirstHeader)
	}
	wantSecondHeader := []byte{
		0x01,
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x00, 0x01,
		0x00, 0x02,
		0x05, 0x00,
		0x01, 0x29,
	}
	if !bytes.Equal(fragments[1][:17], wantSecondHeader) {
		t.Fatalf("second header = %x, want %x", fragments[1][:17], wantSecondHeader)
	}

	reassembler := NewReassembler(1280)
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	if assembled, complete, err := reassembler.Add("peer-c", fragments[1], now); err != nil || complete || assembled != nil {
		t.Fatalf("first out-of-order add = (%x, %t, %v), want (nil, false, nil)", assembled, complete, err)
	}
	assembled, complete, err := reassembler.Add("peer-c", fragments[0], now.Add(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Fatal("out-of-order fragments did not complete")
	}
	if !bytes.Equal(assembled, packet) {
		t.Fatalf("assembled packet differs: got %x, want %x", assembled, packet)
	}
}

func TestFragmentPacketRejectsPacketsOutsideConfiguredAndHardBounds(t *testing.T) {
	tests := []struct {
		name   string
		packet []byte
		mtu    int
	}{
		{name: "empty packet", packet: []byte{}, mtu: 1280},
		{name: "invalid mtu", packet: []byte{0x45}, mtu: 0},
		{name: "configured mtu", packet: bytes.Repeat([]byte{0x45}, 1281), mtu: 1280},
		{name: "hard maximum", packet: bytes.Repeat([]byte{0x45}, 9001), mtu: 9001},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := FragmentPacket(7, tt.packet, tt.mtu); err == nil {
				t.Fatal("FragmentPacket returned nil error")
			}
		})
	}
}

func TestReassemblerRejectsConflictingDuplicateAndExpiresMissingFragments(t *testing.T) {
	reassembler := NewReassembler(1280)
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	first := fragmentFixture(42, 0, 2, 4, []byte{0x61, 0x62})
	identical := append([]byte(nil), first...)
	conflicting := fragmentFixture(42, 0, 2, 4, []byte{0x78, 0x79})

	if _, complete, err := reassembler.Add("peer-c", first, now); err != nil || complete {
		t.Fatalf("first add complete=%t err=%v, want false and nil", complete, err)
	}
	if _, complete, err := reassembler.Add("peer-c", identical, now.Add(time.Millisecond)); err != nil || complete {
		t.Fatalf("identical duplicate complete=%t err=%v, want false and nil", complete, err)
	}
	if _, _, err := reassembler.Add("peer-c", conflicting, now.Add(2*time.Millisecond)); !errors.Is(err, ErrConflictingFragment) {
		t.Fatalf("conflicting duplicate error = %v, want ErrConflictingFragment", err)
	}
	peerUsage, globalUsage := reassembler.Usage("peer-c")
	if peerUsage != (ReassemblyUsage{}) || globalUsage != (ReassemblyUsage{}) {
		t.Fatalf("usage after conflict = peer %+v global %+v, want zero", peerUsage, globalUsage)
	}

	missingSecond := fragmentFixture(43, 0, 2, 4, []byte{0x01, 0x02})
	if _, complete, err := reassembler.Add("peer-c", missingSecond, now); err != nil || complete {
		t.Fatalf("partial add complete=%t err=%v, want false and nil", complete, err)
	}
	reassembler.Expire(now.Add(4 * time.Second))
	peerUsage, globalUsage = reassembler.Usage("peer-c")
	if peerUsage != (ReassemblyUsage{}) || globalUsage != (ReassemblyUsage{}) {
		t.Fatalf("usage after expiry = peer %+v global %+v, want zero", peerUsage, globalUsage)
	}
	lateSecond := fragmentFixture(43, 1, 2, 4, []byte{0x03, 0x04})
	if assembled, complete, err := reassembler.Add("peer-c", lateSecond, now.Add(4*time.Second)); err != nil || complete || assembled != nil {
		t.Fatalf("late fragment = (%x, %t, %v), want (nil, false, nil)", assembled, complete, err)
	}
}

func TestReassemblerEnforcesPerPeerAndGlobalMemoryBounds(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

	t.Run("per-peer packet count", func(t *testing.T) {
		limits := ReassemblyLimits{Expiry: 3 * time.Second, PerPeerPackets: 2, PerPeerBytes: 64, GlobalPackets: 8, GlobalBytes: 256}
		reassembler := mustLimitedReassembler(t, limits)
		peerUsage, globalUsage := assertOldestEvicted(t, reassembler, "peer-a", "peer-a", "peer-a", now, []byte{0x11}, []byte{0x21}, []byte{0x31}, []byte{0x21, 0xf2})
		if peerUsage != (ReassemblyUsage{Packets: 2, Bytes: 2}) || globalUsage != (ReassemblyUsage{Packets: 2, Bytes: 2}) {
			t.Fatalf("usage = peer %+v global %+v, want 2 packets and 2 bytes", peerUsage, globalUsage)
		}
	})

	t.Run("per-peer bytes", func(t *testing.T) {
		limits := ReassemblyLimits{Expiry: 3 * time.Second, PerPeerPackets: 8, PerPeerBytes: 4, GlobalPackets: 8, GlobalBytes: 256}
		reassembler := mustLimitedReassembler(t, limits)
		peerUsage, globalUsage := assertOldestEvicted(t, reassembler, "peer-a", "peer-a", "peer-a", now, []byte{0x11, 0x12}, []byte{0x21, 0x22}, []byte{0x31, 0x32}, []byte{0x21, 0x22, 0xf2})
		if peerUsage != (ReassemblyUsage{Packets: 2, Bytes: 4}) || globalUsage != (ReassemblyUsage{Packets: 2, Bytes: 4}) {
			t.Fatalf("usage = peer %+v global %+v, want 2 packets and 4 bytes", peerUsage, globalUsage)
		}
	})

	t.Run("global packet count", func(t *testing.T) {
		limits := ReassemblyLimits{Expiry: 3 * time.Second, PerPeerPackets: 8, PerPeerBytes: 64, GlobalPackets: 2, GlobalBytes: 256}
		reassembler := mustLimitedReassembler(t, limits)
		_, globalUsage := assertOldestEvicted(t, reassembler, "peer-a", "peer-b", "peer-c", now, []byte{0x11}, []byte{0x21}, []byte{0x31}, []byte{0x21, 0xf2})
		if globalUsage != (ReassemblyUsage{Packets: 2, Bytes: 2}) {
			t.Fatalf("global usage = %+v, want 2 packets and 2 bytes", globalUsage)
		}
	})

	t.Run("global bytes", func(t *testing.T) {
		limits := ReassemblyLimits{Expiry: 3 * time.Second, PerPeerPackets: 8, PerPeerBytes: 64, GlobalPackets: 8, GlobalBytes: 4}
		reassembler := mustLimitedReassembler(t, limits)
		_, globalUsage := assertOldestEvicted(t, reassembler, "peer-a", "peer-b", "peer-c", now, []byte{0x11, 0x12}, []byte{0x21, 0x22}, []byte{0x31, 0x32}, []byte{0x21, 0x22, 0xf2})
		if globalUsage != (ReassemblyUsage{Packets: 2, Bytes: 4}) {
			t.Fatalf("global usage = %+v, want 2 packets and 4 bytes", globalUsage)
		}
	})
}

func TestReassemblerRejectsMalformedFragments(t *testing.T) {
	valid := fragmentFixture(9, 0, 2, 4, []byte{0x01, 0x02})
	wrongVersion := append([]byte(nil), valid...)
	wrongVersion[0] = 2
	lengthMismatch := append([]byte(nil), valid[:len(valid)-1]...)
	oversized := fragmentFixture(9, 0, 1, 984, bytes.Repeat([]byte{0x01}, 984))

	tests := []struct {
		name     string
		peerID   string
		fragment []byte
		mtu      int
	}{
		{name: "empty peer", peerID: "", fragment: valid, mtu: 1280},
		{name: "short header", peerID: "peer-c", fragment: []byte{0x01}, mtu: 1280},
		{name: "wrong version", peerID: "peer-c", fragment: wrongVersion, mtu: 1280},
		{name: "zero count", peerID: "peer-c", fragment: fragmentFixture(9, 0, 0, 4, []byte{0x01}), mtu: 1280},
		{name: "too many fragments", peerID: "peer-c", fragment: fragmentFixture(9, 0, 17, 17, []byte{0x01}), mtu: 1280},
		{name: "index outside count", peerID: "peer-c", fragment: fragmentFixture(9, 2, 2, 4, []byte{0x01}), mtu: 1280},
		{name: "above configured mtu", peerID: "peer-c", fragment: fragmentFixture(9, 0, 2, 1281, []byte{0x01}), mtu: 1280},
		{name: "above hard maximum", peerID: "peer-c", fragment: fragmentFixture(9, 0, 10, 9001, []byte{0x01}), mtu: 9001},
		{name: "encoded length mismatch", peerID: "peer-c", fragment: lengthMismatch, mtu: 1280},
		{name: "encoded fragment too large", peerID: "peer-c", fragment: oversized, mtu: 1280},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reassembler := NewReassembler(tt.mtu)
			if _, _, err := reassembler.Add(tt.peerID, tt.fragment, time.Unix(0, 0)); err == nil {
				t.Fatal("Add returned nil error")
			}
		})
	}

	reassembler := NewReassembler(1280)
	if _, _, err := reassembler.Add("peer-c", valid, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	inconsistent := fragmentFixture(9, 1, 3, 4, []byte{0x03, 0x04})
	if _, _, err := reassembler.Add("peer-c", inconsistent, time.Unix(0, 1)); err == nil {
		t.Fatal("Add accepted inconsistent fragment metadata")
	}
}

func mustLimitedReassembler(t *testing.T, limits ReassemblyLimits) *Reassembler {
	t.Helper()
	reassembler, err := NewReassemblerWithLimits(1280, limits)
	if err != nil {
		t.Fatal(err)
	}
	return reassembler
}

func assertOldestEvicted(t *testing.T, reassembler *Reassembler, firstPeer, secondPeer, thirdPeer string, now time.Time, firstPayload, secondPayload, thirdPayload, wantSecond []byte) (ReassemblyUsage, ReassemblyUsage) {
	t.Helper()
	first := fragmentFixture(1, 0, 2, uint16(len(firstPayload)+1), firstPayload)
	second := fragmentFixture(2, 0, 2, uint16(len(secondPayload)+1), secondPayload)
	third := fragmentFixture(3, 0, 2, uint16(len(thirdPayload)+1), thirdPayload)
	for i, add := range []struct {
		peer     string
		fragment []byte
	}{{firstPeer, first}, {secondPeer, second}, {thirdPeer, third}} {
		if _, complete, err := reassembler.Add(add.peer, add.fragment, now.Add(time.Duration(i)*time.Millisecond)); err != nil || complete {
			t.Fatalf("add %d complete=%t err=%v, want false and nil", i, complete, err)
		}
	}
	peerUsage, globalUsage := reassembler.Usage(thirdPeer)

	secondTail := fragmentFixture(2, 1, 2, uint16(len(secondPayload)+1), []byte{0xf2})
	assembled, complete, err := reassembler.Add(secondPeer, secondTail, now.Add(4*time.Millisecond))
	if err != nil || !complete {
		t.Fatalf("second packet completion = (%x, %t, %v), want complete", assembled, complete, err)
	}
	if !bytes.Equal(assembled, wantSecond) {
		t.Fatalf("second packet = %x, want %x", assembled, wantSecond)
	}

	firstTail := fragmentFixture(1, 1, 2, uint16(len(firstPayload)+1), []byte{0xf1})
	if assembled, complete, err := reassembler.Add(firstPeer, firstTail, now.Add(5*time.Millisecond)); err != nil || complete || assembled != nil {
		t.Fatalf("evicted first packet completion = (%x, %t, %v), want incomplete", assembled, complete, err)
	}
	return peerUsage, globalUsage
}

func fragmentFixture(packetID uint64, index, count, originalLength uint16, payload []byte) []byte {
	fragment := make([]byte, 17+len(payload))
	fragment[0] = 1
	binary.BigEndian.PutUint64(fragment[1:9], packetID)
	binary.BigEndian.PutUint16(fragment[9:11], index)
	binary.BigEndian.PutUint16(fragment[11:13], count)
	binary.BigEndian.PutUint16(fragment[13:15], originalLength)
	binary.BigEndian.PutUint16(fragment[15:17], uint16(len(payload)))
	copy(fragment[17:], payload)
	return fragment
}
