package relay

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestBrokerRelaysBidirectionalBytesAndReportsClose(t *testing.T) {
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	reports := make(chan SessionReport, 1)
	broker := NewBroker(
		WithNow(func() time.Time { return now }),
		WithClosedCallback(func(report SessionReport) {
			reports <- report
		}),
	)
	sourceToken := "source-proof"
	targetToken := "target-proof"
	grant := AuthorizedSession{
		SessionID:           "relay_sess_ok",
		AccountID:           "acct_1",
		NetworkID:           "net_1",
		SourceDeviceID:      "dev_source",
		TargetDeviceID:      "dev_target",
		SourceJoinTokenHash: HashJoinToken("relay_sess_ok", RoleSource, sourceToken),
		TargetJoinTokenHash: HashJoinToken("relay_sess_ok", RoleTarget, targetToken),
		ExpiresAt:           now.Add(time.Minute),
	}
	if err := broker.Authorize(grant); err != nil {
		t.Fatalf("Authorize returned error: %v", err)
	}

	sourceConn, targetConn := joinPair(t, broker, sourceToken, targetToken)
	writeAndRead(t, sourceConn, targetConn, []byte("ping"))
	writeAndRead(t, targetConn, sourceConn, []byte("pong!"))
	if err := sourceConn.Close(); err != nil {
		t.Fatalf("source Close returned error: %v", err)
	}
	if err := targetConn.Close(); err != nil {
		t.Fatalf("target Close returned error: %v", err)
	}

	select {
	case report := <-reports:
		if report.SessionID != grant.SessionID || report.AccountID != grant.AccountID || report.NetworkID != grant.NetworkID {
			t.Fatalf("report = %+v, want authorized session metadata", report)
		}
		if report.BytesSourceToTarget != 4 || report.BytesTargetToSource != 5 {
			t.Fatalf("report byte counts = %+v, want 4 source->target and 5 target->source", report)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for close report")
	}
}

func TestBrokerSharesAccountByteBudgetAcrossAuthorizedSessions(t *testing.T) {
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	reports := make(chan SessionReport, 2)
	broker := NewBroker(
		WithNow(func() time.Time { return now }),
		WithClosedCallback(func(report SessionReport) {
			reports <- report
		}),
	)
	sourceToken := "source-proof"
	targetToken := "target-proof"
	for _, sessionID := range []string{"relay_sess_budget_a", "relay_sess_budget_b"} {
		grant := AuthorizedSession{
			SessionID:           sessionID,
			AccountID:           "acct_shared_budget",
			NetworkID:           "net_1",
			SourceDeviceID:      "dev_source_" + sessionID,
			TargetDeviceID:      "dev_target_" + sessionID,
			SourceJoinTokenHash: HashJoinToken(sessionID, RoleSource, sourceToken),
			TargetJoinTokenHash: HashJoinToken(sessionID, RoleTarget, targetToken),
			ExpiresAt:           now.Add(time.Minute),
			ByteBudgetLimited:   true,
			ByteBudgetRemaining: 5,
		}
		if err := broker.Authorize(grant); err != nil {
			t.Fatalf("Authorize %s returned error: %v", sessionID, err)
		}
	}

	sourceA, targetA := joinPairForSession(t, broker, "relay_sess_budget_a", "dev_source_relay_sess_budget_a", "dev_target_relay_sess_budget_a", sourceToken, targetToken)
	sourceB, targetB := joinPairForSession(t, broker, "relay_sess_budget_b", "dev_source_relay_sess_budget_b", "dev_target_relay_sess_budget_b", sourceToken, targetToken)
	writeAndRead(t, sourceA, targetA, []byte("abc"))

	writeDone := make(chan error, 1)
	go func() {
		n, err := sourceB.Write([]byte("wxyz"))
		if n != 2 {
			writeDone <- errors.New("budgeted write transferred unexpected byte count")
			return
		}
		writeDone <- err
	}()
	got := make([]byte, 2)
	if _, err := io.ReadFull(targetB, got); err != nil {
		t.Fatalf("ReadFull budgeted bytes returned error: %v", err)
	}
	if string(got) != "wx" {
		t.Fatalf("budgeted payload = %q, want wx", got)
	}
	if err := <-writeDone; !errors.Is(err, ErrByteBudget) {
		t.Fatalf("budgeted Write error = %v, want ErrByteBudget", err)
	}
	_ = sourceA.Close()
	_ = targetA.Close()
	_ = sourceB.Close()
	_ = targetB.Close()

	var total int64
	for i := 0; i < 2; i++ {
		select {
		case report := <-reports:
			total += report.BytesSourceToTarget + report.BytesTargetToSource
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for shared budget reports")
		}
	}
	if total != 5 {
		t.Fatalf("total relayed bytes = %d, want shared account budget 5", total)
	}
}

func TestBrokerRejectsInvalidDuplicateExpiredAndRevokedJoins(t *testing.T) {
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	broker := NewBroker(
		WithNow(func() time.Time { return now }),
		WithSingleSideTimeout(25*time.Millisecond),
	)
	sourceToken := "source-proof"
	targetToken := "target-proof"
	grant := AuthorizedSession{
		SessionID:           "relay_sess_guarded",
		AccountID:           "acct_1",
		NetworkID:           "net_1",
		SourceDeviceID:      "dev_source",
		TargetDeviceID:      "dev_target",
		SourceJoinTokenHash: HashJoinToken("relay_sess_guarded", RoleSource, sourceToken),
		TargetJoinTokenHash: HashJoinToken("relay_sess_guarded", RoleTarget, targetToken),
		ExpiresAt:           now.Add(time.Minute),
	}
	if err := broker.Authorize(grant); err != nil {
		t.Fatalf("Authorize returned error: %v", err)
	}

	if _, err := broker.Join(context.Background(), JoinRequest{
		SessionID: "relay_sess_guarded",
		Role:      RoleSource,
		DeviceID:  "dev_source",
		Token:     "wrong-proof",
	}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Join with wrong token error = %v, want ErrUnauthorized", err)
	}

	firstJoinCtx, cancelFirstJoin := context.WithCancel(context.Background())
	firstJoinDone := make(chan error, 1)
	go func() {
		conn, err := broker.Join(firstJoinCtx, JoinRequest{
			SessionID: "relay_sess_guarded",
			Role:      RoleSource,
			DeviceID:  "dev_source",
			Token:     sourceToken,
		})
		if conn != nil {
			_ = conn.Close()
		}
		firstJoinDone <- err
	}()
	time.Sleep(10 * time.Millisecond)
	if _, err := broker.Join(context.Background(), JoinRequest{
		SessionID: "relay_sess_guarded",
		Role:      RoleSource,
		DeviceID:  "dev_source",
		Token:     sourceToken,
	}); !errors.Is(err, ErrAlreadyJoined) {
		t.Fatalf("duplicate Join error = %v, want ErrAlreadyJoined", err)
	}
	cancelFirstJoin()
	<-firstJoinDone

	if err := broker.Authorize(AuthorizedSession{
		SessionID:           "relay_sess_expired",
		SourceDeviceID:      "dev_source",
		TargetDeviceID:      "dev_target",
		SourceJoinTokenHash: HashJoinToken("relay_sess_expired", RoleSource, sourceToken),
		TargetJoinTokenHash: HashJoinToken("relay_sess_expired", RoleTarget, targetToken),
		ExpiresAt:           now.Add(-time.Second),
	}); err != nil {
		t.Fatalf("Authorize expired returned error: %v", err)
	}
	if _, err := broker.Join(context.Background(), JoinRequest{
		SessionID: "relay_sess_expired",
		Role:      RoleSource,
		DeviceID:  "dev_source",
		Token:     sourceToken,
	}); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired Join error = %v, want ErrExpired", err)
	}

	if err := broker.Authorize(AuthorizedSession{
		SessionID:           "relay_sess_revoked",
		SourceDeviceID:      "dev_source",
		TargetDeviceID:      "dev_target",
		SourceJoinTokenHash: HashJoinToken("relay_sess_revoked", RoleSource, sourceToken),
		TargetJoinTokenHash: HashJoinToken("relay_sess_revoked", RoleTarget, targetToken),
		ExpiresAt:           now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("Authorize revoked returned error: %v", err)
	}
	if err := broker.Revoke("relay_sess_revoked"); err != nil {
		t.Fatalf("Revoke returned error: %v", err)
	}
	if _, err := broker.Join(context.Background(), JoinRequest{
		SessionID: "relay_sess_revoked",
		Role:      RoleTarget,
		DeviceID:  "dev_target",
		Token:     targetToken,
	}); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked Join error = %v, want ErrRevoked", err)
	}

	if err := broker.Authorize(AuthorizedSession{
		SessionID:           "relay_sess_timeout",
		SourceDeviceID:      "dev_source",
		TargetDeviceID:      "dev_target",
		SourceJoinTokenHash: HashJoinToken("relay_sess_timeout", RoleSource, sourceToken),
		TargetJoinTokenHash: HashJoinToken("relay_sess_timeout", RoleTarget, targetToken),
		ExpiresAt:           now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("Authorize timeout returned error: %v", err)
	}
	if _, err := broker.Join(context.Background(), JoinRequest{
		SessionID: "relay_sess_timeout",
		Role:      RoleSource,
		DeviceID:  "dev_source",
		Token:     sourceToken,
	}); !errors.Is(err, ErrJoinTimeout) {
		t.Fatalf("single-side Join error = %v, want ErrJoinTimeout", err)
	}
}

func joinPair(t *testing.T, broker *Broker, sourceToken, targetToken string) (net.Conn, net.Conn) {
	t.Helper()
	return joinPairForSession(t, broker, "relay_sess_ok", "dev_source", "dev_target", sourceToken, targetToken)
}

func joinPairForSession(t *testing.T, broker *Broker, sessionID, sourceDeviceID, targetDeviceID, sourceToken, targetToken string) (net.Conn, net.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	type result struct {
		conn net.Conn
		err  error
	}
	sourceCh := make(chan result, 1)
	targetCh := make(chan result, 1)
	go func() {
		conn, err := broker.Join(ctx, JoinRequest{
			SessionID: sessionID,
			Role:      RoleSource,
			DeviceID:  sourceDeviceID,
			Token:     sourceToken,
		})
		sourceCh <- result{conn: conn, err: err}
	}()
	go func() {
		conn, err := broker.Join(ctx, JoinRequest{
			SessionID: sessionID,
			Role:      RoleTarget,
			DeviceID:  targetDeviceID,
			Token:     targetToken,
		})
		targetCh <- result{conn: conn, err: err}
	}()
	source := <-sourceCh
	target := <-targetCh
	if source.err != nil {
		t.Fatalf("source Join returned error: %v", source.err)
	}
	if target.err != nil {
		t.Fatalf("target Join returned error: %v", target.err)
	}
	return source.conn, target.conn
}

func writeAndRead(t *testing.T, writer net.Conn, reader net.Conn, payload []byte) {
	t.Helper()
	errCh := make(chan error, 1)
	go func() {
		n, err := writer.Write(payload)
		if err != nil {
			errCh <- err
			return
		}
		if n != len(payload) {
			errCh <- io.ErrShortWrite
			return
		}
		errCh <- nil
	}()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(reader, got); err != nil {
		t.Fatalf("ReadFull returned error: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("relay payload = %q, want %q", got, payload)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
}
