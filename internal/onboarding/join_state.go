package onboarding

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"meshlink/internal/config"
)

// JoinedNetworkInfo is local display state. Reconnecting always uses the
// enrolled certificate, never the saved invitation (which may have expired).
type JoinedNetworkInfo struct {
	NodeID      string `json:"node_id"`
	NodeName    string `json:"node_name"`
	Server      string `json:"server"`
	VirtualIP   string `json:"virtual_ip"`
	InviteLink  string `json:"invite_link,omitempty"`
	Code        string `json:"code,omitempty"`
	Certificate string `json:"certificate_sha256,omitempty"`
}

func (m Manager) joinStatePath() string { return filepath.Join(m.configsDir(), "join-state.json") }

func (m Manager) rememberJoinedNetwork(cfg config.Config, req JoinSpokeRequest) error {
	fingerprint, err := m.joinCertificateFingerprint(cfg)
	if err != nil {
		return err
	}
	return writePrettyJSON(m.joinStatePath(), JoinedNetworkInfo{
		NodeID: cfg.NodeID, NodeName: cfg.DisplayName, Server: cfg.Connect,
		VirtualIP: cfg.VirtualIP, InviteLink: req.InviteLink, Code: req.Code, Certificate: fingerprint,
	})
}

func (m Manager) JoinedNetwork() (JoinedNetworkInfo, error) {
	cfg, err := config.Load(m.activeConfigPath())
	if err != nil {
		return JoinedNetworkInfo{}, err
	}
	if cfg.Mode != "spoke" {
		return JoinedNetworkInfo{}, nil
	}
	info := JoinedNetworkInfo{NodeID: cfg.NodeID, NodeName: cfg.DisplayName, Server: cfg.Connect, VirtualIP: cfg.VirtualIP}
	if info.NodeName == "" {
		info.NodeName = cfg.NodeID
	}
	data, err := os.ReadFile(m.joinStatePath())
	if os.IsNotExist(err) {
		return info, nil
	}
	if err != nil {
		return info, err
	}
	var saved JoinedNetworkInfo
	if err := json.Unmarshal(data, &saved); err != nil {
		return info, err
	}
	fingerprint, err := m.joinCertificateFingerprint(*cfg)
	if err != nil {
		return info, err
	}
	if saved.NodeID == cfg.NodeID && saved.Server == cfg.Connect && saved.Certificate == fingerprint {
		info.InviteLink, info.Code = saved.InviteLink, saved.Code
	}
	return info, nil
}

func (m Manager) joinCertificateFingerprint(cfg config.Config) (string, error) {
	data, err := os.ReadFile(resolveConfigPath(m.configsDir(), cfg.CertFile))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
