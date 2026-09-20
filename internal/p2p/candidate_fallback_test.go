package p2p

import (
	"crypto/tls"
	"net"
	"strings"
	"testing"
	"time"

	"meshlink/internal/proto"
)

func TestSessionReachesPublicCandidateAfterSilentLANCandidates(t *testing.T) {
	for _, tc := range []struct {
		name           string
		silentLAN      int
		dialTimeout    time.Duration
		handshakeDelay time.Duration
	}{
		{name: "two LAN addresses", silentLAN: 2, dialTimeout: 2 * time.Second},
		{name: "maximum candidates and delayed public handshake", silentLAN: 15, dialTimeout: 4 * time.Second, handshakeDelay: 800 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			identities := newSessionTestIdentities(t, "node-b", "node-c")
			if tc.handshakeDelay > 0 {
				identities["node-c"].tlsConfig.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
					time.Sleep(tc.handshakeDelay)
					return nil, nil
				}
			}
			b := newSessionTestEndpointWithConfig(t, "node-b", "10.77.0.2", map[string]string{"node-c": "10.77.0.3"}, identities, func(cfg *SessionManagerConfig) {
				cfg.DialTimeout = tc.dialTimeout
				cfg.QUICConfig.HandshakeIdleTimeout = 10 * time.Second
			})
			c := newSessionTestEndpointWithConfig(t, "node-c", "10.77.0.3", map[string]string{"node-b": "10.77.0.2"}, identities, func(cfg *SessionManagerConfig) {
				cfg.DialTimeout = tc.dialTimeout
			})
			bOffer, cOffer := sessionTestOffers(b, c, identities, "public-after-silent-lan", 1, strings.Repeat("37", 32))
			bOffer.Candidates[0].Scope = "public"
			bOffer.Candidates[0].Priority = 50
			cOffer.Candidates[0].Scope = "public"
			for i := 0; i < tc.silentLAN; i++ {
				// A bound UDP socket which never replies models an unreachable LAN
				// address without relying on external routes or ICMP behavior.
				sink, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = sink.Close() })
				address := sink.LocalAddr().(*net.UDPAddr)
				bOffer.Candidates = append(bOffer.Candidates, proto.Candidate{
					Address: address.IP.String(), Port: uint16(address.Port), Scope: "lan", Priority: 100, ExpiresAt: bOffer.ExpiresAt,
				})
			}
			if err := b.manager.InstallOffer(bOffer); err != nil {
				t.Fatal(err)
			}
			if err := c.manager.InstallOffer(cOffer); err != nil {
				t.Fatal(err)
			}
			start := proto.SessionStart{SessionID: bOffer.SessionID, Generation: bOffer.Generation}
			if err := c.manager.StartOffer(start); err != nil {
				t.Fatal(err)
			}
			if err := b.manager.StartOffer(start); err != nil {
				t.Fatal(err)
			}
			waitSessionState(t, b.manager, "node-c", PathStatePublicDirect, tc.dialTimeout+time.Second)
			waitSessionState(t, c.manager, "node-b", PathStatePublicDirect, time.Second)
			assertSessionTransportSecurity(t, b.manager, "node-c")
			packet := []byte{0x45, 0, 0, 20, 0, 1, 0, 0, 64, 17, 0, 0, 10, 77, 0, 2, 10, 77, 0, 3}
			if err := b.manager.Send("node-c", packet); err != nil {
				t.Fatal(err)
			}
			assertSessionDelivery(t, c.deliveries, "node-b", packet)
		})
	}
}
