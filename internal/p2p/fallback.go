package p2p

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrDirectConnectionFailed            = errors.New("direct connection failed")
	ErrRelayFallbackDisabled             = errors.New("relay fallback disabled")
	ErrRelaySessionCreatorUnavailable    = errors.New("relay session creator unavailable")
	ErrRelaySessionAuthorizerUnavailable = errors.New("relay session authorizer unavailable")
)

type RelaySessionRequest struct {
	AccountID      string        `json:"account_id"`
	NetworkID      string        `json:"network_id"`
	SourceDeviceID string        `json:"source_device_id"`
	TargetDeviceID string        `json:"target_device_id"`
	TTL            time.Duration `json:"ttl,omitempty"`
}

type RelaySessionGrant struct {
	ID              string    `json:"id"`
	AccountID       string    `json:"account_id"`
	NetworkID       string    `json:"network_id"`
	SourceDeviceID  string    `json:"source_device_id"`
	TargetDeviceID  string    `json:"target_device_id"`
	PathType        PathType  `json:"path_type"`
	Endpoint        string    `json:"endpoint,omitempty"`
	ExpiresAt       time.Time `json:"expires_at,omitempty"`
	SourceJoinToken string    `json:"-"`
	TargetJoinToken string    `json:"-"`
}

type CloseRelaySessionRequest struct {
	SessionID string `json:"session_id"`
	Error     string `json:"error,omitempty"`
}

type RelaySessionCreator interface {
	CreateRelaySession(context.Context, RelaySessionRequest) (RelaySessionGrant, error)
}

type RelaySessionAuthorizer interface {
	AuthorizeRelaySession(context.Context, RelaySessionGrant) error
}

type RelaySessionCloser interface {
	CloseRelaySession(context.Context, CloseRelaySessionRequest) error
}

type AutoFallbackConnector struct {
	Connector              Connector
	RelaySessionCreator    RelaySessionCreator
	RelaySessionAuthorizer RelaySessionAuthorizer
	RelaySessionCloser     RelaySessionCloser
	RelaySessionTTL        time.Duration
}

type AutoFallbackResult struct {
	NegotiationID       string           `json:"negotiation_id,omitempty"`
	AccountID           string           `json:"account_id"`
	NetworkID           string           `json:"network_id"`
	SourceDeviceID      string           `json:"source_device_id"`
	TargetDeviceID      string           `json:"target_device_id"`
	State               PathState        `json:"state"`
	FinalPath           PathType         `json:"final_path,omitempty"`
	DirectFailureReason string           `json:"direct_failure_reason,omitempty"`
	RelaySessionID      string           `json:"relay_session_id,omitempty"`
	RelayEndpoint       string           `json:"relay_endpoint,omitempty"`
	RelayAuthorized     bool             `json:"relay_authorized,omitempty"`
	DiagnosticMessage   string           `json:"diagnostic_message,omitempty"`
	Diagnostics         []string         `json:"diagnostics,omitempty"`
	DirectResult        ConnectionResult `json:"direct_result"`
}

