package cloudhub

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"meshlink/internal/p2p"
)

func TestP2PRelaySessionManagerCreatesSanitizedGrantForOnlineDevices(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	account, network := mustAccountAndNetwork(t, ctx, svc)
	source, target := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)
	mustHeartbeatOnlineForP2PFallback(t, ctx, svc, source.ID, target.ID)

	manager := P2PRelaySessionManager{Service: svc, Endpoint: "127.0.0.1:18082"}
	grant, err := manager.CreateRelaySession(ctx, p2p.RelaySessionRequest{
		AccountID:      account.ID,
		NetworkID:      network.ID,
		SourceDeviceID: source.ID,
		TargetDeviceID: target.ID,
		TTL:            time.Minute,
	})
	if err != nil {
		t.Fatalf("CreateRelaySession returned error: %v", err)
	}
	if grant.ID == "" || grant.PathType != p2p.PathTypeRelay || grant.Endpoint != "127.0.0.1:18082" {
		t.Fatalf("grant = %+v, want relay session id/path/endpoint", grant)
	}
	if grant.SourceJoinToken == "" || grant.TargetJoinToken == "" {
		t.Fatal("grant join tokens are required for in-memory runtime authorization")
	}
	encoded, err := json.Marshal(grant)
	if err != nil {
		t.Fatalf("marshal grant: %v", err)
	}
	for _, forbidden := range []string{grant.SourceJoinToken, grant.TargetJoinToken, "join_token", "token_hash", "private_key"} {
		if forbidden != "" && strings.Contains(strings.ToLower(string(encoded)), strings.ToLower(forbidden)) {
			t.Fatal("grant JSON leaks a forbidden sensitive value")
		}
	}
}

func TestP2PRelaySessionManagerRejectsUnsafeFallbackBoundaries(t *testing.T) {
	ctx := context.Background()

	t.Run("offline target", func(t *testing.T) {
		svc := NewService(NewMemoryStore())
		account, network := mustAccountAndNetwork(t, ctx, svc)
		source, target := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)
		mustHeartbeatOnlineForP2PFallback(t, ctx, svc, source.ID)

		_, err := P2PRelaySessionManager{Service: svc}.CreateRelaySession(ctx, p2p.RelaySessionRequest{
			AccountID:      account.ID,
			NetworkID:      network.ID,
			SourceDeviceID: source.ID,
			TargetDeviceID: target.ID,
		})
		if err == nil || !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "online") {
			t.Fatalf("offline target error = %v, want online ErrForbidden", err)
		}
	})

	t.Run("revoked target", func(t *testing.T) {
		svc := NewService(NewMemoryStore())
		account, network := mustAccountAndNetwork(t, ctx, svc)
		source, target := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)
		mustHeartbeatOnlineForP2PFallback(t, ctx, svc, source.ID, target.ID)
		if _, err := svc.RevokeDevice(ctx, RevokeDeviceRequest{DeviceID: target.ID, Reason: "retired"}); err != nil {
			t.Fatalf("RevokeDevice returned error: %v", err)
		}

		_, err := P2PRelaySessionManager{Service: svc}.CreateRelaySession(ctx, p2p.RelaySessionRequest{
			AccountID:      account.ID,
			NetworkID:      network.ID,
			SourceDeviceID: source.ID,
			TargetDeviceID: target.ID,
		})
		if err == nil || !errors.Is(err, ErrRevoked) {
			t.Fatalf("revoked target error = %v, want ErrRevoked", err)
		}
	})

	t.Run("frozen account", func(t *testing.T) {
		svc := NewService(NewMemoryStore())
		account, network := mustAccountAndNetwork(t, ctx, svc)
		source, target := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)
		mustHeartbeatOnlineForP2PFallback(t, ctx, svc, source.ID, target.ID)
		if _, err := svc.FreezeAccount(ctx, AccountStatusChangeRequest{AccountID: account.ID, Reason: "risk review"}); err != nil {
			t.Fatalf("FreezeAccount returned error: %v", err)
		}

		_, err := P2PRelaySessionManager{Service: svc}.CreateRelaySession(ctx, p2p.RelaySessionRequest{
			AccountID:      account.ID,
			NetworkID:      network.ID,
			SourceDeviceID: source.ID,
			TargetDeviceID: target.ID,
		})
		if err == nil || !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "frozen") {
			t.Fatalf("frozen account error = %v, want frozen ErrForbidden", err)
		}
	})

	t.Run("banned account", func(t *testing.T) {
		svc := NewService(NewMemoryStore())
		account, network := mustAccountAndNetwork(t, ctx, svc)
		source, target := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)
		mustHeartbeatOnlineForP2PFallback(t, ctx, svc, source.ID, target.ID)
		if _, err := svc.BanAccount(ctx, AccountStatusChangeRequest{AccountID: account.ID, Reason: "abuse"}); err != nil {
			t.Fatalf("BanAccount returned error: %v", err)
		}

		_, err := P2PRelaySessionManager{Service: svc}.CreateRelaySession(ctx, p2p.RelaySessionRequest{
			AccountID:      account.ID,
			NetworkID:      network.ID,
			SourceDeviceID: source.ID,
			TargetDeviceID: target.ID,
		})
		if err == nil || !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "banned") {
			t.Fatalf("banned account error = %v, want banned ErrForbidden", err)
		}
	})

	t.Run("relay quota exceeded", func(t *testing.T) {
		svc := NewService(NewMemoryStore(),
			WithAccountPolicies("tiny-p2p", AccountPolicy{
				Name:                   "tiny-p2p",
				RelayBytesQuota:        32,
				MaxActiveRelaySessions: 4,
				MaxRelaySessionsPerDay: 8,
			}),
		)
		account, network := mustAccountAndNetwork(t, ctx, svc)
		source, target := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)
		mustHeartbeatOnlineForP2PFallback(t, ctx, svc, source.ID, target.ID)
		first, err := svc.CreateRelaySession(ctx, CreateRelaySessionRequest{
			AccountID:      account.ID,
			NetworkID:      network.ID,
			SourceDeviceID: source.ID,
			TargetDeviceID: target.ID,
		})
		if err != nil {
			t.Fatalf("CreateRelaySession returned error: %v", err)
		}
		if _, err := svc.CloseRelaySession(ctx, CloseRelaySessionRequest{
			SessionID:     first.Session.ID,
			RelayBytesIn:  32,
			RelayBytesOut: 32,
		}); err != nil {
			t.Fatalf("CloseRelaySession returned error: %v", err)
		}

		_, err = P2PRelaySessionManager{Service: svc}.CreateRelaySession(ctx, p2p.RelaySessionRequest{
			AccountID:      account.ID,
			NetworkID:      network.ID,
			SourceDeviceID: source.ID,
			TargetDeviceID: target.ID,
		})
		if err == nil || !errors.Is(err, ErrQuotaExceeded) {
			t.Fatalf("quota error = %v, want ErrQuotaExceeded", err)
		}
	})
}

