package proto

import (
	"encoding/binary"
	"errors"
	"net/netip"
)

var ErrNotIPPacket = errors.New("not an IPv4 packet")

func SourceIP(packet []byte) (netip.Addr, error) {
	source, _, err := ipv4Endpoints(packet)
	if err != nil {
		return netip.Addr{}, ErrNotIPPacket
	}
	return source, nil
}

func DestinationIP(packet []byte) (netip.Addr, error) {
	_, destination, err := ipv4Endpoints(packet)
	if err != nil {
		return netip.Addr{}, ErrNotIPPacket
	}
	return destination, nil
}

func ipv4Endpoints(packet []byte) (netip.Addr, netip.Addr, error) {
	if len(packet) < 20 || packet[0]>>4 != 4 {
		return netip.Addr{}, netip.Addr{}, ErrNotIPPacket
	}
	headerLength := int(packet[0]&0x0f) * 4
	if headerLength < 20 || headerLength > len(packet) {
		return netip.Addr{}, netip.Addr{}, ErrNotIPPacket
	}
	totalLength := int(binary.BigEndian.Uint16(packet[2:4]))
	if totalLength < headerLength || totalLength != len(packet) {
		return netip.Addr{}, netip.Addr{}, ErrNotIPPacket
	}
	source := netip.AddrFrom4([4]byte{packet[12], packet[13], packet[14], packet[15]})
	destination := netip.AddrFrom4([4]byte{packet[16], packet[17], packet[18], packet[19]})
	return source, destination, nil
}
