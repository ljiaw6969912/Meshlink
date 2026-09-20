package cloudhub

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"meshlink/internal/p2p"
)

type MemoryStore struct {
	mu sync.RWMutex

	accounts                         map[string]Account
	networks                         map[string]Network
	members                          map[string]Member
	organizations                    map[string]Organization
	memberships                      map[string]Membership
	membershipIDs                    map[string]string
	organizationInvites              map[string]OrganizationInvite
	organizationInviteTokenDigestIDs map[string]string
	deviceGroups                     map[string]DeviceGroup
	deviceGroupNameIDs               map[string]string
	organizationDevices              map[string]OrganizationDevice
	organizationDeviceIDs            map[string]string
	deploymentBundles                map[string]DeploymentBundle
	bootstrapCredentials             map[string]BootstrapCredential
	bootstrapCredentialDigestIDs     map[string]string
	rollouts                         map[string]Rollout
	deploymentTargets                map[string]DeploymentTarget
	connectionGrants                 map[string]ConnectionGrant
	connectionGrantIDs               map[string]string
	invites                          map[string]Invite
	inviteTokenIDs                   map[string]string
	devices                          map[string]Device
	p2pCandidates                    map[string][]p2p.Candidate
	sessions                         map[string]Session
	relaySessions                    map[string]RelaySession
	connectionLogs                   map[string]ConnectionLog
	relayUsage                       map[string]RelayUsage
	bans                             map[string]Ban
	revocations                      map[string]Revocation
	riskEvents                       []RiskEvent
	auditEvents                      []AuditEvent
	subscriptions                    map[string]Subscription
	subscriptionEvents               map[string]SubscriptionEventApplyResult
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		accounts:                         map[string]Account{},
		networks:                         map[string]Network{},
		members:                          map[string]Member{},
		organizations:                    map[string]Organization{},
		memberships:                      map[string]Membership{},
		membershipIDs:                    map[string]string{},
		organizationInvites:              map[string]OrganizationInvite{},
		organizationInviteTokenDigestIDs: map[string]string{},
		deviceGroups:                     map[string]DeviceGroup{},
		deviceGroupNameIDs:               map[string]string{},
		organizationDevices:              map[string]OrganizationDevice{},
		organizationDeviceIDs:            map[string]string{},
		deploymentBundles:                map[string]DeploymentBundle{},
		bootstrapCredentials:             map[string]BootstrapCredential{},
		bootstrapCredentialDigestIDs:     map[string]string{},
		rollouts:                         map[string]Rollout{},
		deploymentTargets:                map[string]DeploymentTarget{},
		connectionGrants:                 map[string]ConnectionGrant{},
		connectionGrantIDs:               map[string]string{},
		invites:                          map[string]Invite{},
		inviteTokenIDs:                   map[string]string{},
		devices:                          map[string]Device{},
		p2pCandidates:                    map[string][]p2p.Candidate{},
		sessions:                         map[string]Session{},
		relaySessions:                    map[string]RelaySession{},
		connectionLogs:                   map[string]ConnectionLog{},
		relayUsage:                       map[string]RelayUsage{},
		bans:                             map[string]Ban{},
		revocations:                      map[string]Revocation{},
		subscriptions:                    map[string]Subscription{},
		subscriptionEvents:               map[string]SubscriptionEventApplyResult{},
	}
}

func (s *MemoryStore) CreateAccount(_ context.Context, account Account) (Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[account.ID]; ok {
		return Account{}, ErrAlreadyExists
	}
	s.accounts[account.ID] = account
	return account, nil
}

func (s *MemoryStore) GetAccount(_ context.Context, id string) (Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	account, ok := s.accounts[id]
	if !ok {
		return Account{}, ErrNotFound
	}
	return account, nil
}

func (s *MemoryStore) UpdateAccount(_ context.Context, account Account) (Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[account.ID]; !ok {
		return Account{}, ErrNotFound
	}
	s.accounts[account.ID] = account
	return account, nil
}

func (s *MemoryStore) GetAccountSubscription(_ context.Context, accountID string) (Subscription, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	subscription, ok := s.subscriptions[accountID]
	if !ok {
		return Subscription{}, ErrNotFound
	}
	return cloneSubscription(subscription), nil
}

func (s *MemoryStore) ApplySubscriptionEvent(_ context.Context, event SubscriptionEvent) (SubscriptionEventApplyResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	eventKey := subscriptionEventKey(event.Provider, event.EventID)
	if existing, ok := s.subscriptionEvents[eventKey]; ok {
		if existing.payloadDigest != event.PayloadDigest {
			return SubscriptionEventApplyResult{}, fmt.Errorf("subscription event conflicts with prior event: %w", ErrConflict)
		}
		existing.Outcome = SubscriptionEventRepeated
		existing.Subscription = cloneSubscription(existing.Subscription)
		return existing, nil
	}
	account, ok := s.accounts[event.AccountID]
	if !ok {
		return SubscriptionEventApplyResult{}, ErrNotFound
	}
	if _, ok := LookupPlan(event.PlanID); !ok {
		return SubscriptionEventApplyResult{}, ErrNotFound
	}

	previousSubscription, hadSubscription := s.subscriptions[event.AccountID]
	previousStatus := previousSubscription.Status
	previousPlanID := account.PlanID
	if hadSubscription && subscriptionEventIsStale(previousSubscription, event) {
		result := SubscriptionEventApplyResult{
			Outcome:        SubscriptionEventStale,
			Subscription:   cloneSubscription(previousSubscription),
			Account:        account,
			payloadDigest:  event.PayloadDigest,
			PreviousStatus: previousStatus,
			PreviousPlanID: previousPlanID,
		}
		s.subscriptionEvents[eventKey] = result
		return result, nil
	}

	now := event.ReceivedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	subscription := previousSubscription
	if !hadSubscription {
		subscription = Subscription{
			ID:        mustID("sub"),
			AccountID: account.ID,
			CreatedAt: now,
		}
	}
	subscription.AccountID = account.ID
	subscription.PlanID = event.PlanID
	subscription.Status = effectiveSubscriptionStatus(event.Status, event.EffectiveUntil, now)
	subscription.EffectiveAt = event.EffectiveAt
	subscription.EffectiveUntil = cloneTimePtr(event.EffectiveUntil)
	subscription.Provider = event.Provider
	subscription.ProviderSubscriptionID = event.ProviderSubscriptionID
	subscription.ProviderCustomerID = event.ProviderCustomerID
	subscription.Version = event.Version
	subscription.UpdatedAt = now

	account.PlanID = accountPlanForSubscriptionEvent(account.PlanID, event, subscription, now)
	s.subscriptions[account.ID] = cloneSubscription(subscription)
	s.accounts[account.ID] = account
	result := SubscriptionEventApplyResult{
		Outcome:        SubscriptionEventAccepted,
		Subscription:   cloneSubscription(subscription),
		Account:        account,
		payloadDigest:  event.PayloadDigest,
		PreviousStatus: previousStatus,
		PreviousPlanID: previousPlanID,
		StatusChanged:  !hadSubscription || previousStatus != subscription.Status,
		PlanChanged:    previousPlanID != account.PlanID,
	}
	s.subscriptionEvents[eventKey] = result
	return result, nil
}

