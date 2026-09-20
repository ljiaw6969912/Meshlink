package p2p

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	DatagramProtocolVersion byte = 1
	FragmentHeaderSize           = 17
	MaxEncodedFragmentSize       = 1000
	MaxFragmentPayloadSize       = MaxEncodedFragmentSize - FragmentHeaderSize
	MaxFragmentsPerPacket        = 16
	MaxOriginalPacketSize        = 9000

	DefaultReassemblyExpiry         = 3 * time.Second
	DefaultReassemblyPacketsPerPeer = 64
	DefaultReassemblyBytesPerPeer   = 256 << 10
	DefaultReassemblyPacketsGlobal  = 1024
	DefaultReassemblyBytesGlobal    = 8 << 20
)

var (
	ErrInvalidFragment     = errors.New("invalid datagram fragment")
	ErrConflictingFragment = errors.New("conflicting datagram fragment")
	ErrReassemblyLimit     = errors.New("reassembly limit exceeded")
)

// ReassemblyLimits bounds incomplete packets retained by a Reassembler.
type ReassemblyLimits struct {
	Expiry         time.Duration
	PerPeerPackets int
	PerPeerBytes   int
	GlobalPackets  int
	GlobalBytes    int
}

// ReassemblyUsage reports retained incomplete packets and fragment payload bytes.
type ReassemblyUsage struct {
	Packets int
	Bytes   int
}

type fragmentHeader struct {
	packetID       uint64
	index          uint16
	count          uint16
	originalLength int
}

type reassemblyKey struct {
	peerID   string
	packetID uint64
}

type incompletePacket struct {
	key            reassemblyKey
	fragmentCount  uint16
	originalLength int
	fragments      [][]byte
	received       int
	bytes          int
	createdAt      time.Time
	sequence       uint64
}

// Reassembler retains incomplete packets only within fixed time, count, and byte limits.
type Reassembler struct {
	mu sync.Mutex

	mtu      int
	limits   ReassemblyLimits
	packets  map[reassemblyKey]*incompletePacket
	perPeer  map[string]ReassemblyUsage
	global   ReassemblyUsage
	sequence uint64
}

// FragmentPacket encodes packet into bounded datagram fragments.
func FragmentPacket(packetID uint64, packet []byte, mtu int) ([][]byte, error) {
	if mtu <= 0 {
		return nil, fmt.Errorf("packet MTU must be positive: %d", mtu)
	}
	if len(packet) == 0 {
		return nil, fmt.Errorf("packet length must be positive")
	}
	if len(packet) > mtu {
		return nil, fmt.Errorf("packet length %d exceeds configured MTU %d", len(packet), mtu)
	}
	if len(packet) > MaxOriginalPacketSize {
		return nil, fmt.Errorf("packet length %d exceeds hard maximum %d", len(packet), MaxOriginalPacketSize)
	}

	fragmentCount := (len(packet) + MaxFragmentPayloadSize - 1) / MaxFragmentPayloadSize
	if fragmentCount == 0 || fragmentCount > MaxFragmentsPerPacket {
		return nil, fmt.Errorf("packet requires %d fragments, maximum is %d", fragmentCount, MaxFragmentsPerPacket)
	}

	fragments := make([][]byte, 0, fragmentCount)
	for index, offset := 0, 0; offset < len(packet); index++ {
		payloadLength := min(MaxFragmentPayloadSize, len(packet)-offset)
		fragment := make([]byte, FragmentHeaderSize+payloadLength)
		fragment[0] = DatagramProtocolVersion
		binary.BigEndian.PutUint64(fragment[1:9], packetID)
		binary.BigEndian.PutUint16(fragment[9:11], uint16(index))
		binary.BigEndian.PutUint16(fragment[11:13], uint16(fragmentCount))
		binary.BigEndian.PutUint16(fragment[13:15], uint16(len(packet)))
		binary.BigEndian.PutUint16(fragment[15:17], uint16(payloadLength))
		copy(fragment[FragmentHeaderSize:], packet[offset:offset+payloadLength])
		fragments = append(fragments, fragment)
		offset += payloadLength
	}
	return fragments, nil
}

// DefaultReassemblyLimits returns the protocol's fixed cache bounds.
func DefaultReassemblyLimits() ReassemblyLimits {
	return ReassemblyLimits{
		Expiry:         DefaultReassemblyExpiry,
		PerPeerPackets: DefaultReassemblyPacketsPerPeer,
		PerPeerBytes:   DefaultReassemblyBytesPerPeer,
		GlobalPackets:  DefaultReassemblyPacketsGlobal,
		GlobalBytes:    DefaultReassemblyBytesGlobal,
	}
}

