package p2p

import (
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"sync"

	"meshlink/internal/proto"
)

var (
	ErrRouteConflict      = errors.New("route_conflict")
	ErrInvalidRoute       = errors.New("invalid_route")
	ErrSpoofedSource      = errors.New("spoofed_source")
	ErrForeignDestination = errors.New("foreign_destination")
)

var allowedPrivateRoutes = [...]netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
}

type routeEntry struct {
	prefix netip.Prefix
	member proto.Member
}

// RouteTable stores an atomically replaceable IPv4 longest-prefix route set.
type RouteTable struct {
	mu     sync.RWMutex
	routes []routeEntry
}

// Replace validates a complete member snapshot before publishing any routes.
func (t *RouteTable) Replace(members []proto.Member) error {
	if t == nil {
		return fmt.Errorf("route table is nil")
	}

	owners := make(map[netip.Prefix]string)
	seenNodes := make(map[string]struct{}, len(members))
	routes := make([]routeEntry, 0, len(members))
	for memberIndex, member := range members {
		if strings.TrimSpace(member.NodeID) == "" {
			return fmt.Errorf("member %d node ID is required", memberIndex)
		}
		if _, exists := seenNodes[member.NodeID]; exists {
			return fmt.Errorf("member %d repeats node ID %q", memberIndex, member.NodeID)
		}
		seenNodes[member.NodeID] = struct{}{}

		virtualIP, err := parsePrivateIPv4Address(member.VirtualIP)
		if err != nil {
			return fmt.Errorf("member %q virtual IP: %w", member.NodeID, err)
		}
		member = cloneMember(member)
		if err := addRoute(&routes, owners, netip.PrefixFrom(virtualIP, 32), member); err != nil {
			return err
		}
		for routeIndex, rawRoute := range member.Routes {
			prefix, err := parsePrivateRoute(rawRoute)
			if err != nil {
				return fmt.Errorf("member %q route %d: %w", member.NodeID, routeIndex, err)
			}
			if err := addRoute(&routes, owners, prefix, member); err != nil {
				return err
			}
		}
	}

	sort.Slice(routes, func(i, j int) bool {
		if routes[i].prefix.Bits() != routes[j].prefix.Bits() {
			return routes[i].prefix.Bits() > routes[j].prefix.Bits()
		}
		return routes[i].prefix.Addr().Less(routes[j].prefix.Addr())
	})

	t.mu.Lock()
	t.routes = routes
	t.mu.Unlock()
	return nil
}

func addRoute(routes *[]routeEntry, owners map[netip.Prefix]string, prefix netip.Prefix, member proto.Member) error {
	prefix = prefix.Masked()
	if owner, exists := owners[prefix]; exists {
		if owner != member.NodeID {
			return fmt.Errorf("%w: prefix %s is owned by both %q and %q", ErrRouteConflict, prefix, owner, member.NodeID)
		}
		return nil
	}
	owners[prefix] = member.NodeID
	*routes = append(*routes, routeEntry{prefix: prefix, member: member})
	return nil
}

// Lookup selects the owner of the most-specific prefix containing address.
func (t *RouteTable) Lookup(address netip.Addr) (proto.Member, bool) {
	if t == nil || !address.IsValid() || !address.Is4() {
		return proto.Member{}, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, route := range t.routes {
		if route.prefix.Contains(address) {
			return cloneMember(route.member), true
		}
	}
	return proto.Member{}, false
}

// ValidateInboundPacket authorizes both inner IPv4 addresses before TUN delivery.
func ValidateInboundPacket(peer proto.Member, localVirtualIP netip.Addr, localRoutes []netip.Prefix, packet []byte) error {
	if strings.TrimSpace(peer.NodeID) == "" {
		return fmt.Errorf("invalid peer authorization: node ID is required")
	}
	peerVirtualIP, err := parsePrivateIPv4Address(peer.VirtualIP)
	if err != nil {
		return fmt.Errorf("invalid peer authorization: %w", err)
	}
	peerRoutes := make([]netip.Prefix, 0, len(peer.Routes))
	for i, rawRoute := range peer.Routes {
		prefix, err := parsePrivateRoute(rawRoute)
		if err != nil {
			return fmt.Errorf("invalid peer route %d: %w", i, err)
		}
		peerRoutes = append(peerRoutes, prefix)
	}
	if err := validatePrivateIPv4Address(localVirtualIP); err != nil {
		return fmt.Errorf("invalid local virtual IP: %w", err)
	}
	validatedLocalRoutes := make([]netip.Prefix, 0, len(localRoutes))
	for i, prefix := range localRoutes {
		if err := validatePrivatePrefix(prefix); err != nil {
			return fmt.Errorf("invalid local route %d: %w", i, err)
		}
		validatedLocalRoutes = append(validatedLocalRoutes, prefix.Masked())
	}

	source, err := proto.SourceIP(packet)
	if err != nil {
		return fmt.Errorf("invalid inbound packet source: %w", err)
	}
	destination, err := proto.DestinationIP(packet)
	if err != nil {
		return fmt.Errorf("invalid inbound packet destination: %w", err)
	}
	if source != peerVirtualIP && !prefixesContain(peerRoutes, source) {
		return fmt.Errorf("%w: peer %q is not authorized for source %s", ErrSpoofedSource, peer.NodeID, source)
	}
	if destination != localVirtualIP && !prefixesContain(validatedLocalRoutes, destination) {
		return fmt.Errorf("%w: destination %s is not local", ErrForeignDestination, destination)
	}
	return nil
}

func parsePrivateIPv4Address(raw string) (netip.Addr, error) {
	address, err := netip.ParseAddr(raw)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("invalid IPv4 address %q: %w", raw, err)
	}
	if err := validatePrivateIPv4Address(address); err != nil {
		return netip.Addr{}, fmt.Errorf("address %q: %w", raw, err)
	}
	return address, nil
}

func validatePrivateIPv4Address(address netip.Addr) error {
	if !address.IsValid() || !address.Is4() {
		return fmt.Errorf("must be IPv4")
	}
	for _, allowed := range allowedPrivateRoutes {
		if allowed.Contains(address) {
			return nil
		}
	}
	return fmt.Errorf("must be inside an RFC1918 network")
}

func parsePrivateRoute(raw string) (netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(raw)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%w: parse prefix %q: %v", ErrInvalidRoute, raw, err)
	}
	prefix = prefix.Masked()
	if err := validatePrivatePrefix(prefix); err != nil {
		return netip.Prefix{}, err
	}
	return prefix, nil
}

func validatePrivatePrefix(prefix netip.Prefix) error {
	if !prefix.IsValid() || !prefix.Addr().Is4() {
		return fmt.Errorf("%w: prefix %q must be IPv4", ErrInvalidRoute, prefix)
	}
	prefix = prefix.Masked()
	for _, allowed := range allowedPrivateRoutes {
		if prefix.Bits() >= allowed.Bits() && allowed.Contains(prefix.Addr()) {
			return nil
		}
	}
	return fmt.Errorf("%w: public route %q is not allowed", ErrInvalidRoute, prefix)
}

func prefixesContain(prefixes []netip.Prefix, address netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func cloneMember(member proto.Member) proto.Member {
	member.Routes = append([]string(nil), member.Routes...)
	return member
}