func (s *MemoryStore) RefreshAccountSubscription(_ context.Context, accountID string, now time.Time) (SubscriptionRefreshResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account, ok := s.accounts[accountID]
	if !ok {
		return SubscriptionRefreshResult{}, ErrNotFound
	}
	subscription, ok := s.subscriptions[accountID]
	if !ok {
		return SubscriptionRefreshResult{Account: account}, nil
	}
	previousStatus := subscription.Status
	previousPlanID := account.PlanID
	if subscription.Status == SubscriptionStatusCanceled && subscription.EffectiveUntil != nil && !now.UTC().Before(subscription.EffectiveUntil.UTC()) {
		subscription.Status = SubscriptionStatusExpired
		subscription.UpdatedAt = now.UTC()
		account.PlanID = PlanFree
		s.subscriptions[accountID] = cloneSubscription(subscription)
		s.accounts[accountID] = account
		return SubscriptionRefreshResult{
			Account:        account,
			Subscription:   cloneSubscription(subscription),
			Found:          true,
			Changed:        previousStatus != subscription.Status || previousPlanID != account.PlanID,
			PreviousStatus: previousStatus,
			PreviousPlanID: previousPlanID,
		}, nil
	}
	return SubscriptionRefreshResult{
		Account:        account,
		Subscription:   cloneSubscription(subscription),
		Found:          true,
		PreviousStatus: previousStatus,
		PreviousPlanID: previousPlanID,
	}, nil
}

func (s *MemoryStore) CreateNetwork(_ context.Context, network Network) (Network, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.networks[network.ID]; ok {
		return Network{}, ErrAlreadyExists
	}
	s.networks[network.ID] = network
	return network, nil
}

func (s *MemoryStore) GetNetwork(_ context.Context, id string) (Network, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	network, ok := s.networks[id]
	if !ok {
		return Network{}, ErrNotFound
	}
	return network, nil
}

func (s *MemoryStore) ListAccountNetworks(_ context.Context, accountID string) ([]Network, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var networks []Network
	for _, network := range s.networks {
		if network.AccountID == accountID && network.DeletedAt == nil {
			networks = append(networks, network)
		}
	}
	return networks, nil
}

func (s *MemoryStore) UpdateNetwork(_ context.Context, network Network) (Network, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.networks[network.ID]; !ok {
		return Network{}, ErrNotFound
	}
	s.networks[network.ID] = network
	return network, nil
}

func (s *MemoryStore) CreateMember(_ context.Context, member Member) (Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.members[member.ID]; ok {
		return Member{}, ErrAlreadyExists
	}
	s.members[member.ID] = member
	return member, nil
}

func (s *MemoryStore) CreateOrganization(_ context.Context, organization Organization) (Organization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.organizations[organization.ID]; ok {
		return Organization{}, ErrAlreadyExists
	}
	s.organizations[organization.ID] = organization
	return organization, nil
}

func (s *MemoryStore) GetOrganization(_ context.Context, id string) (Organization, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	organization, ok := s.organizations[id]
	if !ok {
		return Organization{}, ErrNotFound
	}
	return organization, nil
}

func (s *MemoryStore) UpdateOrganization(_ context.Context, organization Organization) (Organization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.organizations[organization.ID]; !ok {
		return Organization{}, ErrNotFound
	}
	s.organizations[organization.ID] = organization
	return organization, nil
}

func (s *MemoryStore) CreateMembership(_ context.Context, membership Membership) (Membership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.memberships[membership.ID]; ok {
		return Membership{}, ErrAlreadyExists
	}
	key := organizationMemberKey(membership.OrganizationID, membership.AccountID)
	if existingID, ok := s.membershipIDs[key]; ok {
		existing := s.memberships[existingID]
		if existing.Status != MembershipStatusRemoved {
			return Membership{}, ErrAlreadyExists
		}
	}
	s.memberships[membership.ID] = membership
	s.membershipIDs[key] = membership.ID
	return membership, nil
}

func (s *MemoryStore) GetOrganizationMembership(_ context.Context, organizationID, accountID string) (Membership, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.membershipIDs[organizationMemberKey(organizationID, accountID)]
	if !ok {
		return Membership{}, ErrNotFound
	}
	membership, ok := s.memberships[id]
	if !ok {
		return Membership{}, ErrNotFound
	}
	return membership, nil
}

func (s *MemoryStore) ListOrganizationMemberships(_ context.Context, organizationID string) ([]Membership, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	memberships := make([]Membership, 0, len(s.memberships))
	for _, membership := range s.memberships {
		if membership.OrganizationID == organizationID {
			memberships = append(memberships, membership)
		}
	}
	sort.Slice(memberships, func(i, j int) bool {
		if memberships[i].CreatedAt.Equal(memberships[j].CreatedAt) {
			return memberships[i].ID < memberships[j].ID
		}
		return memberships[i].CreatedAt.Before(memberships[j].CreatedAt)
	})
	return memberships, nil
}

func (s *MemoryStore) UpdateMembership(_ context.Context, membership Membership) (Membership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.memberships[membership.ID]; !ok {
		return Membership{}, ErrNotFound
	}
	s.memberships[membership.ID] = membership
	s.membershipIDs[organizationMemberKey(membership.OrganizationID, membership.AccountID)] = membership.ID
	return membership, nil
}

func (s *MemoryStore) CreateOrganizationInvite(_ context.Context, invite OrganizationInvite) (OrganizationInvite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.organizationInvites[invite.ID]; ok {
		return OrganizationInvite{}, ErrAlreadyExists
	}
	if _, ok := s.organizationInviteTokenDigestIDs[invite.TokenDigest]; ok {
		return OrganizationInvite{}, ErrAlreadyExists
	}
	s.organizationInvites[invite.ID] = invite
	s.organizationInviteTokenDigestIDs[invite.TokenDigest] = invite.ID
	return invite, nil
}

func (s *MemoryStore) GetOrganizationInvite(_ context.Context, id string) (OrganizationInvite, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	invite, ok := s.organizationInvites[id]
	if !ok {
		return OrganizationInvite{}, ErrNotFound
	}
	return invite, nil
}

func (s *MemoryStore) GetOrganizationInviteByTokenDigest(_ context.Context, tokenDigest string) (OrganizationInvite, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.organizationInviteTokenDigestIDs[tokenDigest]
	if !ok {
		return OrganizationInvite{}, ErrNotFound
	}
	invite, ok := s.organizationInvites[id]
	if !ok {
		return OrganizationInvite{}, ErrNotFound
	}
	return invite, nil
}

func (s *MemoryStore) UpdateOrganizationInvite(_ context.Context, invite OrganizationInvite) (OrganizationInvite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.organizationInvites[invite.ID]
	if !ok {
		return OrganizationInvite{}, ErrNotFound
	}
	if old.TokenDigest != invite.TokenDigest {
		if existingID, ok := s.organizationInviteTokenDigestIDs[invite.TokenDigest]; ok && existingID != invite.ID {
			return OrganizationInvite{}, ErrAlreadyExists
		}
		delete(s.organizationInviteTokenDigestIDs, old.TokenDigest)
		s.organizationInviteTokenDigestIDs[invite.TokenDigest] = invite.ID
	}
	s.organizationInvites[invite.ID] = invite
	return invite, nil
}

