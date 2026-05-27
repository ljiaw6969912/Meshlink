package proto

import (
	"errors"
	"net/netip"
)

var ErrNotIPPacket = errors.New("not an IPv4 packet")

func DestinationIP(packet []byte) (netip.Addr, error) {
	if len(packet) == 0 {
		return netip.Addr{}, ErrNotIPPacket
	}
	version := packet[0] >> 4
	switch version {
	case 4:
		if len(packet) < 20 {
			return netip.Addr{}, ErrNotIPPacket
		}
		return netip.AddrFrom4([4]byte{packet[16], packet[17], packet[18], packet[19]}), nil
	default:
		return netip.Addr{}, ErrNotIPPacket
	}
}
