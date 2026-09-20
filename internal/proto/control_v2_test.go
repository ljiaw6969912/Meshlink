package proto

import (
	"bytes"
	"strings"
	"testing"
)

func TestControlV2RoundTripSessionOffer(t *testing.T) {
	payload, err := MarshalControl(ControlTypeSessionOffer, "req-7", SessionOffer{
		SessionID: "session-1", Generation: 9, PeerNodeID: "node-c",
		DialerNodeID: "node-b", PairingKey: strings.Repeat("ab", 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := ParseControl(payload)
	if err != nil {
		t.Fatal(err)
	}
	offer, err := DecodeControlBody[SessionOffer](env)
	if err != nil || offer.Generation != 9 {
		t.Fatalf("offer=%+v err=%v", offer, err)
	}
}

func TestControlV2RejectsUnknownTypeVersionFieldAndOversize(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`{"protocol_version":1,"type":"ping","body":{}}`),
		[]byte(`{"protocol_version":2,"type":"future","body":{}}`),
		[]byte(`{"protocol_version":2,"type":"ping","body":{},"future":true}`),
	} {
		if _, err := ParseControl(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if _, err := ParseControl(bytes.Repeat([]byte{'x'}, MaxPayload+1)); err == nil {
		t.Fatal("accepted oversized control payload")
	}
	if _, err := ParseControl([]byte(`{"protocol_version":2,"type":"ping","body":{}} {}`)); err == nil {
		t.Fatal("accepted trailing JSON")
	}
	if _, err := ParseControl([]byte(`{"protocol_version":2,"type":"ping","body":null}`)); err == nil {
		t.Fatal("accepted null control body")
	}
}

func TestClientHelloRequiresVersionTwoAndPeerRole(t *testing.T) {
	valid := ClientHello{
		ProtocolVersion: ControlProtocolVersion,
		Role:            "peer",
		NodeID:          "b",
		VirtualIP:       "10.77.0.2",
		MTU:             1280,
		Capabilities:    []string{"quic_udp_v1"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid hello rejected: %v", err)
	}

	for name, mutate := range map[string]func(*ClientHello){
		"legacy version": func(hello *ClientHello) { hello.ProtocolVersion = 1 },
		"hub role":       func(hello *ClientHello) { hello.Role = "hub" },
	} {
		t.Run(name, func(t *testing.T) {
			hello := valid
			mutate(&hello)
			if err := hello.Validate(); err == nil {
				t.Fatal("invalid hello accepted")
			}
		})
	}
}

func TestControlV2RejectsUnknownBodyFieldsAndWrongBodyMapping(t *testing.T) {
	env, err := ParseControl([]byte(`{"protocol_version":2,"type":"session_offer","body":{"session_id":"session-1","generation":9,"peer_node_id":"node-c","dialer_node_id":"node-b","pairing_key":"abab","future":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeControlBody[SessionOffer](env); err == nil {
		t.Fatal("accepted unknown session offer field")
	}
	if _, err := DecodeControlBody[ControlPing](env); err == nil {
		t.Fatal("accepted session offer as ping")
	}
}

func TestClientHelloRequiresQUICCapability(t *testing.T) {
	hello := ClientHello{ProtocolVersion: 2, Role: "peer", NodeID: "b", VirtualIP: "10.77.0.2", MTU: 1280}
	if err := hello.Validate(); err == nil {
		t.Fatal("hello without quic_udp_v1 capability accepted")
	}
}

func TestClientHelloPreservesExistingLongNodeIDCompatibility(t *testing.T) {
	hello := ClientHello{
		ProtocolVersion: ControlProtocolVersion, Role: "peer", VirtualIP: "10.77.0.2", MTU: 1280,
		Capabilities: []string{"quic_udp_v1"}, NodeID: strings.Repeat("n", 1024),
	}
	if err := hello.Validate(); err != nil {
		t.Fatalf("previously valid long node ID rejected: %v", err)
	}
}
