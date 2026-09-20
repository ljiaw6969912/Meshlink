package cloudhub

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func (s *Service) planForAccount(account Account) (Plan, error) {
	planID := PlanID(strings.TrimSpace(string(account.PlanID)))
	if planID == "" {
		return Plan{}, fmt.Errorf("account has no plan binding: %w", ErrNotFound)
	}
	plan, ok := LookupPlan(planID)
	if !ok {
		return Plan{}, fmt.Errorf("plan was not found: %w", ErrNotFound)
	}
	return plan, nil
}

func (s *Service) enforceDeviceCountQuotaLocked(ctx context.Context, account Account, networkID string) error {
	if strings.TrimSpace(string(account.PlanID)) == "" {
		return nil
	}
	plan, err := s.planForAccount(account)
	if err != nil {
		return err
	}
	decision, err := s.evaluateQuotaLocked(ctx, account.ID, QuotaDimensionDeviceCount, plan.Entitlements.DeviceCount)
	if err != nil {
		s.recordQuotaDeniedLocked(ctx, account.ID, networkID, "", withQuotaPlan(err, plan.ID))
		return withQuotaPlan(err, plan.ID)
	}
	if !decision.Limited {
		return nil
	}
	used, err := s.countAccountDevicesLocked(ctx, account.ID, false)
	if err != nil {
		return err
	}
	if used >= decision.Limit {
		quotaErr := newQuotaError(QuotaDimensionDeviceCount, used, decision.Limit, decision.Mode, plan.ID, "device count quota exceeded")
		s.recordQuotaDeniedLocked(ctx, account.ID, networkID, "", quotaErr)
		return quotaErr
	}
	return nil
}

func (s *Service) enforceOnlineDeviceQuotaLocked(ctx context.Context, account Account, device Device, nextStatus DeviceStatus) error {
	if strings.TrimSpace(string(account.PlanID)) == "" || device.Status == DeviceStatusOnline || nextStatus != DeviceStatusOnline {
		return nil
	}
	plan, err := s.planForAccount(account)
	if err != nil {
		return err
	}
	decision, err := s.evaluateQuotaLocked(ctx, account.ID, QuotaDimensionConcurrentOnlineDevices, plan.Entitlements.ConcurrentOnlineDevices)
	if err != nil {
		s.recordQuotaDeniedLocked(ctx, account.ID, device.NetworkID, device.ID, withQuotaPlan(err, plan.ID))
		return withQuotaPlan(err, plan.ID)
	}
	if !decision.Limited {
		return nil
	}
	used, err := s.countAccountDevicesLocked(ctx, account.ID, true)
	if err != nil {
		return err
	}
	if used >= decision.Limit {
		quotaErr := newQuotaError(QuotaDimensionConcurrentOnlineDevices, used, decision.Limit, decision.Mode, plan.ID, "concurrent online device quota exceeded")
		s.recordQuotaDeniedLocked(ctx, account.ID, device.NetworkID, device.ID, quotaErr)
		return quotaErr
	}
	return nil
}

func (s *Service) countAccountDevicesLocked(ctx context.Context, accountID string, onlineOnly bool) (int64, error) {
	networks, err := s.store.ListAccountNetworks(ctx, accountID)
	if err != nil {
		return 0, err
	}
	var count int64
	for _, network := range networks {
		devices, err := s.store.ListNetworkDevices(ctx, network.ID)
		if err != nil {
			return 0, err
		}
		for _, device := range devices {
			if device.AccountID != accountID || device.RevokedAt != nil || device.Status == DeviceStatusRevoked {
				continue
			}
			if onlineOnly && device.Status != DeviceStatusOnline {
				continue
			}
			count++
		}
	}
	return count, nil
}

func (s *Service) recordQuotaDeniedLocked(ctx context.Context, accountID, networkID, deviceID string, err error) {
	var quotaErr *QuotaError
	if !errors.As(err, &quotaErr) {
		return
	}
	metadata := map[string]any{
		"category":  quotaErr.Detail.Category,
		"dimension": string(quotaErr.Detail.Dimension),
		"used":      quotaErr.Detail.Used,
		"limit":     quotaErr.Detail.Limit,
	}
	if quotaErr.Detail.Mode != "" {
		metadata["mode"] = string(quotaErr.Detail.Mode)
	}
	if quotaErr.Detail.PlanID != "" {
		metadata["plan_id"] = string(quotaErr.Detail.PlanID)
	}
	_ = s.recordRiskEvent(ctx, RiskQuotaExceeded, accountID, networkID, deviceID, quotaErr.Error(), metadata)
}

func withQuotaPlan(err error, planID PlanID) error {
	var quotaErr *QuotaError
	if !errors.As(err, &quotaErr) {
		return err
	}
	quotaErr.Detail.PlanID = planID
	return quotaErr
}
