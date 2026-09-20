package onboarding

import (
	"fmt"
	"strings"

	"meshlink/internal/config"
)

// Client installations cannot issue credentials for another server's registry.
func (m Manager) CreateServerInvite(req CreateInviteRequest) (CreateInviteResult, error) {
	cfg, err := config.Load(m.activeConfigPath())
	if err != nil || cfg.Mode != "hub" {
		return CreateInviteResult{}, fmt.Errorf("请先启动本机服务器，再生成接入码")
	}
	server := cfg.ServerPublicEndpoint
	if server == "" {
		invite, ok, err := m.LatestInvite()
		if err != nil {
			return CreateInviteResult{}, err
		}
		if ok {
			server = invite.Server
		}
	}
	if server != "" && !strings.EqualFold(meshConnectAddress(server), meshConnectAddress(req.Server)) {
		return CreateInviteResult{}, fmt.Errorf("服务器地址已改变，请先启动服务器以应用新地址")
	}
	return m.CreateInvite(req)
}

func (m Manager) IsOwnServerInvite(link string) bool {
	invite, err := ParseInviteLink(strings.TrimSpace(link))
	if err != nil {
		return false
	}
	cfg, err := config.Load(m.activeConfigPath())
	if err != nil || cfg.Mode != "hub" {
		return false
	}
	store, err := m.loadInviteStore()
	if err != nil {
		return false
	}
	for _, own := range store.Invites {
		if own.Token == invite.Token && strings.EqualFold(meshConnectAddress(own.Server), meshConnectAddress(invite.Server)) {
			return true
		}
	}
	return false
}
