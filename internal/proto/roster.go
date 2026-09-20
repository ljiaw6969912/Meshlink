package proto

import (
	"encoding/json"
	"strings"
	"time"
)

type Roster struct {
	UpdatedAt time.Time    `json:"updated_at"`
	State     string       `json:"state"`
	Nodes     []RosterNode `json:"nodes"`
}

type RosterNode struct {
	NodeID         string     `json:"node_id"`
	Mode           string     `json:"mode,omitempty"`
	Status         string     `json:"status,omitempty"`
	VirtualIP      string     `json:"virtual_ip,omitempty"`
	Routes         []string   `json:"routes,omitempty"`
	RemoteAddr     string     `json:"remote_addr,omitempty"`
	Fingerprint    string     `json:"fingerprint,omitempty"`
	CommonName     string     `json:"common_name,omitempty"`
	ConnectedAt    time.Time  `json:"connected_at,omitempty"`
	LastSeen       time.Time  `json:"last_seen,omitempty"`
	DisconnectedAt *time.Time `json:"disconnected_at,omitempty"`
}

func (r Roster) Marshal() ([]byte, error) {
	return json.Marshal(r)
}

func ParseRoster(payload []byte) (Roster, error) {
	var r Roster
	if err := json.Unmarshal(payload, &r); err != nil {
		return Roster{}, err
	}
	return r, nil
}

func IsInfrastructureMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "hub", "relay", "control":
		return true
	default:
		return false
	}
}
