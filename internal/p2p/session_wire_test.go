package p2p

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestSessionHelloHMACBindsSessionGenerationIdentitiesAndNonce(t *testing.T) {
	key := bytes.Repeat([]byte{0xd1}, 32)
	hello, err := newSessionHello("session-wire", 91, "node-b", "node-c", key)
	if err != nil {
		t.Fatalf("new SessionHello: %v", err)
	}
	if err := validateSessionHello(hello, "session-wire", 91, "node-b", "node-c", key); err != nil {
		t.Fatalf("validate untouched SessionHello: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*sessionWireMessage)
	}{
		{name: "session", mutate: func(message *sessionWireMessage) { message.SessionID = "session-other" }},
		{name: "generation", mutate: func(message *sessionWireMessage) { message.Generation++ }},
		{name: "from", mutate: func(message *sessionWireMessage) { message.FromNodeID = "node-d" }},
		{name: "to", mutate: func(message *sessionWireMessage) { message.ToNodeID = "node-d" }},
		{name: "nonce", mutate: func(message *sessionWireMessage) { message.Nonce = flipCanonicalHex(message.Nonce) }},
		{name: "mac", mutate: func(message *sessionWireMessage) { message.MAC = flipCanonicalHex(message.MAC) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutated := hello
			test.mutate(&mutated)
			if err := validateSessionHello(mutated, "session-wire", 91, "node-b", "node-c", key); err == nil {
				t.Fatalf("accepted SessionHello with mutated %s: %+v", test.name, mutated)
			}
		})
	}
	wrongKey := bytes.Repeat([]byte{0xd2}, 32)
	if err := validateSessionHello(hello, "session-wire", 91, "node-b", "node-c", wrongKey); err == nil {
		t.Fatal("accepted SessionHello under a different pairing key")
	}
	second, err := newSessionHello("session-wire", 91, "node-c", "node-b", key)
	if err != nil {
		t.Fatalf("new peer SessionHello: %v", err)
	}
	if second.Nonce == hello.Nonce {
		t.Fatal("both SessionHello messages reused a nonce")
	}
}

func TestSessionWireRejectsOversizeAndUnknownFields(t *testing.T) {
	var oversize bytes.Buffer
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], sessionWireMaxFrameSize+1)
	oversize.Write(header[:])
	if _, err := readSessionWireMessage(&oversize); err == nil {
		t.Fatal("accepted oversized session control frame")
	}

	payload := []byte(`{"version":1,"type":"ping","nonce":"` + strings.Repeat("00", heartbeatNonceSize) + `","future":true}`)
	var unknown bytes.Buffer
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	unknown.Write(header[:])
	unknown.Write(payload)
	if _, err := readSessionWireMessage(&unknown); err == nil {
		t.Fatal("accepted unknown SessionHello/control field")
	}
}

func flipCanonicalHex(value string) string {
	if value[0] == '0' {
		return "1" + value[1:]
	}
	return "0" + value[1:]
}
