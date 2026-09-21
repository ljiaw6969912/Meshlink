//go:build windows

package p2p

import (
	"container/list"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	packetInfoAlignment  = int(unsafe.Sizeof(uintptr(0)))
	packetInfoHeaderLen  = packetInfoAlignment + 8 // SIZE_T, INT, INT (WSACMSGHDR)
	packetInfoMessageLen = packetInfoHeaderLen + 8 // IN_ADDR, ULONG (IN_PKTINFO)
	maxReplyRoutes       = 4096
	replyRouteTTL        = 2 * time.Minute
)

type replyRoute struct {
	address [4]byte
	ifIndex uint32
}

type cachedReplyRoute struct {
	remote  netip.AddrPort
	route   replyRoute
	expires time.Time
}

// Windows quic-go uses ReadFrom/WriteTo even on wildcard sockets. Retain the
// destination address and receiving interface so replies do not follow another
// interface's default route (for example, a VPN). These routes only select the
// socket's return path; session authentication still validates every peer.
type routedPacketConn struct {
	*net.UDPConn
	mu     sync.Mutex
	routes map[netip.AddrPort]*list.Element
	recent list.List
}

func candidatePacketConn(conn *net.UDPConn) (net.PacketConn, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return nil, err
	}
	var socketErr error
	if err = raw.Control(func(fd uintptr) {
		socketErr = windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IP, windows.IP_PKTINFO, 1)
	}); err != nil {
		return nil, err
	}
	if socketErr != nil {
		return nil, fmt.Errorf("enable UDP packet information: %w", socketErr)
	}
	return &routedPacketConn{UDPConn: conn, routes: make(map[netip.AddrPort]*list.Element)}, nil
}

func (c *routedPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	var oob [128]byte
	n, oobn, flags, remote, err := c.UDPConn.ReadMsgUDP(p, oob[:])
	if err == nil && remote != nil && flags&windows.MSG_CTRUNC == 0 {
		if route, ok := parseReplyRoute(oob[:oobn]); ok {
			c.rememberRoute(remote.AddrPort(), route, time.Now())
		}
	}
	return n, remote, err
}

func (c *routedPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	if remote, ok := addr.(*net.UDPAddr); ok && remote != nil {
		if route, found := c.replyRoute(remote.AddrPort(), time.Now()); found {
			oob := marshalReplyRoute(route)
			n, _, err := c.UDPConn.WriteMsgUDP(p, oob[:], remote)
			// Go's Windows WSASendMsg path can report zero bytes on synchronous
			// success. UDP sends are atomic; success accepted the full datagram.
			if err == nil && n == 0 {
				n = len(p)
			}
			return n, err
		}
	}
	return c.UDPConn.WriteTo(p, addr)
}

func (c *routedPacketConn) rememberRoute(remote netip.AddrPort, route replyRoute, now time.Time) {
	remote = netip.AddrPortFrom(remote.Addr().Unmap(), remote.Port())
	if !remote.IsValid() || !remote.Addr().Is4() {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := cachedReplyRoute{remote: remote, route: route, expires: now.Add(replyRouteTTL)}
	if el := c.routes[remote]; el != nil {
		el.Value = entry
		c.recent.MoveToFront(el)
		return
	}
	if len(c.routes) >= maxReplyRoutes {
		oldest := c.recent.Back()
		delete(c.routes, oldest.Value.(cachedReplyRoute).remote)
		c.recent.Remove(oldest)
	}
	c.routes[remote] = c.recent.PushFront(entry)
}

func (c *routedPacketConn) replyRoute(remote netip.AddrPort, now time.Time) (replyRoute, bool) {
	remote = netip.AddrPortFrom(remote.Addr().Unmap(), remote.Port())
	c.mu.Lock()
	defer c.mu.Unlock()
	el := c.routes[remote]
	if el == nil {
		return replyRoute{}, false
	}
	entry := el.Value.(cachedReplyRoute)
	if !now.Before(entry.expires) {
		delete(c.routes, remote)
		c.recent.Remove(el)
		return replyRoute{}, false
	}
	return entry.route, true
}

func parseReplyRoute(oob []byte) (replyRoute, bool) {
	for len(oob) >= packetInfoHeaderLen {
		var length uint64
		if packetInfoAlignment == 8 {
			length = binary.NativeEndian.Uint64(oob[:8])
		} else {
			length = uint64(binary.NativeEndian.Uint32(oob[:4]))
		}
		if length < uint64(packetInfoHeaderLen) || length > uint64(len(oob)) {
			return replyRoute{}, false
		}
		level := binary.NativeEndian.Uint32(oob[packetInfoAlignment:])
		kind := binary.NativeEndian.Uint32(oob[packetInfoAlignment+4:])
		if level == windows.IPPROTO_IP && kind == windows.IP_PKTINFO && length == uint64(packetInfoMessageLen) {
			var route replyRoute
			copy(route.address[:], oob[packetInfoHeaderLen:packetInfoHeaderLen+4])
			route.ifIndex = binary.NativeEndian.Uint32(oob[packetInfoHeaderLen+4:])
			addr := netip.AddrFrom4(route.address)
			return route, route.ifIndex != 0 && !addr.IsUnspecified() && !addr.IsMulticast()
		}
		next := (int(length) + packetInfoAlignment - 1) &^ (packetInfoAlignment - 1)
		if next > len(oob) {
			break
		}
		oob = oob[next:]
	}
	return replyRoute{}, false
}

func marshalReplyRoute(route replyRoute) [packetInfoMessageLen]byte {
	var oob [packetInfoMessageLen]byte
	if packetInfoAlignment == 8 {
		binary.NativeEndian.PutUint64(oob[:8], uint64(len(oob)))
	} else {
		binary.NativeEndian.PutUint32(oob[:4], uint32(len(oob)))
	}
	binary.NativeEndian.PutUint32(oob[packetInfoAlignment:], windows.IPPROTO_IP)
	binary.NativeEndian.PutUint32(oob[packetInfoAlignment+4:], windows.IP_PKTINFO)
	copy(oob[packetInfoHeaderLen:], route.address[:])
	binary.NativeEndian.PutUint32(oob[packetInfoHeaderLen+4:], route.ifIndex)
	return oob
}
