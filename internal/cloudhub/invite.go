package cloudhub

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"time"
)

const (
	defaultInviteTTL         = 10 * time.Minute
	defaultInviteMaxUses     = 1
	defaultInviteMaxFailures = 5
)

func (s *Service) CreateInvite(ctx context.Context, req CreateInviteRequest) (InviteResult, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	account, err := s.store.GetAccount(ctx, req.AccountID)
	if err != nil {
		return InviteResult{}, fmt.Errorf("account was not found: %w", err)
	}
	if err := ensureAccountActive(account); err != nil {
		return InviteResult{}, err
	}
	network, err := s.store.GetNetwork(ctx, req.NetworkID)
	if err != nil {
		return InviteResult{}, fmt.Errorf("network was not found: %w", err)
	}
	if network.AccountID != account.ID {
		return InviteResult{}, fmt.Errorf("network does not belong to account: %w", ErrForbidden)
	}
	if network.DeletedAt != nil {
		return InviteResult{}, fmt.Errorf("network has been deleted")
	}

	ttl := req.TTL
	if ttl <= 0 {
		ttl = defaultInviteTTL
	}
	maxUses := req.MaxUses
	if maxUses <= 0 {
		maxUses = defaultInviteMaxUses
	}
	oneTime := req.OneTime || maxUses <= 1
	token, err := randomTokenBytes(32)
	if err != nil {
		return InviteResult{}, err
	}
	code, err := randomCode()
	if err != nil {
		return InviteResult{}, err
	}
	now := s.nowTime()
	invite := Invite{
		ID:          mustID("inv"),
		AccountID:   account.ID,
		NetworkID:   network.ID,
		Token:       token,
		CodeHash:    hashInviteCode(token, code),
		CreatedAt:   now,
		ExpiresAt:   now.Add(ttl),
		OneTime:     oneTime,
		MaxUses:     maxUses,
		MaxFailures: defaultInviteMaxFailures,
	}
	created, err := s.store.CreateInvite(ctx, invite)
	if err != nil {
		return InviteResult{}, err
	}
	if err := s.recordAudit(ctx, AuditInviteCreated, account.ID, network.ID, "", map[string]any{
		"invite_id":    created.ID,
		"expires_at":   created.ExpiresAt,
		"one_time":     created.OneTime,
		"max_uses":     created.MaxUses,
		"max_failures": created.MaxFailures,
	}); err != nil {
		return InviteResult{}, err
	}
	return inviteResult(created, code), nil
}

func (s *Service) GetInvite(ctx context.Context, id string) (Invite, error) {
	if id == "" {
		return Invite{}, fmt.Errorf("invite_id is required")
	}
	return s.store.GetInvite(ctx, id)
}

func inviteResult(invite Invite, code string) InviteResult {
	return InviteResult{
		ID:          invite.ID,
		AccountID:   invite.AccountID,
		NetworkID:   invite.NetworkID,
		Token:       invite.Token,
		Code:        code,
		CreatedAt:   invite.CreatedAt,
		ExpiresAt:   invite.ExpiresAt,
		OneTime:     invite.OneTime,
		Uses:        invite.Uses,
		MaxUses:     invite.MaxUses,
		Failures:    invite.Failures,
		MaxFailures: invite.MaxFailures,
	}
}

func hashInviteCode(token, code string) string {
	sum := sha256.Sum256([]byte(token + ":" + code))
	return hex.EncodeToString(sum[:])
}

func verifyInviteCode(invite Invite, code string) bool {
	got := hashInviteCode(invite.Token, code)
	return subtle.ConstantTimeCompare([]byte(got), []byte(invite.CodeHash)) == 1
}
