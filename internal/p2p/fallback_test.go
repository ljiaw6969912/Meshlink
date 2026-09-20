package p2p

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAutoFallbackConnectorCreatesAndAuthorizesRelayAfterDirectFailure(t *testing.T) {
	negotiation := BuildNegotiation(NegotiationRequest{
		AccountID:          "acct_owner",
		NetworkID:          "net_home",
		SourceDeviceID:     "dev_source",
		TargetDeviceID:     "dev_target",
		SourceCandidates:   []Candidate{lanCandidate("dev_source", "10.0.0.11", 9100, 50)},
		TargetCandidates:   []Candidate{lanCandidate("dev_target", "10.0.0.12", 9100, 60)},
		AllowRelayFallback: true,
	})
	sourceToken := randomAutoFallbackTestToken(t)
	targetToken := randomAutoFallbackTestToken(t)
	creator := &recordingRelayCreator{
		grant: RelaySessionGrant{
			ID:              "rsess_automatic",
			AccountID:       "acct_owner",
			NetworkID:       "net_home",
			SourceDeviceID:  "dev_source",
			TargetDeviceID:  "dev_target",
			PathType:        PathTypeRelay,
			Endpoint:        "127.0.0.1:18082",
			ExpiresAt:       time.Date(2026, 7, 9, 10, 5, 0, 0, time.UTC),
			SourceJoinToken: sourceToken,
			TargetJoinToken: targetToken,
		},
	}
	authorizer := &recordingRelayAuthorizer{}

	startedAt := time.Now()
	result, err := AutoFallbackConnector{
		Connector:              Connector{Dialer: &FakeDialer{Err: errors.New("connection refused")}},
		RelaySessionCreator:    creator,
		RelaySessionAuthorizer: authorizer,
	}.Connect(context.Background(), negotiation)

	if err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed >= 15*time.Second {
		t.Fatalf("automatic fallback took %s, want under 15s gate", elapsed)
	}
	if result.State != PathStateFallbackRelay || result.FinalPath != PathTypeRelay {
		t.Fatalf("result = %+v, want authorized Relay fallback", result)
	}
	if result.RelaySessionID != "rsess_automatic" || result.RelayEndpoint != "127.0.0.1:18082" || !result.RelayAuthorized {
		t.Fatalf("relay fields = %+v, want session id, endpoint, and authorized=true", result)
	}
	if !strings.Contains(result.DirectFailureReason, "connection refused") {
		t.Fatalf("direct failure reason = %q, want dial failure", result.DirectFailureReason)
	}
	for _, want := range []string{"attempted direct", "direct failed", "Relay fallback session created", "current path is relay"} {
		if !hasAutoFallbackDiagnostic(result.Diagnostics, want) {
			t.Fatalf("diagnostics = %+v, missing %q", result.Diagnostics, want)
		}
	}
	if creator.calls != 1 {
		t.Fatalf("relay creator calls = %d, want 1", creator.calls)
	}
	if creator.last.AccountID != "acct_owner" || creator.last.NetworkID != "net_home" ||
		creator.last.SourceDeviceID != "dev_source" || creator.last.TargetDeviceID != "dev_target" {
		t.Fatalf("relay create request = %+v, want negotiation identities", creator.last)
	}
	if len(authorizer.grants) != 1 || authorizer.grants[0].ID != "rsess_automatic" {
		t.Fatalf("authorized grants = %+v, want created grant", authorizer.grants)
	}
	assertAutoFallbackJSONDoesNotContain(t, result, "join_token", sourceToken, targetToken, "private_key", "token_hash")
}

func TestAutoFallbackConnectorDoesNotCreateRelayWhenDirectSucceeds(t *testing.T) {
	negotiation := BuildNegotiation(NegotiationRequest{
		AccountID:          "acct_owner",
		NetworkID:          "net_home",
		SourceDeviceID:     "dev_source",
		TargetDeviceID:     "dev_target",
		SourceCandidates:   []Candidate{lanCandidate("dev_source", "10.0.0.11", 9100, 50)},
		TargetCandidates:   []Candidate{lanCandidate("dev_target", "10.0.0.12", 9100, 60)},
		AllowRelayFallback: true,
	})
	creator := &recordingRelayCreator{}

	result, err := AutoFallbackConnector{
		Connector:              Connector{Dialer: &FakeDialer{}},
		RelaySessionCreator:    creator,
		RelaySessionAuthorizer: &recordingRelayAuthorizer{},
	}.Connect(context.Background(), negotiation)

	if err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}
	if result.State != PathStateLANDirectConnected || result.FinalPath != PathTypeLANDirect {
		t.Fatalf("result = %+v, want direct connected", result)
	}
	if creator.calls != 0 || result.RelaySessionID != "" || result.RelayAuthorized {
		t.Fatalf("result = %+v creator calls=%d, want no Relay session on direct success", result, creator.calls)
	}
}