func (s *MemoryStore) AcceptOrganizationInvite(_ context.Context, req AcceptOrganizationInviteStoreRequest) (Membership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.organizationInviteTokenDigestIDs[req.TokenDigest]
	if !ok {
		return Membership{}, ErrNotFound
	}
	invite, ok := s.organizationInvites[id]
	if !ok {
		return Membership{}, ErrNotFound
	}
	if invite.OrganizationID != req.OrganizationID {
		return Membership{}, fmt.Errorf("organization invite does not belong to organization: %w", ErrForbidden)
	}
	if invite.InvitedAccountID != req.AccountID {
		return Membership{}, fmt.Errorf("organization invite does not belong to account: %w", ErrForbidden)
	}
	if !constantTimeStringEqual(invite.CodeDigest, req.CodeDigest) {
		return Membership{}, fmt.Errorf("organization invite code is incorrect: %w", ErrForbidden)
	}
	if invite.RevokedAt != nil {
		return Membership{}, fmt.Errorf("organization invite has been revoked: %w", ErrRevoked)
	}
	if !invite.ExpiresAt.IsZero() && req.Now.UTC().After(invite.ExpiresAt.UTC()) {
		return Membership{}, fmt.Errorf("organization invite has expired: %w", ErrExpired)
	}
	if invite.UsedAt != nil {
		return Membership{}, fmt.Errorf("organization invite has already been used: %w", ErrConflict)
	}
	key := organizationMemberKey(req.OrganizationID, req.AccountID)
	if existingID, ok := s.membershipIDs[key]; ok {
		existing := s.memberships[existingID]
		if existing.Status != MembershipStatusRemoved {
			return Membership{}, fmt.Errorf("organization membership already exists: %w", ErrConflict)
		}
	}
	var used int64
	for _, membership := range s.memberships {
		if membership.OrganizationID == req.OrganizationID && membership.Status != MembershipStatusRemoved {
			used++
		}
	}
	if req.Quota.Limited && used >= req.Quota.Limit {
		return Membership{}, newQuotaError(QuotaDimensionMemberCount, used, req.Quota.Limit, req.Quota.Mode, req.PlanID, "member count quota exceeded")
	}
	member := req.Member
	if existingID, ok := s.membershipIDs[key]; ok {
		existing := s.memberships[existingID]
		if existing.Status == MembershipStatusRemoved {
			member.ID = existing.ID
			member.CreatedAt = existing.CreatedAt
		}
	}
	if member.ID == "" {
		return Membership{}, fmt.Errorf("membership id is required")
	}
	if _, ok := s.memberships[member.ID]; ok && s.memberships[member.ID].Status != MembershipStatusRemoved {
		return Membership{}, ErrAlreadyExists
	}
	now := req.Now.UTC()
	invite.UsedAt = &now
	s.organizationInvites[invite.ID] = invite
	s.memberships[member.ID] = member
	s.membershipIDs[key] = member.ID
	return member, nil
}

func (s *MemoryStore) CreateDeviceGroup(_ context.Context, group DeviceGroup) (DeviceGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.deviceGroups[group.ID]; ok {
		return DeviceGroup{}, ErrAlreadyExists
	}
	key := deviceGroupNameKey(group.OrganizationID, group.Name)
	if existingID, ok := s.deviceGroupNameIDs[key]; ok {
		existing := s.deviceGroups[existingID]
		if existing.DeletedAt == nil {
			return DeviceGroup{}, fmt.Errorf("device group name already exists: %w", ErrConflict)
		}
	}
	s.deviceGroups[group.ID] = group
	s.deviceGroupNameIDs[key] = group.ID
	return group, nil
}

func (s *MemoryStore) GetDeviceGroup(_ context.Context, organizationID, groupID string) (DeviceGroup, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	group, ok := s.deviceGroups[groupID]
	if !ok || group.OrganizationID != organizationID {
		return DeviceGroup{}, ErrNotFound
	}
	return group, nil
}

func (s *MemoryStore) ListDeviceGroups(_ context.Context, organizationID string) ([]DeviceGroup, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	groups := make([]DeviceGroup, 0)
	for _, group := range s.deviceGroups {
		if group.OrganizationID == organizationID && group.DeletedAt == nil {
			groups = append(groups, group)
		}
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].CreatedAt.Equal(groups[j].CreatedAt) {
			return groups[i].ID < groups[j].ID
		}
		return groups[i].CreatedAt.Before(groups[j].CreatedAt)
	})
	return groups, nil
}

func (s *MemoryStore) UpdateDeviceGroup(_ context.Context, group DeviceGroup) (DeviceGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.deviceGroups[group.ID]
	if !ok || old.OrganizationID != group.OrganizationID {
		return DeviceGroup{}, ErrNotFound
	}
	oldKey := deviceGroupNameKey(old.OrganizationID, old.Name)
	newKey := deviceGroupNameKey(group.OrganizationID, group.Name)
	if oldKey != newKey {
		if existingID, ok := s.deviceGroupNameIDs[newKey]; ok && existingID != group.ID {
			existing := s.deviceGroups[existingID]
			if existing.DeletedAt == nil {
				return DeviceGroup{}, fmt.Errorf("device group name already exists: %w", ErrConflict)
			}
		}
		delete(s.deviceGroupNameIDs, oldKey)
		s.deviceGroupNameIDs[newKey] = group.ID
	}
	s.deviceGroups[group.ID] = group
	return group, nil
}

func (s *MemoryStore) DeleteDeviceGroup(_ context.Context, organizationID, groupID string, now time.Time) (DeviceGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	group, ok := s.deviceGroups[groupID]
	if !ok || group.OrganizationID != organizationID {
		return DeviceGroup{}, ErrNotFound
	}
	if group.DeletedAt != nil {
		return group, nil
	}
	now = now.UTC()
	group.DeletedAt = &now
	group.UpdatedAt = now
	s.deviceGroups[group.ID] = group
	delete(s.deviceGroupNameIDs, deviceGroupNameKey(group.OrganizationID, group.Name))
	for id, device := range s.organizationDevices {
		if device.OrganizationID == organizationID && device.GroupID == groupID {
			device.GroupID = ""
			device.UpdatedAt = now
			s.organizationDevices[id] = device
		}
	}
	for id, grant := range s.connectionGrants {
		if grant.OrganizationID == organizationID && grant.Scope == ConnectionGrantGroup && grant.GroupID == groupID {
			delete(s.connectionGrantIDs, connectionGrantKey(grant))
			delete(s.connectionGrants, id)
		}
	}
	return group, nil
}

func (s *MemoryStore) CreateOrganizationDevice(_ context.Context, device OrganizationDevice) (OrganizationDevice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.organizationDevices[device.ID]; ok {
		return OrganizationDevice{}, ErrAlreadyExists
	}
	if existingID, ok := s.organizationDeviceIDs[device.DeviceID]; ok {
		existing := s.organizationDevices[existingID]
		if existing.OrganizationID == device.OrganizationID {
			return existing, nil
		}
		return OrganizationDevice{}, fmt.Errorf("device already belongs to another organization: %w", ErrConflict)
	}
	s.organizationDevices[device.ID] = device
	s.organizationDeviceIDs[device.DeviceID] = device.ID
	return device, nil
}