func NewReassembler(mtu int) *Reassembler {
	return &Reassembler{
		mtu:     mtu,
		limits:  DefaultReassemblyLimits(),
		packets: make(map[reassemblyKey]*incompletePacket),
		perPeer: make(map[string]ReassemblyUsage),
	}
}

func NewReassemblerWithLimits(mtu int, limits ReassemblyLimits) (*Reassembler, error) {
	if mtu <= 0 {
		return nil, fmt.Errorf("reassembly MTU must be positive: %d", mtu)
	}
	if err := validateReassemblyLimits(limits); err != nil {
		return nil, err
	}
	return &Reassembler{
		mtu:     mtu,
		limits:  limits,
		packets: make(map[reassemblyKey]*incompletePacket),
		perPeer: make(map[string]ReassemblyUsage),
	}, nil
}

func validateReassemblyLimits(limits ReassemblyLimits) error {
	if limits.Expiry <= 0 {
		return fmt.Errorf("reassembly expiry must be positive: %s", limits.Expiry)
	}
	if limits.PerPeerPackets <= 0 || limits.PerPeerBytes <= 0 {
		return fmt.Errorf("per-peer reassembly limits must be positive")
	}
	if limits.GlobalPackets <= 0 || limits.GlobalBytes <= 0 {
		return fmt.Errorf("global reassembly limits must be positive")
	}
	return nil
}