func (c AutoFallbackConnector) Connect(ctx context.Context, negotiation Negotiation) (AutoFallbackResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	directResult := c.Connector.Connect(ctx, negotiation)
	result := newAutoFallbackResult(directResult)

	if directResult.State != PathStateFallbackRelay {
		if directResult.State == PathStateFailed {
			result.State = PathStateFailed
			result.FinalPath = ""
			if !directResult.RelayFallback.CreateRelaySession {
				result.setDiagnostic("direct failed and Relay fallback is disabled")
				return result, fmt.Errorf("%w: %s", ErrRelayFallbackDisabled, nonEmptyReason(directResult.FallbackReason))
			}
			result.setDiagnostic("direct failed before Relay fallback could start")
			return result, fmt.Errorf("%w: %s", ErrDirectConnectionFailed, nonEmptyReason(directResult.FallbackReason))
		}
		result.setDiagnostic("direct path connected")
		return result, nil
	}

	result.FinalPath = ""
	result.DirectFailureReason = nonEmptyReason(directResult.FallbackReason)
	result.Diagnostics = append(result.Diagnostics, "direct failed: "+result.DirectFailureReason)
	if !directResult.RelayFallback.CreateRelaySession {
		result.State = PathStateFailed
		result.setDiagnostic("direct failed and Relay fallback is disabled")
		return result, fmt.Errorf("%w: %s", ErrRelayFallbackDisabled, result.DirectFailureReason)
	}
	if c.RelaySessionCreator == nil {
		result.State = PathStateFailed
		result.setDiagnostic("direct failed but Relay session creator is unavailable")
		return result, ErrRelaySessionCreatorUnavailable
	}

	grant, err := c.RelaySessionCreator.CreateRelaySession(ctx, relayRequestFromDirectResult(directResult, c.RelaySessionTTL))
	if err != nil {
		result.State = PathStateFailed
		result.setDiagnostic("direct failed but Relay fallback session could not be created")
		return result, fmt.Errorf("create relay fallback session: %w", err)
	}
	result.RelaySessionID = strings.TrimSpace(grant.ID)
	result.RelayEndpoint = strings.TrimSpace(grant.Endpoint)
	result.Diagnostics = append(result.Diagnostics, "Relay fallback session created")

	if c.RelaySessionAuthorizer == nil {
		c.closeRelaySession(ctx, grant, "relay authorization failed: runtime unavailable")
		result.State = PathStateFailed
		result.setDiagnostic("Relay fallback session created but runtime authorization is unavailable")
		return result, ErrRelaySessionAuthorizerUnavailable
	}
	if err := c.RelaySessionAuthorizer.AuthorizeRelaySession(ctx, grant); err != nil {
		c.closeRelaySession(ctx, grant, "relay authorization failed")
		result.State = PathStateFailed
		result.setDiagnostic("Relay fallback session created but runtime authorization failed")
		return result, fmt.Errorf("authorize relay fallback session: %w", err)
	}

	result.State = PathStateFallbackRelay
	result.FinalPath = PathTypeRelay
	result.RelayAuthorized = true
	result.Diagnostics = append(result.Diagnostics, "Relay runtime authorized; current path is relay")
	result.setDiagnostic("direct failed; Relay fallback session created and authorized; current path is relay")
	return result, nil
}

func newAutoFallbackResult(directResult ConnectionResult) AutoFallbackResult {
	finalPath := directResult.PathType
	if directResult.State == PathStateFallbackRelay || directResult.State == PathStateFailed {
		finalPath = ""
	}
	diagnostics := []string{"attempted direct path"}
	if len(directResult.Attempts) == 0 && directResult.FallbackReason == "no_direct_candidates" {
		diagnostics[0] = "attempted direct path; no direct candidate pairs were available"
	}
	return AutoFallbackResult{
		NegotiationID:       directResult.NegotiationID,
		AccountID:           directResult.AccountID,
		NetworkID:           directResult.NetworkID,
		SourceDeviceID:      directResult.SourceDeviceID,
		TargetDeviceID:      directResult.TargetDeviceID,
		State:               directResult.State,
		FinalPath:           finalPath,
		DirectFailureReason: directResult.FallbackReason,
		DiagnosticMessage:   directResult.DiagnosticMessage,
		Diagnostics:         diagnostics,
		DirectResult:        directResult,
	}
}

func relayRequestFromDirectResult(result ConnectionResult, ttl time.Duration) RelaySessionRequest {
	fallback := result.RelayFallback
	req := RelaySessionRequest{
		AccountID:      firstNonEmpty(fallback.AccountID, result.AccountID),
		NetworkID:      firstNonEmpty(fallback.NetworkID, result.NetworkID),
		SourceDeviceID: firstNonEmpty(fallback.SourceDeviceID, result.SourceDeviceID),
		TargetDeviceID: firstNonEmpty(fallback.TargetDeviceID, result.TargetDeviceID),
		TTL:            ttl,
	}
	return req
}

func (c AutoFallbackConnector) closeRelaySession(ctx context.Context, grant RelaySessionGrant, reason string) {
	if c.RelaySessionCloser == nil || strings.TrimSpace(grant.ID) == "" {
		return
	}
	_ = c.RelaySessionCloser.CloseRelaySession(ctx, CloseRelaySessionRequest{
		SessionID: grant.ID,
		Error:     reason,
	})
}

func (r *AutoFallbackResult) setDiagnostic(message string) {
	if strings.TrimSpace(message) != "" {
		r.DiagnosticMessage = message
	}
}

func nonEmptyReason(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return "direct connection failed"
	}
	return strings.TrimSpace(reason)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