func (s *MemoryStore) GetOrganizationDevice(_ context.Context, organizationID, deviceID string) (OrganizationDevice, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.organizationDeviceIDs[deviceID]
	if !ok {
		return OrganizationDevice{}, ErrNotFound
	}
	device, ok := s.organizationDevices[id]
	if !ok || device.OrganizationID != organizationID {
		return OrganizationDevice{}, ErrNotFound
	}
	return device, nil
}

func (s *MemoryStore) FindOrganizationDevice(_ context.Context, deviceID string) (OrganizationDevice, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.organizationDeviceIDs[deviceID]
	if !ok {
		return OrganizationDevice{}, ErrNotFound
	}
	device, ok := s.organizationDevices[id]
	if !ok {
		return OrganizationDevice{}, ErrNotFound
	}
	return device, nil
}

func (s *MemoryStore) ListOrganizationDevices(_ context.Context, organizationID string) ([]OrganizationDevice, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	devices := make([]OrganizationDevice, 0)
	for _, device := range s.organizationDevices {
		if device.OrganizationID == organizationID {
			devices = append(devices, device)
		}
	}
	sort.Slice(devices, func(i, j int) bool {
		if devices[i].CreatedAt.Equal(devices[j].CreatedAt) {
			return devices[i].ID < devices[j].ID
		}
		return devices[i].CreatedAt.Before(devices[j].CreatedAt)
	})
	return devices, nil
}

func (s *MemoryStore) UpdateOrganizationDevice(_ context.Context, device OrganizationDevice) (OrganizationDevice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.organizationDevices[device.ID]
	if !ok || old.OrganizationID != device.OrganizationID || old.DeviceID != device.DeviceID {
		return OrganizationDevice{}, ErrNotFound
	}
	s.organizationDevices[device.ID] = device
	return device, nil
}

func (s *MemoryStore) DeleteOrganizationDevice(_ context.Context, organizationID, deviceID string) (OrganizationDevice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.organizationDeviceIDs[deviceID]
	if !ok {
		return OrganizationDevice{}, ErrNotFound
	}
	device, ok := s.organizationDevices[id]
	if !ok || device.OrganizationID != organizationID {
		return OrganizationDevice{}, ErrNotFound
	}
	delete(s.organizationDevices, id)
	delete(s.organizationDeviceIDs, deviceID)
	for grantID, grant := range s.connectionGrants {
		if grant.OrganizationID == organizationID && grant.Scope == ConnectionGrantDevice && grant.DeviceID == deviceID {
			delete(s.connectionGrantIDs, connectionGrantKey(grant))
			delete(s.connectionGrants, grantID)
		}
	}
	return device, nil
}

func (s *MemoryStore) CreateDeploymentBundle(_ context.Context, bundle DeploymentBundle, credential BootstrapCredential) (DeploymentBundle, BootstrapCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.deploymentBundles[bundle.ID]; ok {
		return DeploymentBundle{}, BootstrapCredential{}, ErrAlreadyExists
	}
	if _, ok := s.bootstrapCredentials[credential.ID]; ok {
		return DeploymentBundle{}, BootstrapCredential{}, ErrAlreadyExists
	}
	if _, ok := s.bootstrapCredentialDigestIDs[credential.Digest]; ok {
		return DeploymentBundle{}, BootstrapCredential{}, ErrAlreadyExists
	}
	if bundle.OrganizationID != credential.OrganizationID || bundle.ID != credential.BundleID || bundle.CredentialID != credential.ID {
		return DeploymentBundle{}, BootstrapCredential{}, ErrConflict
	}
	s.deploymentBundles[bundle.ID] = cloneDeploymentBundle(bundle)
	s.bootstrapCredentials[credential.ID] = credential
	s.bootstrapCredentialDigestIDs[credential.Digest] = credential.ID
	return cloneDeploymentBundle(bundle), credential, nil
}

func (s *MemoryStore) GetDeploymentBundle(_ context.Context, organizationID, bundleID string) (DeploymentBundle, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bundle, ok := s.deploymentBundles[bundleID]
	if !ok || bundle.OrganizationID != organizationID {
		return DeploymentBundle{}, ErrNotFound
	}
	return cloneDeploymentBundle(bundle), nil
}

func (s *MemoryStore) ListDeploymentBundles(_ context.Context, organizationID string) ([]DeploymentBundle, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bundles := make([]DeploymentBundle, 0)
	for _, bundle := range s.deploymentBundles {
		if bundle.OrganizationID == organizationID {
			bundles = append(bundles, cloneDeploymentBundle(bundle))
		}
	}
	sort.Slice(bundles, func(i, j int) bool {
		if bundles[i].CreatedAt.Equal(bundles[j].CreatedAt) {
			return bundles[i].ID < bundles[j].ID
		}
		return bundles[i].CreatedAt.Before(bundles[j].CreatedAt)
	})
	return bundles, nil
}

func (s *MemoryStore) GetBootstrapCredential(_ context.Context, organizationID, credentialID string) (BootstrapCredential, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	credential, ok := s.bootstrapCredentials[credentialID]
	if !ok || credential.OrganizationID != organizationID {
		return BootstrapCredential{}, ErrNotFound
	}
	return credential, nil
}

func (s *MemoryStore) GetBootstrapCredentialByDigest(_ context.Context, digest string) (BootstrapCredential, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.bootstrapCredentialDigestIDs[digest]
	if !ok {
		return BootstrapCredential{}, ErrNotFound
	}
	credential, ok := s.bootstrapCredentials[id]
	if !ok {
		return BootstrapCredential{}, ErrNotFound
	}
	return credential, nil
}

func (s *MemoryStore) UpdateBootstrapCredential(_ context.Context, credential BootstrapCredential) (BootstrapCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.bootstrapCredentials[credential.ID]
	if !ok || old.OrganizationID != credential.OrganizationID || old.BundleID != credential.BundleID || old.Digest != credential.Digest {
		return BootstrapCredential{}, ErrNotFound
	}
	s.bootstrapCredentials[credential.ID] = credential
	return credential, nil
}

