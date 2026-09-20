package cloudhub

import (
	"context"
	"fmt"
	"strings"
)

func (s *Service) CreateNetwork(ctx context.Context, req CreateNetworkRequest) (Network, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	account, err := s.store.GetAccount(ctx, req.AccountID)
	if err != nil {
		return Network{}, fmt.Errorf("account was not found: %w", err)
	}
	if err := ensureAccountActive(account); err != nil {
		return Network{}, err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return Network{}, fmt.Errorf("network name is required")
	}
	now := s.nowTime()
	network := Network{
		ID:        mustID("net"),
		AccountID: account.ID,
		Name:      name,
		CreatedAt: now,
	}
	created, err := s.store.CreateNetwork(ctx, network)
	if err != nil {
		return Network{}, err
	}
	member := Member{
		ID:        mustID("mbr"),
		AccountID: account.ID,
		NetworkID: created.ID,
		Role:      "owner",
		CreatedAt: now,
	}
	if _, err := s.store.CreateMember(ctx, member); err != nil {
		return Network{}, err
	}
	if err := s.recordAudit(ctx, AuditNetworkCreated, account.ID, created.ID, "", map[string]any{
		"name": created.Name,
	}); err != nil {
		return Network{}, err
	}
	return created, nil
}

func (s *Service) ListAccountNetworks(ctx context.Context, accountID string) ([]Network, error) {
	if accountID == "" {
		return nil, fmt.Errorf("account_id is required")
	}
	if _, err := s.store.GetAccount(ctx, accountID); err != nil {
		return nil, fmt.Errorf("account was not found: %w", err)
	}
	return s.store.ListAccountNetworks(ctx, accountID)
}