func TestP2PAutomaticFallbackDoesNotReportRelaySuccessWhenCloudHubRejectsSession(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		service func() (*Service, Account, Network, Device, Device)
		mutate  func(*testing.T, *Service, Account, Network, Device, Device)
		wantErr error
	}{
		{
			name: "frozen account",
			service: func() (*Service, Account, Network, Device, Device) {
				return mustP2PFallbackServiceDevices(t, ctx, NewService(NewMemoryStore()))
			},
			mutate: func(t *testing.T, svc *Service, account Account, _ Network, _ Device, _ Device) {
				t.Helper()
				if _, err := svc.FreezeAccount(ctx, AccountStatusChangeRequest{AccountID: account.ID, Reason: "risk review"}); err != nil {
					t.Fatalf("FreezeAccount returned error: %v", err)
				}
			},
			wantErr: ErrForbidden,
		},
		{
			name: "banned account",
			service: func() (*Service, Account, Network, Device, Device) {
				return mustP2PFallbackServiceDevices(t, ctx, NewService(NewMemoryStore()))
			},
			mutate: func(t *testing.T, svc *Service, account Account, _ Network, _ Device, _ Device) {
				t.Helper()
				if _, err := svc.BanAccount(ctx, AccountStatusChangeRequest{AccountID: account.ID, Reason: "abuse"}); err != nil {
					t.Fatalf("BanAccount returned error: %v", err)
				}
			},
			wantErr: ErrForbidden,
		},
		{
			name: "revoked target",
			service: func() (*Service, Account, Network, Device, Device) {
				return mustP2PFallbackServiceDevices(t, ctx, NewService(NewMemoryStore()))
			},
			mutate: func(t *testing.T, svc *Service, _ Account, _ Network, _ Device, target Device) {
				t.Helper()
				if _, err := svc.RevokeDevice(ctx, RevokeDeviceRequest{DeviceID: target.ID, Reason: "retired"}); err != nil {
					t.Fatalf("RevokeDevice returned error: %v", err)
				}
			},
			wantErr: ErrRevoked,
		},
		{
			name: "offline target",
			service: func() (*Service, Account, Network, Device, Device) {
				return mustP2PFallbackServiceDevices(t, ctx, NewService(NewMemoryStore()))
			},
			mutate: func(t *testing.T, svc *Service, _ Account, _ Network, _ Device, target Device) {
				t.Helper()
				if _, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{DeviceID: target.ID, Status: DeviceStatusOffline}); err != nil {
					t.Fatalf("HeartbeatDevice offline returned error: %v", err)
				}
			},
			wantErr: ErrForbidden,
		},
		{
			name: "relay quota exceeded",
			service: func() (*Service, Account, Network, Device, Device) {
				svc := NewService(NewMemoryStore(),
					WithAccountPolicies("tiny-p2p-auto", AccountPolicy{
						Name:                   "tiny-p2p-auto",
						RelayBytesQuota:        32,
						MaxActiveRelaySessions: 4,
						MaxRelaySessionsPerDay: 8,
					}),
				)
				return mustP2PFallbackServiceDevices(t, ctx, svc)
			},
			mutate: func(t *testing.T, svc *Service, account Account, network Network, source Device, target Device) {
				t.Helper()
				first, err := svc.CreateRelaySession(ctx, CreateRelaySessionRequest{
					AccountID:      account.ID,
					NetworkID:      network.ID,
					SourceDeviceID: source.ID,
					TargetDeviceID: target.ID,
				})
				if err != nil {
					t.Fatalf("CreateRelaySession returned error: %v", err)
				}
				if _, err := svc.CloseRelaySession(ctx, CloseRelaySessionRequest{
					SessionID:     first.Session.ID,
					RelayBytesIn:  32,
					RelayBytesOut: 32,
				}); err != nil {
					t.Fatalf("CloseRelaySession returned error: %v", err)
				}
			},
			wantErr: ErrQuotaExceeded,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, account, network, source, target := tc.service()
			negotiation := p2p.BuildNegotiation(p2p.NegotiationRequest{
				AccountID:          account.ID,
				NetworkID:          network.ID,
				SourceDeviceID:     source.ID,
				TargetDeviceID:     target.ID,
				SourceCandidates:   []p2p.Candidate{p2pFallbackTestCandidate(source.ID, network.ID, "127.0.0.1", 49130)},
				TargetCandidates:   []p2p.Candidate{p2pFallbackTestCandidate(target.ID, network.ID, "127.0.0.1", 49131)},
				AllowRelayFallback: true,
			})
			tc.mutate(t, svc, account, network, source, target)
			authorizer := &p2pFallbackAuthorizerForCloudHubTest{}

			result, err := p2p.AutoFallbackConnector{
				Connector:              p2p.Connector{Dialer: &p2p.FakeDialer{Err: errors.New("direct refused")}},
				RelaySessionCreator:    P2PRelaySessionManager{Service: svc, Endpoint: "127.0.0.1:18082"},
				RelaySessionAuthorizer: authorizer,
				RelaySessionCloser:     P2PRelaySessionManager{Service: svc},
			}.Connect(ctx, negotiation)

			if err == nil || !errors.Is(err, tc.wantErr) {
				t.Fatalf("Connect error = %v, want %v", err, tc.wantErr)
			}
			if result.State != p2p.PathStateFailed || result.FinalPath == p2p.PathTypeRelay || result.RelayAuthorized {
				t.Fatalf("result = %+v, must not report relay success", result)
			}
			if authorizer.calls != 0 {
				t.Fatalf("authorizer calls = %d, want 0 when Cloud Hub rejects session", authorizer.calls)
			}
		})
	}
}