func (s *MemoryStore) RedeemBootstrapCredential(_ context.Context, req RedeemBootstrapCredentialStoreRequest) (BootstrapRedemption, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.bootstrapCredentialDigestIDs[req.Digest]
	if !ok {
		return BootstrapRedemption{}, ErrNotFound
	}
	credential, ok := s.bootstrapCredentials[id]
	if !ok {
		return BootstrapRedemption{}, ErrNotFound
	}
	if credential.OrganizationID != req.OrganizationID || credential.GroupID != req.GroupID || credential.Platform != req.Platform || credential.Architecture != req.Architecture {
		return BootstrapRedemption{}, ErrForbidden
	}
	if credential.RevokedAt != nil || credential.Status == BootstrapCredentialRevoked {
		return BootstrapRedemption{}, fmt.Errorf("bootstrap credential has been revoked: %w", ErrRevoked)
	}
	if credential.Status == BootstrapCredentialExpired || (!credential.ExpiresAt.IsZero() && !req.Now.Before(credential.ExpiresAt)) {
		return BootstrapRedemption{}, fmt.Errorf("bootstrap credential has expired: %w", ErrExpired)
	}
	if credential.Status == BootstrapCredentialExhausted || (credential.MaxUses > 0 && credential.Uses >= credential.MaxUses) {
		return BootstrapRedemption{}, fmt.Errorf("bootstrap credential use limit reached: %w", ErrConflict)
	}
	if credential.Status != BootstrapCredentialActive {
		return BootstrapRedemption{}, ErrForbidden
	}
	organization, ok := s.organizations[credential.OrganizationID]
	if !ok || organization.Status == OrganizationStatusSuspended {
		return BootstrapRedemption{}, ErrForbidden
	}
	owner, ok := s.accounts[organization.OwnerAccountID]
	if !ok || owner.Status != AccountStatusActive {
		return BootstrapRedemption{}, ErrForbidden
	}
	membershipID, ok := s.membershipIDs[organizationMemberKey(organization.ID, owner.ID)]
	if !ok || s.memberships[membershipID].Status != MembershipStatusActive {
		return BootstrapRedemption{}, ErrForbidden
	}
	credentialCreator, ok := s.accounts[credential.CreatedByAccountID]
	if !ok || credentialCreator.Status != AccountStatusActive {
		return BootstrapRedemption{}, ErrForbidden
	}
	creatorMembershipID, ok := s.membershipIDs[organizationMemberKey(organization.ID, credentialCreator.ID)]
	if !ok {
		return BootstrapRedemption{}, ErrForbidden
	}
	creatorMembership := s.memberships[creatorMembershipID]
	if creatorMembership.Status != MembershipStatusActive || (creatorMembership.Role != MembershipRoleOwner && creatorMembership.Role != MembershipRoleAdmin) {
		return BootstrapRedemption{}, ErrForbidden
	}
	network, ok := s.networks[credential.NetworkID]
	if !ok || network.AccountID != owner.ID || network.DeletedAt != nil {
		return BootstrapRedemption{}, ErrForbidden
	}
	if credential.GroupID != "" {
		group, ok := s.deviceGroups[credential.GroupID]
		if !ok || group.OrganizationID != organization.ID || group.DeletedAt != nil {
			return BootstrapRedemption{}, ErrForbidden
		}
	}
	if req.Device.AccountID != owner.ID || req.Device.NetworkID != network.ID || req.OrgDevice.OrganizationID != organization.ID || req.OrgDevice.DeviceID != req.Device.ID || req.OrgDevice.GroupID != credential.GroupID {
		return BootstrapRedemption{}, ErrForbidden
	}
	if req.Quota.Limited {
		var used int64
		for _, device := range s.devices {
			if device.AccountID == owner.ID && device.Status != DeviceStatusRevoked && device.RevokedAt == nil {
				used++
			}
		}
		if used >= req.Quota.Limit {
			return BootstrapRedemption{}, newQuotaError(QuotaDimensionDeviceCount, used, req.Quota.Limit, req.Quota.Mode, req.PlanID, "device count quota exceeded")
		}
	}
	if _, ok := s.devices[req.Device.ID]; ok {
		return BootstrapRedemption{}, ErrAlreadyExists
	}
	if _, ok := s.organizationDevices[req.OrgDevice.ID]; ok {
		return BootstrapRedemption{}, ErrAlreadyExists
	}
	if _, ok := s.organizationDeviceIDs[req.Device.ID]; ok {
		return BootstrapRedemption{}, ErrConflict
	}
	for _, device := range s.devices {
		if req.Device.Fingerprint != "" && device.Fingerprint == req.Device.Fingerprint && device.RevokedAt == nil {
			return BootstrapRedemption{}, fmt.Errorf("device identity has already been registered: %w", ErrConflict)
		}
	}
	credential.Uses++
	if credential.MaxUses > 0 && credential.Uses >= credential.MaxUses {
		credential.Status = BootstrapCredentialExhausted
	}
	s.bootstrapCredentials[credential.ID] = credential
	s.devices[req.Device.ID] = cloneDevice(req.Device)
	s.organizationDevices[req.OrgDevice.ID] = req.OrgDevice
	s.organizationDeviceIDs[req.Device.ID] = req.OrgDevice.ID
	req.Audit.Metadata = cloneMetadata(req.Audit.Metadata)
	s.auditEvents = append(s.auditEvents, req.Audit)
	return BootstrapRedemption{Device: cloneDevice(req.Device), OrganizationDevice: req.OrgDevice, Credential: credential}, nil
}

func (s *MemoryStore) CreateRollout(_ context.Context, rollout Rollout, targets []DeploymentTarget) (Rollout, []DeploymentTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rollouts[rollout.ID]; ok {
		return Rollout{}, nil, ErrAlreadyExists
	}
	if len(targets) == 0 {
		return Rollout{}, nil, ErrConflict
	}
	for _, target := range targets {
		if _, ok := s.deploymentTargets[deploymentTargetKey(target.RolloutID, target.DeviceID)]; ok || target.RolloutID != rollout.ID || target.OrganizationID != rollout.OrganizationID {
			return Rollout{}, nil, ErrConflict
		}
		organizationDeviceID, ok := s.organizationDeviceIDs[target.DeviceID]
		if !ok || s.organizationDevices[organizationDeviceID].OrganizationID != rollout.OrganizationID {
			return Rollout{}, nil, ErrNotFound
		}
		device, ok := s.devices[target.DeviceID]
		if !ok {
			return Rollout{}, nil, ErrNotFound
		}
		if device.RolloutID != "" || device.TargetVersion != "" {
			return Rollout{}, nil, fmt.Errorf("device already has an active rollout: %w", ErrConflict)
		}
	}
	s.rollouts[rollout.ID] = rollout
	created := make([]DeploymentTarget, len(targets))
	copy(created, targets)
	for _, target := range created {
		s.deploymentTargets[deploymentTargetKey(target.RolloutID, target.DeviceID)] = target
		device := s.devices[target.DeviceID]
		device.RolloutID = rollout.ID
		device.TargetVersion = target.TargetVersion
		s.devices[target.DeviceID] = cloneDevice(device)
	}
	sortDeploymentTargets(created)
	return rollout, created, nil
}

func (s *MemoryStore) GetRollout(_ context.Context, organizationID, rolloutID string) (Rollout, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rollout, ok := s.rollouts[rolloutID]
	if !ok || rollout.OrganizationID != organizationID {
		return Rollout{}, ErrNotFound
	}
	return rollout, nil
}

func (s *MemoryStore) FindRollout(_ context.Context, rolloutID string) (Rollout, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rollout, ok := s.rollouts[rolloutID]
	if !ok {
		return Rollout{}, ErrNotFound
	}
	return rollout, nil
}

