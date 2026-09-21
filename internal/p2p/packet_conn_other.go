//go:build !windows

package p2p

import "net"

// Other platforms let quic-go handle per-packet routing information itself.
func candidatePacketConn(conn *net.UDPConn) (net.PacketConn, error) { return conn, nil }
