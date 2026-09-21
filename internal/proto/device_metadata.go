package proto

import (
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"unicode/utf8"
)

const DeviceMetadataCapability = "device_metadata_v1"

// Metadata uses capability tokens so older strict ClientHello decoders remain compatible.
func DeviceMetadataCapabilities(name, mac string) []string {
	capabilities := []string{"quic_udp_v1", DeviceMetadataCapability, "device_name:" + base64.RawURLEncoding.EncodeToString([]byte(name))}
	if mac != "" {
		capabilities = append(capabilities, "device_mac:"+mac)
	}
	return capabilities
}

func ParseDeviceMetadata(capabilities []string) (name, mac string, err error) {
	seenName, seenMAC := false, false
	for _, capability := range capabilities {
		if strings.HasPrefix(capability, "device_name:") {
			if seenName {
				return "", "", fmt.Errorf("duplicate device name metadata")
			}
			seenName = true
			decoded, decodeErr := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(capability, "device_name:"))
			if decodeErr != nil || !utf8.Valid(decoded) || len(decoded) > 1024 {
				return "", "", fmt.Errorf("invalid device name metadata")
			}
			name = string(decoded)
		}
		if strings.HasPrefix(capability, "device_mac:") {
			if seenMAC {
				return "", "", fmt.Errorf("duplicate device MAC metadata")
			}
			seenMAC = true
			parsed, parseErr := net.ParseMAC(strings.TrimPrefix(capability, "device_mac:"))
			if parseErr != nil || len(parsed) != 6 || parsed[0]&1 != 0 || parsed.String() == "00:00:00:00:00:00" {
				return "", "", fmt.Errorf("invalid device MAC metadata")
			}
			mac = parsed.String()
		}
	}
	return name, mac, nil
}