func (s *MemoryStore) ListRollouts(_ context.Context, organizationID string) ([]Rollout, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rollouts := make([]Rollout, 0)
	for _, rollout := range s.rollouts {
		if rollout.OrganizationID == organizationID {
			rollouts = append(rollouts, rollout)
		}
	}
	sort.Slice(rollouts, func(i, j int) bool {
		if rollouts[i].CreatedAt.Equal(rollouts[j].CreatedAt) {
			return rollouts[i].ID < rollouts[j].ID
		}
		return rollouts[i].CreatedAt.Before(rollouts[j].CreatedAt)
	})
	return rollouts, nil
}

func (s *MemoryStore) UpdateRollout(_ context.Context, rollout Rollout) (Rollout, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.rollouts[rollout.ID]
	if !ok || old.OrganizationID != rollout.OrganizationID {
		return Rollout{}, ErrNotFound
	}
	if old.Status == RolloutStatusCanceled && rollout.Status != RolloutStatusCanceled {
		return old, nil
	}
	s.rollouts[rollout.ID] = rollout
	return rollout, nil
}

func (s *MemoryStore) GetDeploymentTarget(_ context.Context, organizationID, rolloutID, deviceID string) (DeploymentTarget, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	target, ok := s.deploymentTargets[deploymentTargetKey(rolloutID, deviceID)]
	if !ok || target.OrganizationID != organizationID {
		return DeploymentTarget{}, ErrNotFound
	}
	return target, nil
}

func (s *MemoryStore) ListDeploymentTargets(_ context.Context, organizationID, rolloutID string) ([]DeploymentTarget, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rollout, ok := s.rollouts[rolloutID]
	if !ok || rollout.OrganizationID != organizationID {
		return nil, ErrNotFound
	}
	targets := make([]DeploymentTarget, 0)
	for _, target := range s.deploymentTargets {
		if target.OrganizationID == organizationID && target.RolloutID == rolloutID {
			targets = append(targets, target)
		}
	}
	sortDeploymentTargets(targets)
	return targets, nil
}

func (s *MemoryStore) UpdateDeploymentTarget(_ context.Context, target DeploymentTarget) (DeploymentTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := deploymentTargetKey(target.RolloutID, target.DeviceID)
	old, ok := s.deploymentTargets[key]
	if !ok || old.OrganizationID != target.OrganizationID || old.ID != target.ID {
		return DeploymentTarget{}, ErrNotFound
	}
	if target.Attempt < old.Attempt || (target.Attempt == old.Attempt && target.ReportSequence < old.ReportSequence) {
		return old, nil
	}
	if old.Status == DeploymentTargetSucceeded || old.Status == DeploymentTargetCanceled {
		return old, nil
	}
	if target.Attempt == old.Attempt && target.ReportSequence == old.ReportSequence && !(old.Status == DeploymentTargetPending && target.Status == DeploymentTargetCanceled) {
		return old, nil
	}
	if target.Attempt > old.Attempt && (old.Status != DeploymentTargetFailed || target.Attempt != old.Attempt+1 || target.Status != DeploymentTargetPending) {
		return old, nil
	}
	device, ok := s.devices[target.DeviceID]
	if !ok {
		return DeploymentTarget{}, ErrNotFound
	}
	s.deploymentTargets[key] = target
	switch target.Status {
	case DeploymentTargetSucceeded:
		device.CurrentVersion = target.TargetVersion
		device.RolloutID = ""
		device.TargetVersion = ""
	case DeploymentTargetCanceled:
		if device.RolloutID == target.RolloutID {
			device.RolloutID = ""
			device.TargetVersion = ""
		}
	default:
		device.RolloutID = target.RolloutID
		device.TargetVersion = target.TargetVersion
	}
	s.devices[target.DeviceID] = cloneDevice(device)
	return target, nil
}

func (s *MemoryStore) CreateConnectionGrant(_ context.Context, grant ConnectionGrant) (ConnectionGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.connectionGrants[grant.ID]; ok {
		return ConnectionGrant{}, ErrAlreadyExists
	}
	key := connectionGrantKey(grant)
	if existingID, ok := s.connectionGrantIDs[key]; ok {
		return s.connectionGrants[existingID], nil
	}
	s.connectionGrants[grant.ID] = grant
	s.connectionGrantIDs[key] = grant.ID
	return grant, nil
}

func (s *MemoryStore) GetConnectionGrant(_ context.Context, organizationID, grantID string) (ConnectionGrant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	grant, ok := s.connectionGrants[grantID]
	if !ok || grant.OrganizationID != organizationID {
		return ConnectionGrant{}, ErrNotFound
	}
	return grant, nil
}

func (s *MemoryStore) ListConnectionGrants(_ context.Context, organizationID string) ([]ConnectionGrant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	grants := make([]ConnectionGrant, 0)
	for _, grant := range s.connectionGrants {
		if grant.OrganizationID == organizationID {
			grants = append(grants, grant)
		}
	}
	sort.Slice(grants, func(i, j int) bool {
		if grants[i].CreatedAt.Equal(grants[j].CreatedAt) {
			return grants[i].ID < grants[j].ID
		}
		return grants[i].CreatedAt.Before(grants[j].CreatedAt)
	})
	return grants, nil
}

func (s *MemoryStore) DeleteConnectionGrant(_ context.Context, organizationID, grantID string) (ConnectionGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	grant, ok := s.connectionGrants[grantID]
	if !ok || grant.OrganizationID != organizationID {
		return ConnectionGrant{}, ErrNotFound
	}
	delete(s.connectionGrants, grantID)
	delete(s.connectionGrantIDs, connectionGrantKey(grant))
	return grant, nil
}

func (s *MemoryStore) CreateInvite(_ context.Context, invite Invite) (Invite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.invites[invite.ID]; ok {
		return Invite{}, ErrAlreadyExists
	}
	if _, ok := s.inviteTokenIDs[invite.Token]; ok {
		return Invite{}, ErrAlreadyExists
	}
	s.invites[invite.ID] = invite
	s.inviteTokenIDs[invite.Token] = invite.ID
	return invite, nil
}

func (s *MemoryStore) GetInvite(_ context.Context, id string) (Invite, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	invite, ok := s.invites[id]
	if !ok {
		return Invite{}, ErrNotFound
	}
	return invite, nil
}

func (s *MemoryStore) GetInviteByToken(_ context.Context, token string) (Invite, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.inviteTokenIDs[token]
	if !ok {
		return Invite{}, ErrNotFound
	}
	invite, ok := s.invites[id]
	if !ok {
		return Invite{}, ErrNotFound
	}
	return invite, nil
}

func (s *MemoryStore) UpdateInvite(_ context.Context, invite Invite) (Invite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.invites[invite.ID]
	if !ok {
		return Invite{}, ErrNotFound
	}
	if old.Token != invite.Token {
		if existingID, ok := s.inviteTokenIDs[invite.Token]; ok && existingID != invite.ID {
			return Invite{}, ErrAlreadyExists
		}
		delete(s.inviteTokenIDs, old.Token)
		s.inviteTokenIDs[invite.Token] = invite.ID
	}
	s.invites[invite.ID] = invite
	return invite, nil
}

func (s *MemoryStore) CreateDevice(_ context.Context, device Device) (Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.devices[device.ID]; ok {
		return Device{}, ErrAlreadyExists
	}
	cloned := cloneDevice(device)
	s.devices[device.ID] = cloned
	return cloneDevice(cloned), nil
}