func mustHeartbeatOnlineForP2PFallback(t *testing.T, ctx context.Context, svc *Service, deviceIDs ...string) {
	t.Helper()
	for _, deviceID := range deviceIDs {
		if _, err := svc.HeartbeatDevice(ctx, HeartbeatDeviceRequest{DeviceID: deviceID, Status: DeviceStatusOnline}); err != nil {
			t.Fatalf("HeartbeatDevice %s returned error: %v", deviceID, err)
		}
	}
}

type p2pFallbackAuthorizerForCloudHubTest struct {
	calls int
}

func (a *p2pFallbackAuthorizerForCloudHubTest) AuthorizeRelaySession(ctx context.Context, _ p2p.RelaySessionGrant) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.calls++
	return nil
}

func mustP2PFallbackServiceDevices(t *testing.T, ctx context.Context, svc *Service) (*Service, Account, Network, Device, Device) {
	t.Helper()
	account, network := mustAccountAndNetwork(t, ctx, svc)
	source, target := mustJoinTwoDevices(t, ctx, svc, account.ID, network.ID)
	mustHeartbeatOnlineForP2PFallback(t, ctx, svc, source.ID, target.ID)
	return svc, account, network, source, target
}

func p2pFallbackTestCandidate(deviceID, networkID, address string, port int) p2p.Candidate {
	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	return p2p.Candidate{
		DeviceID:   deviceID,
		NetworkID:  networkID,
		Address:    address,
		Port:       port,
		Protocol:   p2p.ProtocolTCP,
		Scope:      p2p.CandidateScopeLAN,
		Priority:   50,
		TTLSeconds: 60,
		SeenAt:     now,
		ExpiresAt:  now.Add(time.Minute),
	}
}