// Add retains one fragment and returns a packet only when every fragment arrived.
func (r *Reassembler) Add(peerID string, fragment []byte, now time.Time) ([]byte, bool, error) {
	if peerID == "" {
		return nil, false, fmt.Errorf("%w: peer ID is required", ErrInvalidFragment)
	}
	if r == nil {
		return nil, false, fmt.Errorf("%w: nil reassembler", ErrInvalidFragment)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.initializeLocked()
	r.expireLocked(now)

	header, payload, err := parseFragment(fragment, r.mtu)
	if err != nil {
		return nil, false, err
	}
	if header.originalLength > r.limits.PerPeerBytes || header.originalLength > r.limits.GlobalBytes {
		return nil, false, fmt.Errorf("%w: packet length %d cannot fit cache byte limits", ErrReassemblyLimit, header.originalLength)
	}

	key := reassemblyKey{peerID: peerID, packetID: header.packetID}
	entry, ok := r.packets[key]
	if !ok {
		r.sequence++
		entry = &incompletePacket{
			key:            key,
			fragmentCount:  header.count,
			originalLength: header.originalLength,
			fragments:      make([][]byte, int(header.count)),
			createdAt:      now,
			sequence:       r.sequence,
		}
		r.packets[key] = entry
		r.addUsageLocked(peerID, 1, 0)
	} else if entry.fragmentCount != header.count || entry.originalLength != header.originalLength {
		r.removeLocked(entry)
		return nil, false, fmt.Errorf("%w: packet metadata changed", ErrInvalidFragment)
	}

	if existing := entry.fragments[int(header.index)]; existing != nil {
		if bytes.Equal(existing, payload) {
			return nil, false, nil
		}
		r.removeLocked(entry)
		return nil, false, fmt.Errorf("%w: packet %d index %d", ErrConflictingFragment, header.packetID, header.index)
	}

	retained := append([]byte(nil), payload...)
	entry.fragments[int(header.index)] = retained
	entry.received++
	entry.bytes += len(retained)
	r.addUsageLocked(peerID, 0, len(retained))

	if entry.received == len(entry.fragments) {
		assembled := make([]byte, 0, entry.originalLength)
		for _, part := range entry.fragments {
			assembled = append(assembled, part...)
		}
		r.removeLocked(entry)
		if len(assembled) != entry.originalLength {
			return nil, false, fmt.Errorf("%w: assembled length %d does not match original length %d", ErrInvalidFragment, len(assembled), entry.originalLength)
		}
		return assembled, true, nil
	}

	r.enforceLimitsLocked(peerID)
	return nil, false, nil
}

// Expire removes packets whose first fragment has been retained for the expiry interval.
func (r *Reassembler) Expire(now time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.initializeLocked()
	r.expireLocked(now)
}

// Usage returns per-peer and global retained-cache usage.
func (r *Reassembler) Usage(peerID string) (ReassemblyUsage, ReassemblyUsage) {
	if r == nil {
		return ReassemblyUsage{}, ReassemblyUsage{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.initializeLocked()
	return r.perPeer[peerID], r.global
}

func (r *Reassembler) initializeLocked() {
	if r.limits == (ReassemblyLimits{}) {
		r.limits = DefaultReassemblyLimits()
	}
	if r.packets == nil {
		r.packets = make(map[reassemblyKey]*incompletePacket)
	}
	if r.perPeer == nil {
		r.perPeer = make(map[string]ReassemblyUsage)
	}
}

func (r *Reassembler) expireLocked(now time.Time) {
	for _, entry := range r.packets {
		if !now.Before(entry.createdAt.Add(r.limits.Expiry)) {
			r.removeLocked(entry)
		}
	}
}

func (r *Reassembler) enforceLimitsLocked(peerID string) {
	for usage := r.perPeer[peerID]; usage.Packets > r.limits.PerPeerPackets || usage.Bytes > r.limits.PerPeerBytes; usage = r.perPeer[peerID] {
		victim := r.oldestLocked(peerID)
		if victim == nil {
			break
		}
		r.removeLocked(victim)
	}
	for r.global.Packets > r.limits.GlobalPackets || r.global.Bytes > r.limits.GlobalBytes {
		victim := r.oldestLocked("")
		if victim == nil {
			break
		}
		r.removeLocked(victim)
	}
}

func (r *Reassembler) oldestLocked(peerID string) *incompletePacket {
	var oldest *incompletePacket
	for _, entry := range r.packets {
		if peerID != "" && entry.key.peerID != peerID {
			continue
		}
		if oldest == nil || entry.sequence < oldest.sequence {
			oldest = entry
		}
	}
	return oldest
}

func (r *Reassembler) addUsageLocked(peerID string, packets, retainedBytes int) {
	usage := r.perPeer[peerID]
	usage.Packets += packets
	usage.Bytes += retainedBytes
	r.perPeer[peerID] = usage
	r.global.Packets += packets
	r.global.Bytes += retainedBytes
}

func (r *Reassembler) removeLocked(entry *incompletePacket) {
	if entry == nil {
		return
	}
	if _, ok := r.packets[entry.key]; !ok {
		return
	}
	delete(r.packets, entry.key)
	usage := r.perPeer[entry.key.peerID]
	usage.Packets--
	usage.Bytes -= entry.bytes
	if usage.Packets == 0 && usage.Bytes == 0 {
		delete(r.perPeer, entry.key.peerID)
	} else {
		r.perPeer[entry.key.peerID] = usage
	}
	r.global.Packets--
	r.global.Bytes -= entry.bytes
}

func parseFragment(fragment []byte, mtu int) (fragmentHeader, []byte, error) {
	if len(fragment) < FragmentHeaderSize {
		return fragmentHeader{}, nil, fmt.Errorf("%w: encoded length %d is below header size", ErrInvalidFragment, len(fragment))
	}
	if len(fragment) > MaxEncodedFragmentSize {
		return fragmentHeader{}, nil, fmt.Errorf("%w: encoded length %d exceeds %d", ErrInvalidFragment, len(fragment), MaxEncodedFragmentSize)
	}
	if fragment[0] != DatagramProtocolVersion {
		return fragmentHeader{}, nil, fmt.Errorf("%w: unsupported version %d", ErrInvalidFragment, fragment[0])
	}
	if mtu <= 0 {
		return fragmentHeader{}, nil, fmt.Errorf("%w: configured MTU must be positive", ErrInvalidFragment)
	}

	header := fragmentHeader{
		packetID:       binary.BigEndian.Uint64(fragment[1:9]),
		index:          binary.BigEndian.Uint16(fragment[9:11]),
		count:          binary.BigEndian.Uint16(fragment[11:13]),
		originalLength: int(binary.BigEndian.Uint16(fragment[13:15])),
	}
	payloadLength := int(binary.BigEndian.Uint16(fragment[15:17]))
	if header.count == 0 || header.count > MaxFragmentsPerPacket {
		return fragmentHeader{}, nil, fmt.Errorf("%w: fragment count %d", ErrInvalidFragment, header.count)
	}
	if header.index >= header.count {
		return fragmentHeader{}, nil, fmt.Errorf("%w: fragment index %d outside count %d", ErrInvalidFragment, header.index, header.count)
	}
	if header.originalLength <= 0 || header.originalLength > mtu || header.originalLength > MaxOriginalPacketSize {
		return fragmentHeader{}, nil, fmt.Errorf("%w: original length %d exceeds bounds", ErrInvalidFragment, header.originalLength)
	}
	if payloadLength <= 0 || payloadLength > MaxFragmentPayloadSize {
		return fragmentHeader{}, nil, fmt.Errorf("%w: payload length %d exceeds bounds", ErrInvalidFragment, payloadLength)
	}
	if len(fragment) != FragmentHeaderSize+payloadLength {
		return fragmentHeader{}, nil, fmt.Errorf("%w: encoded length %d does not match payload length %d", ErrInvalidFragment, len(fragment), payloadLength)
	}
	if header.originalLength < int(header.count) || header.originalLength > int(header.count)*MaxFragmentPayloadSize {
		return fragmentHeader{}, nil, fmt.Errorf("%w: original length %d is impossible for %d fragments", ErrInvalidFragment, header.originalLength, header.count)
	}
	return header, fragment[FragmentHeaderSize:], nil
}