func (s *MemoryStore) GetDevice(_ context.Context, id string) (Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	device, ok := s.devices[id]
	if !ok {
		return Device{}, ErrNotFound
	}
	return cloneDevice(device), nil
}

func (s *MemoryStore) UpdateDevice(_ context.Context, device Device) (Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.devices[device.ID]; !ok {
		return Device{}, ErrNotFound
	}
	cloned := cloneDevice(device)
	s.devices[device.ID] = cloned
	return cloneDevice(cloned), nil
}

func (s *MemoryStore) ListNetworkDevices(_ context.Context, networkID string) ([]Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var devices []Device
	for _, device := range s.devices {
		if device.NetworkID == networkID {
			devices = append(devices, cloneDevice(device))
		}
	}
	return devices, nil
}

func (s *MemoryStore) ReplaceP2PCandidates(_ context.Context, deviceID string, candidates []p2p.Candidate) ([]p2p.Candidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.devices[deviceID]; !ok {
		return nil, ErrNotFound
	}
	cloned := cloneP2PCandidates(candidates)
	s.p2pCandidates[deviceID] = cloned
	return cloneP2PCandidates(cloned), nil
}

func (s *MemoryStore) ListP2PCandidates(_ context.Context, networkID, deviceID string) ([]p2p.Candidate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []p2p.Candidate
	if deviceID != "" {
		candidates := s.p2pCandidates[deviceID]
		for _, candidate := range candidates {
			if networkID == "" || candidate.NetworkID == networkID {
				out = append(out, candidate)
			}
		}
		return cloneP2PCandidates(out), nil
	}
	for _, candidates := range s.p2pCandidates {
		for _, candidate := range candidates {
			if networkID == "" || candidate.NetworkID == networkID {
				out = append(out, candidate)
			}
		}
	}
	return cloneP2PCandidates(out), nil
}

func (s *MemoryStore) CreateSession(_ context.Context, session Session) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[session.ID]; ok {
		return Session{}, ErrAlreadyExists
	}
	s.sessions[session.ID] = session
	return session, nil
}

func (s *MemoryStore) CreateRelaySession(_ context.Context, session RelaySession) (RelaySession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.relaySessions[session.ID]; ok {
		return RelaySession{}, ErrAlreadyExists
	}
	s.relaySessions[session.ID] = session
	return session, nil
}

func (s *MemoryStore) GetRelaySession(_ context.Context, id string) (RelaySession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.relaySessions[id]
	if !ok {
		return RelaySession{}, ErrNotFound
	}
	return session, nil
}

func (s *MemoryStore) UpdateRelaySession(_ context.Context, session RelaySession) (RelaySession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.relaySessions[session.ID]; !ok {
		return RelaySession{}, ErrNotFound
	}
	s.relaySessions[session.ID] = session
	return session, nil
}

func (s *MemoryStore) ListRelaySessions(_ context.Context, accountID string) ([]RelaySession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sessions := make([]RelaySession, 0, len(s.relaySessions))
	for _, session := range s.relaySessions {
		if accountID == "" || session.AccountID == accountID {
			sessions = append(sessions, session)
		}
	}
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].CreatedAt.Equal(sessions[j].CreatedAt) {
			return sessions[i].ID < sessions[j].ID
		}
		return sessions[i].CreatedAt.Before(sessions[j].CreatedAt)
	})
	return sessions, nil
}

func (s *MemoryStore) CreateConnectionLog(_ context.Context, log ConnectionLog) (ConnectionLog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.connectionLogs[log.ID]; ok {
		return ConnectionLog{}, ErrAlreadyExists
	}
	log = cloneConnectionLog(log)
	s.connectionLogs[log.ID] = log
	return cloneConnectionLog(log), nil
}

func (s *MemoryStore) ListConnectionLogs(_ context.Context, sessionID string) ([]ConnectionLog, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	logs := make([]ConnectionLog, 0, len(s.connectionLogs))
	for _, log := range s.connectionLogs {
		if sessionID == "" || log.SessionID == sessionID {
			logs = append(logs, cloneConnectionLog(log))
		}
	}
	sort.Slice(logs, func(i, j int) bool {
		if logs[i].StartedAt.Equal(logs[j].StartedAt) {
			return logs[i].ID < logs[j].ID
		}
		return logs[i].StartedAt.Before(logs[j].StartedAt)
	})
	return logs, nil
}

func (s *MemoryStore) ListOrganizationConnectionLogs(_ context.Context, organizationID string) ([]ConnectionLog, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	organizationID = strings.TrimSpace(organizationID)
	logs := make([]ConnectionLog, 0, len(s.connectionLogs))
	for _, log := range s.connectionLogs {
		if strings.TrimSpace(log.OrganizationID) == organizationID && organizationID != "" {
			logs = append(logs, cloneConnectionLog(log))
		}
	}
	return logs, nil
}

func (s *MemoryStore) CreateRelayUsage(_ context.Context, usage RelayUsage) (RelayUsage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.relayUsage[usage.ID]; ok {
		return RelayUsage{}, ErrAlreadyExists
	}
	s.relayUsage[usage.ID] = usage
	return usage, nil
}

func (s *MemoryStore) ListRelayUsage(_ context.Context, sessionID string) ([]RelayUsage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	usageRows := make([]RelayUsage, 0, len(s.relayUsage))
	for _, usage := range s.relayUsage {
		if sessionID == "" || usage.SessionID == sessionID {
			usageRows = append(usageRows, usage)
		}
	}
	sort.Slice(usageRows, func(i, j int) bool {
		if usageRows[i].RecordedAt.Equal(usageRows[j].RecordedAt) {
			return usageRows[i].ID < usageRows[j].ID
		}
		return usageRows[i].RecordedAt.Before(usageRows[j].RecordedAt)
	})
	return usageRows, nil
}

func (s *MemoryStore) CreateBan(_ context.Context, ban Ban) (Ban, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.bans[ban.ID]; ok {
		return Ban{}, ErrAlreadyExists
	}
	s.bans[ban.ID] = ban
	return ban, nil
}

func (s *MemoryStore) CreateRevocation(_ context.Context, revocation Revocation) (Revocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.revocations[revocation.ID]; ok {
		return Revocation{}, ErrAlreadyExists
	}
	s.revocations[revocation.ID] = revocation
	return revocation, nil
}

func (s *MemoryStore) AddRiskEvent(_ context.Context, event RiskEvent) (RiskEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	event.Metadata = cloneMetadata(event.Metadata)
	s.riskEvents = append(s.riskEvents, event)
	return event, nil
}

func (s *MemoryStore) ListRiskEvents(_ context.Context, accountID string) ([]RiskEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	events := make([]RiskEvent, 0, len(s.riskEvents))
	for _, event := range s.riskEvents {
		if accountID == "" || event.AccountID == accountID {
			event.Metadata = cloneMetadata(event.Metadata)
			events = append(events, event)
		}
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].Time.Equal(events[j].Time) {
			return events[i].ID < events[j].ID
		}
		return events[i].Time.Before(events[j].Time)
	})
	return events, nil
}

