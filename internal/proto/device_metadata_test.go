package proto

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestDeviceMetadataHelloCompatibility(t *testing.T) {
	h := ClientHello{ProtocolVersion: 2, Role: "peer", NodeID: "desk", VirtualIP: "10.77.0.2", MTU: 1280, Capabilities: DeviceMetadataCapabilities("办公电脑", "02:11:22:33:44:55")}
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	// This is precisely the old strict decoder shape: metadata adds no fields.
	var old struct {
		ProtocolVersion int      `json:"protocol_version"`
		Role            string   `json:"role"`
		NodeID          string   `json:"node_id"`
		VirtualIP       string   `json:"virtual_ip"`
		Routes          []string `json:"routes"`
		MTU             int      `json:"mtu"`
		Capabilities    []string `json:"capabilities"`
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&old); err != nil {
		t.Fatal(err)
	}
	name, mac, err := ParseDeviceMetadata(old.Capabilities)
	if err != nil || name != "办公电脑" || mac != "02:11:22:33:44:55" {
		t.Fatalf("metadata: %q %q %v", name, mac, err)
	}
	if err := h.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDeviceMetadataRejectsAmbiguousOrInvalidTokens(t *testing.T) {
	for _, caps := range [][]string{{"device_mac:bad"}, {"device_name:***"}, {"device_name:_w"}, {"device_name:YQ", "device_name:Yg"}, {"device_mac:02:11:22:33:44:55", "device_mac:02:11:22:33:44:66"}, {"device_mac:ff:ff:ff:ff:ff:ff"}} {
		if _, _, err := ParseDeviceMetadata(caps); err == nil {
			t.Fatalf("accepted %v", caps)
		}
	}
}