func TestAutoFallbackConnectorFailsClosedWhenFallbackDisabledOrRuntimeUnavailable(t *testing.T) {
	t.Run("fallback disabled", func(t *testing.T) {
		negotiation := BuildNegotiation(NegotiationRequest{
			AccountID:          "acct_owner",
			NetworkID:          "net_home",
			SourceDeviceID:     "dev_source",
			TargetDeviceID:     "dev_target",
			SourceCandidates:   []Candidate{lanCandidate("dev_source", "10.0.0.11", 9100, 50)},
			TargetCandidates:   []Candidate{lanCandidate("dev_target", "10.0.0.12", 9100, 60)},
			AllowRelayFallback: false,
		})
		creator := &recordingRelayCreator{}

		result, err := AutoFallbackConnector{
			Connector:           Connector{Dialer: &FakeDialer{Err: errors.New("connection refused")}},
			RelaySessionCreator: creator,
		}.Connect(context.Background(), negotiation)

		if err == nil || !errors.Is(err, ErrRelayFallbackDisabled) {
			t.Fatalf("Connect error = %v, want ErrRelayFallbackDisabled", err)
		}
		if creator.calls != 0 || result.FinalPath == PathTypeRelay || result.RelayAuthorized {
			t.Fatalf("result = %+v creator calls=%d, must not create or report Relay", result, creator.calls)
		}
	})

	t.Run("runtime unavailable", func(t *testing.T) {
		negotiation := BuildNegotiation(NegotiationRequest{
			AccountID:          "acct_owner",
			NetworkID:          "net_home",
			SourceDeviceID:     "dev_source",
			TargetDeviceID:     "dev_target",
			SourceCandidates:   []Candidate{lanCandidate("dev_source", "10.0.0.11", 9100, 50)},
			TargetCandidates:   []Candidate{lanCandidate("dev_target", "10.0.0.12", 9100, 60)},
			AllowRelayFallback: true,
		})
		creator := &recordingRelayCreator{
			grant: RelaySessionGrant{
				ID:             "rsess_runtime_down",
				AccountID:      "acct_owner",
				NetworkID:      "net_home",
				SourceDeviceID: "dev_source",
				TargetDeviceID: "dev_target",
				PathType:       PathTypeRelay,
			},
		}
		closer := &recordingRelayCloser{}

		result, err := AutoFallbackConnector{
			Connector:           Connector{Dialer: &FakeDialer{Err: errors.New("connection refused")}},
			RelaySessionCreator: creator,
			RelaySessionCloser:  closer,
		}.Connect(context.Background(), negotiation)

		if err == nil || !errors.Is(err, ErrRelaySessionAuthorizerUnavailable) {
			t.Fatalf("Connect error = %v, want ErrRelaySessionAuthorizerUnavailable", err)
		}
		if result.State != PathStateFailed || result.FinalPath == PathTypeRelay || result.RelayAuthorized {
			t.Fatalf("result = %+v, must not report Relay connected", result)
		}
		if creator.calls != 1 || closer.calls != 1 || closer.last.SessionID != "rsess_runtime_down" {
			t.Fatalf("creator calls=%d closer=%+v, want created session closed after auth failure", creator.calls, closer)
		}
	})
}

type recordingRelayCreator struct {
	calls int
	last  RelaySessionRequest
	grant RelaySessionGrant
	err   error
}

func (c *recordingRelayCreator) CreateRelaySession(ctx context.Context, req RelaySessionRequest) (RelaySessionGrant, error) {
	if err := ctx.Err(); err != nil {
		return RelaySessionGrant{}, err
	}
	c.calls++
	c.last = req
	if c.err != nil {
		return RelaySessionGrant{}, c.err
	}
	return c.grant, nil
}

type recordingRelayAuthorizer struct {
	grants []RelaySessionGrant
	err    error
}

func (a *recordingRelayAuthorizer) AuthorizeRelaySession(ctx context.Context, grant RelaySessionGrant) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.err != nil {
		return a.err
	}
	a.grants = append(a.grants, grant)
	return nil
}

type recordingRelayCloser struct {
	calls int
	last  CloseRelaySessionRequest
	err   error
}

func (c *recordingRelayCloser) CloseRelaySession(ctx context.Context, req CloseRelaySessionRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.calls++
	c.last = req
	return c.err
}

func hasAutoFallbackDiagnostic(diagnostics []string, want string) bool {
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic, want) {
			return true
		}
	}
	return false
}

func assertAutoFallbackJSONDoesNotContain(t *testing.T, v any, forbidden ...string) {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal value: %v", err)
	}
	lower := strings.ToLower(string(encoded))
	for _, value := range forbidden {
		if value != "" && strings.Contains(lower, strings.ToLower(value)) {
			t.Fatal("JSON leaks a forbidden sensitive value")
		}
	}
}

func randomAutoFallbackTestToken(t *testing.T) string {
	t.Helper()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatalf("generate test token: %v", err)
	}
	return hex.EncodeToString(raw[:])
}