func (s *MemoryStore) AddAuditEvent(_ context.Context, event AuditEvent) (AuditEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	event.Metadata = cloneMetadata(event.Metadata)
	s.auditEvents = append(s.auditEvents, event)
	return event, nil
}

func (s *MemoryStore) ListAuditEvents(_ context.Context) ([]AuditEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	events := make([]AuditEvent, len(s.auditEvents))
	for i, event := range s.auditEvents {
		event.Metadata = cloneMetadata(event.Metadata)
		events[i] = event
	}
	return events, nil
}

func (s *MemoryStore) ListOrganizationAuditEvents(_ context.Context, organizationID string) ([]AuditEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	organizationID = strings.TrimSpace(organizationID)
	events := make([]AuditEvent, 0, len(s.auditEvents))
	for _, event := range s.auditEvents {
		if auditEventOrganizationID(event) != organizationID || organizationID == "" {
			continue
		}
		event.Metadata = cloneMetadata(event.Metadata)
		events = append(events, event)
	}
	return events, nil
}

func (s *MemoryStore) DeleteOrganizationAuditBefore(_ context.Context, organizationID string, cutoff time.Time, limit int) (OrganizationAuditStoreCleanupResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	organizationID = strings.TrimSpace(organizationID)
	if organizationID == "" {
		return OrganizationAuditStoreCleanupResult{}, fmt.Errorf("organization_id is required")
	}
	if cutoff.IsZero() {
		return OrganizationAuditStoreCleanupResult{}, fmt.Errorf("cutoff is required")
	}
	if limit <= 0 {
		return OrganizationAuditStoreCleanupResult{}, fmt.Errorf("limit must be positive")
	}
	type cleanupCandidate struct {
		kind       string
		id         string
		auditIndex int
		at         time.Time
	}
	candidates := make([]cleanupCandidate, 0)
	for i, event := range s.auditEvents {
		if auditEventOrganizationID(event) == organizationID && event.Time.Before(cutoff) {
			candidates = append(candidates, cleanupCandidate{kind: OrganizationAuditKindEvent, id: event.ID, auditIndex: i, at: event.Time})
		}
	}
	for id, log := range s.connectionLogs {
		if strings.TrimSpace(log.OrganizationID) == organizationID && log.StartedAt.Before(cutoff) {
			candidates = append(candidates, cleanupCandidate{kind: OrganizationAuditKindConnection, id: id, auditIndex: -1, at: log.StartedAt})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].at.Equal(candidates[j].at) {
			return candidates[i].at.Before(candidates[j].at)
		}
		if candidates[i].kind != candidates[j].kind {
			return candidates[i].kind < candidates[j].kind
		}
		return candidates[i].id < candidates[j].id
	})
	deleteCount := limit
	if deleteCount > len(candidates) {
		deleteCount = len(candidates)
	}
	removeAudit := map[int]struct{}{}
	result := OrganizationAuditStoreCleanupResult{HasMore: len(candidates) > deleteCount}
	for _, candidate := range candidates[:deleteCount] {
		if candidate.kind == OrganizationAuditKindEvent {
			removeAudit[candidate.auditIndex] = struct{}{}
			result.DeletedAuditEvents++
			continue
		}
		delete(s.connectionLogs, candidate.id)
		result.DeletedConnectionLogs++
	}
	if len(removeAudit) > 0 {
		kept := make([]AuditEvent, 0, len(s.auditEvents)-len(removeAudit))
		for i, event := range s.auditEvents {
			if _, remove := removeAudit[i]; !remove {
				kept = append(kept, event)
			}
		}
		s.auditEvents = kept
	}
	return result, nil
}

func auditEventOrganizationID(event AuditEvent) string {
	value, ok := event.Metadata["organization_id"].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func cloneConnectionLog(log ConnectionLog) ConnectionLog {
	log.SwitchReasons = append([]string(nil), log.SwitchReasons...)
	return log
}

func subscriptionEventKey(provider, eventID string) string {
	return strings.TrimSpace(provider) + "\x00" + strings.TrimSpace(eventID)
}

func organizationMemberKey(organizationID, accountID string) string {
	return strings.TrimSpace(organizationID) + "\x00" + strings.TrimSpace(accountID)
}

func deviceGroupNameKey(organizationID, name string) string {
	return strings.TrimSpace(organizationID) + "\x00" + strings.ToLower(strings.TrimSpace(name))
}

func connectionGrantKey(grant ConnectionGrant) string {
	resourceID := grant.DeviceID
	if grant.Scope == ConnectionGrantGroup {
		resourceID = grant.GroupID
	}
	return strings.Join([]string{
		strings.TrimSpace(grant.OrganizationID),
		strings.TrimSpace(grant.MemberAccountID),
		string(grant.Scope),
		strings.TrimSpace(resourceID),
	}, "\x00")
}

func deploymentTargetKey(rolloutID, deviceID string) string {
	return strings.TrimSpace(rolloutID) + "\x00" + strings.TrimSpace(deviceID)
}

func subscriptionEventIsStale(existing Subscription, event SubscriptionEvent) bool {
	if existing.Version > 0 && event.Version > 0 {
		return event.Version <= existing.Version
	}
	if existing.Version > 0 && event.Version <= 0 {
		return true
	}
	if !existing.EffectiveAt.IsZero() && !event.EffectiveAt.IsZero() {
		return !event.EffectiveAt.After(existing.EffectiveAt)
	}
	return false
}

func effectiveSubscriptionStatus(status SubscriptionStatus, effectiveUntil *time.Time, now time.Time) SubscriptionStatus {
	if status == SubscriptionStatusCanceled && effectiveUntil != nil && !now.UTC().Before(effectiveUntil.UTC()) {
		return SubscriptionStatusExpired
	}
	return status
}

func accountPlanForSubscriptionEvent(currentPlan PlanID, event SubscriptionEvent, subscription Subscription, now time.Time) PlanID {
	switch subscription.Status {
	case SubscriptionStatusActive:
		return subscription.PlanID
	case SubscriptionStatusCanceled:
		if subscription.EffectiveUntil != nil && now.UTC().Before(subscription.EffectiveUntil.UTC()) {
			return subscription.PlanID
		}
		return PlanFree
	case SubscriptionStatusExpired:
		return PlanFree
	case SubscriptionStatusPending, SubscriptionStatusPastDue:
		if currentPlan == event.PlanID {
			return PlanFree
		}
		return currentPlan
	default:
		return currentPlan
	}
}

func cloneSubscription(subscription Subscription) Subscription {
	subscription.EffectiveAt = subscription.EffectiveAt.UTC()
	subscription.EffectiveUntil = cloneTimePtr(subscription.EffectiveUntil)
	subscription.CreatedAt = subscription.CreatedAt.UTC()
	subscription.UpdatedAt = subscription.UpdatedAt.UTC()
	return subscription
}

func cloneMetadata(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneDevice(device Device) Device {
	device.NATProbe = p2p.CloneNATProbeSummary(device.NATProbe)
	return device
}

func cloneP2PCandidates(in []p2p.Candidate) []p2p.Candidate {
	if len(in) == 0 {
		return nil
	}
	out := make([]p2p.Candidate, len(in))
	copy(out, in)
	return out
}
