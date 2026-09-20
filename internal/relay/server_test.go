package relay

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"meshlink/internal/cloudhub"
	"meshlink/internal/p2p"
)

func TestServerRelaysTCPHandshakeAndWritesCloudHubLifecycle(t *testing.T) {
	ctx := context.Background()
	svc := cloudhub.NewService(cloudhub.NewMemoryStore())
	runtime := NewServer(svc, WithEndpoint("local-relay"), WithJoinTimeout(100*time.Millisecond))
	addr := serveRelayForTest(t, runtime)
	result, source, target := createAuthorizedRelaySession(t, ctx, svc, runtime, 2*time.Minute)

	sourceConn, sourceReader := openRelayConn(t, addr, map[string]string{
		"session_id": result.Session.ID,
		"role":       string(RoleSource),
		"device_id":  source.ID,
		"token":      result.SourceJoinToken,
	})
	defer sourceConn.Close()
	targetConn, targetReader := openRelayConn(t, addr, map[string]string{
		"session_id": result.Session.ID,
		"role":       string(RoleTarget),
		"device_id":  target.ID,
		"token":      result.TargetJoinToken,
	})
	defer targetConn.Close()

	readRelayAck(t, sourceReader, true)
	readRelayAck(t, targetReader, true)
	active, err := svc.GetRelaySession(ctx, result.Session.ID)
	if err != nil {
		t.Fatalf("GetRelaySession active returned error: %v", err)
	}
	if active.Status != cloudhub.RelaySessionActive {
		t.Fatalf("relay session status = %q, want active after both endpoints join", active.Status)
	}

	if _, err := sourceConn.Write([]byte("ping")); err != nil {
		t.Fatalf("source Write returned error: %v", err)
	}
	assertRelayPayload(t, targetReader, "ping")
	if _, err := targetConn.Write([]byte("pong!")); err != nil {
		t.Fatalf("target Write returned error: %v", err)
	}
	assertRelayPayload(t, sourceReader, "pong!")

	if err := sourceConn.Close(); err != nil {
		t.Fatalf("source Close returned error: %v", err)
	}
	if err := targetConn.Close(); err != nil {
		t.Fatalf("target Close returned error: %v", err)
	}

	session := waitForRelaySessionClosed(t, ctx, svc, result.Session.ID)
	if session.RelayBytesIn != 4 || session.RelayBytesOut != 5 {
		t.Fatalf("relay bytes = in:%d out:%d, want 4/5", session.RelayBytesIn, session.RelayBytesOut)
	}
	logs, err := svc.ListRelaySessionConnectionLogs(ctx, result.Session.ID)
	if err != nil {
		t.Fatalf("ListRelaySessionConnectionLogs returned error: %v", err)
	}
	if len(logs) != 1 || logs[0].SessionID != result.Session.ID || logs[0].RelayBytesIn != 4 || logs[0].RelayBytesOut != 5 {
		t.Fatalf("connection logs = %+v, want one relay log with session bytes", logs)
	}
	if logs[0].PathType != string(p2p.PathTypeRelay) || logs[0].PathState != string(p2p.PathStateClosed) || logs[0].QualityScore == 0 {
		t.Fatalf("connection log quality = %+v, want relay path, closed state, and computed quality score", logs[0])
	}
	usage, err := svc.ListRelaySessionUsage(ctx, result.Session.ID)
	if err != nil {
		t.Fatalf("ListRelaySessionUsage returned error: %v", err)
	}
	if len(usage) != 2 {
		t.Fatalf("relay usage rows = %+v, want source and target usage", usage)
	}
}

func TestServerStopsRelayDataPathAtSessionByteBudget(t *testing.T) {
	ctx := context.Background()
	svc := cloudhub.NewService(cloudhub.NewMemoryStore(),
		cloudhub.WithAccountPolicies("runtime-budget", cloudhub.AccountPolicy{
			Name:                   "runtime-budget",
			RelayBytesQuota:        6,
			MaxActiveRelaySessions: 4,
			MaxRelaySessionsPerDay: 8,
		}),
	)
	runtime := NewServer(svc, WithEndpoint("local-relay"), WithJoinTimeout(100*time.Millisecond))
	addr := serveRelayForTest(t, runtime)
	result, source, target := createAuthorizedRelaySession(t, ctx, svc, runtime, 2*time.Minute)
	if !result.RelayByteBudget.Limited || result.RelayByteBudget.RemainingBytes != 6 {
		t.Fatalf("relay budget = %+v, want six byte data-path budget", result.RelayByteBudget)
	}

	sourceConn, sourceReader := openRelayConn(t, addr, map[string]string{
		"session_id": result.Session.ID,
		"role":       string(RoleSource),
		"device_id":  source.ID,
		"token":      result.SourceJoinToken,
	})
	defer sourceConn.Close()
	targetConn, targetReader := openRelayConn(t, addr, map[string]string{
		"session_id": result.Session.ID,
		"role":       string(RoleTarget),
		"device_id":  target.ID,
		"token":      result.TargetJoinToken,
	})
	defer targetConn.Close()

	readRelayAck(t, sourceReader, true)
	readRelayAck(t, targetReader, true)
	if _, err := sourceConn.Write([]byte("abcd")); err != nil {
		t.Fatalf("source Write returned error: %v", err)
	}
	assertRelayPayload(t, targetReader, "abcd")
	if _, err := targetConn.Write([]byte("xyz")); err != nil {
		t.Fatalf("target Write returned error before peer observes quota close: %v", err)
	}
	assertRelayPayload(t, sourceReader, "xy")
	assertRelayReadFails(t, sourceReader)
	assertRelayReadFails(t, targetReader)

	session := waitForRelaySessionClosed(t, ctx, svc, result.Session.ID)
	if session.RelayBytesIn != 4 || session.RelayBytesOut != 2 {
		t.Fatalf("relay bytes = in:%d out:%d, want clamped 4/2 at hard budget", session.RelayBytesIn, session.RelayBytesOut)
	}
	status, err := svc.GetAccountPolicyStatus(ctx, result.Session.AccountID)
	if err != nil {
		t.Fatalf("GetAccountPolicyStatus returned error: %v", err)
	}
	if status.RelayBytesUsed != 6 || status.RelayBytesRemaining != 0 {
		t.Fatalf("policy status = %+v, want exact budget usage 6/0", status)
	}
}

func TestServerLoadsRelayByteBudgetWhenGrantDoesNotCarryIt(t *testing.T) {
	ctx := context.Background()
	svc := cloudhub.NewService(cloudhub.NewMemoryStore(),
		cloudhub.WithAccountPolicies("runtime-budget-from-hub", cloudhub.AccountPolicy{
			Name:                   "runtime-budget-from-hub",
			RelayBytesQuota:        5,
			MaxActiveRelaySessions: 4,
			MaxRelaySessionsPerDay: 8,
		}),
	)
	runtime := NewServer(svc, WithEndpoint("local-relay"), WithJoinTimeout(100*time.Millisecond))
	addr := serveRelayForTest(t, runtime)
	result, source, target := createRelaySessionForRuntimeTest(t, ctx, svc, 2*time.Minute)
	result.RelayByteBudget = cloudhub.RelayByteBudget{}
	if err := runtime.AuthorizeRelaySession(result); err != nil {
		t.Fatalf("AuthorizeRelaySession returned error: %v", err)
	}

	sourceConn, sourceReader := openRelayConn(t, addr, map[string]string{
		"session_id": result.Session.ID,
		"role":       string(RoleSource),
		"device_id":  source.ID,
		"token":      result.SourceJoinToken,
	})
	defer sourceConn.Close()
	targetConn, targetReader := openRelayConn(t, addr, map[string]string{
		"session_id": result.Session.ID,
		"role":       string(RoleTarget),
		"device_id":  target.ID,
		"token":      result.TargetJoinToken,
	})
	defer targetConn.Close()

	readRelayAck(t, sourceReader, true)
	readRelayAck(t, targetReader, true)
	if _, err := sourceConn.Write([]byte("hello")); err != nil {
		t.Fatalf("source Write returned error: %v", err)
	}
	assertRelayPayload(t, targetReader, "hello")
	writeDone := make(chan error, 1)
	go func() {
		_, err := targetConn.Write([]byte("!"))
		writeDone <- err
	}()
	got := make([]byte, 1)
	_, readErr := io.ReadFull(sourceReader, got)
	writeErr := <-writeDone
	if readErr == nil && string(got) == "!" {
		t.Fatal("source received bytes after hub budget was exhausted")
	}
	_ = writeErr

	session := waitForRelaySessionClosed(t, ctx, svc, result.Session.ID)
	if session.RelayBytesIn != 5 || session.RelayBytesOut != 0 {
		t.Fatalf("relay bytes = in:%d out:%d, want hub-loaded budget 5/0", session.RelayBytesIn, session.RelayBytesOut)
	}
}

func TestServerRevokesActiveTCPRelayWhenAccountIsFrozen(t *testing.T) {
	ctx := context.Background()
	svc := cloudhub.NewService(cloudhub.NewMemoryStore())
	runtime := NewServer(svc, WithEndpoint("local-relay"), WithJoinTimeout(100*time.Millisecond))
	svc.SetRelaySessionRevokedHook(func(_ context.Context, session cloudhub.RelaySession) error {
		if err := runtime.RevokeRelaySession(session.ID); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		return nil
	})
	addr := serveRelayForTest(t, runtime)
	result, source, target := createAuthorizedRelaySession(t, ctx, svc, runtime, 2*time.Minute)

	sourceConn, sourceReader := openRelayConn(t, addr, map[string]string{
		"session_id": result.Session.ID,
		"role":       string(RoleSource),
		"device_id":  source.ID,
		"token":      result.SourceJoinToken,
	})
	defer sourceConn.Close()
	targetConn, targetReader := openRelayConn(t, addr, map[string]string{
		"session_id": result.Session.ID,
		"role":       string(RoleTarget),
		"device_id":  target.ID,
		"token":      result.TargetJoinToken,
	})
	defer targetConn.Close()

	readRelayAck(t, sourceReader, true)
	readRelayAck(t, targetReader, true)
	if _, err := sourceConn.Write([]byte("ping")); err != nil {
		t.Fatalf("source Write returned error: %v", err)
	}
	assertRelayPayload(t, targetReader, "ping")

	if _, err := svc.FreezeAccount(ctx, cloudhub.AccountStatusChangeRequest{
		AccountID: result.Session.AccountID,
		Reason:    "risk review",
	}); err != nil {
		t.Fatalf("FreezeAccount returned error: %v", err)
	}

	assertRelayReadFails(t, sourceReader)
	assertRelayReadFails(t, targetReader)
	session := waitForRelaySessionClosed(t, ctx, svc, result.Session.ID)
	if !strings.Contains(session.Error, "account frozen") {
		t.Fatalf("closed session error = %q, want account frozen", session.Error)
	}
	if session.RelayBytesIn != 4 || session.RelayBytesOut != 0 {
		t.Fatalf("relay bytes after revoke = in:%d out:%d, want preserved 4/0", session.RelayBytesIn, session.RelayBytesOut)
	}
	events, err := svc.ListRiskEvents(ctx, result.Session.AccountID)
	if err != nil {
		t.Fatalf("ListRiskEvents returned error: %v", err)
	}
	if !cloudhubHasRiskEvent(events, cloudhub.RiskRelaySessionRevoked) {
		t.Fatalf("risk events = %+v, want relay_session_revoked", events)
	}
}

func TestServerRejectsInvalidAndUnavailableRelayJoins(t *testing.T) {
	ctx := context.Background()
	svc := cloudhub.NewService(cloudhub.NewMemoryStore())
	runtime := NewServer(svc, WithJoinTimeout(25*time.Millisecond))
	addr := serveRelayForTest(t, runtime)
	result, source, _ := createAuthorizedRelaySession(t, ctx, svc, runtime, 2*time.Minute)

	t.Run("wrong token", func(t *testing.T) {
		conn, reader := openRelayConn(t, addr, map[string]string{
			"session_id": result.Session.ID,
			"role":       string(RoleSource),
			"device_id":  source.ID,
			"token":      "wrong-token",
		})
		defer conn.Close()
		errMsg := readRelayAck(t, reader, false)
		if !strings.Contains(errMsg, "unauthorized") {
			t.Fatalf("error = %q, want unauthorized", errMsg)
		}
	})

	t.Run("wrong device", func(t *testing.T) {
		conn, reader := openRelayConn(t, addr, map[string]string{
			"session_id": result.Session.ID,
			"role":       string(RoleTarget),
			"device_id":  source.ID,
			"token":      result.TargetJoinToken,
		})
		defer conn.Close()
		errMsg := readRelayAck(t, reader, false)
		if !strings.Contains(errMsg, "unauthorized") {
			t.Fatalf("error = %q, want unauthorized", errMsg)
		}
	})

	t.Run("wrong role", func(t *testing.T) {
		conn, reader := openRelayConn(t, addr, map[string]string{
			"session_id": result.Session.ID,
			"role":       "observer",
			"device_id":  source.ID,
			"token":      result.SourceJoinToken,
		})
		defer conn.Close()
		errMsg := readRelayAck(t, reader, false)
		if !strings.Contains(errMsg, "role") {
			t.Fatalf("error = %q, want role validation", errMsg)
		}
	})

	t.Run("duplicate join", func(t *testing.T) {
		firstConn, firstReader := openRelayConn(t, addr, map[string]string{
			"session_id": result.Session.ID,
			"role":       string(RoleSource),
			"device_id":  source.ID,
			"token":      result.SourceJoinToken,
		})
		defer firstConn.Close()

		secondConn, secondReader := openRelayConn(t, addr, map[string]string{
			"session_id": result.Session.ID,
			"role":       string(RoleSource),
			"device_id":  source.ID,
			"token":      result.SourceJoinToken,
		})
		defer secondConn.Close()
		errMsg := readRelayAck(t, secondReader, false)
		if !strings.Contains(errMsg, "already joined") {
			t.Fatalf("error = %q, want duplicate join rejection", errMsg)
		}
		errMsg = readRelayAck(t, firstReader, false)
		if !strings.Contains(errMsg, "timed out") {
			t.Fatalf("first join error = %q, want wait timeout", errMsg)
		}
	})

	t.Run("closed session", func(t *testing.T) {
		closedResult, closedSource, _ := createAuthorizedRelaySession(t, ctx, svc, runtime, 2*time.Minute)
		if _, err := svc.CloseRelaySession(ctx, cloudhub.CloseRelaySessionRequest{SessionID: closedResult.Session.ID}); err != nil {
			t.Fatalf("CloseRelaySession returned error: %v", err)
		}
		if err := runtime.RevokeRelaySession(closedResult.Session.ID); err != nil {
			t.Fatalf("RevokeRelaySession returned error: %v", err)
		}
		conn, reader := openRelayConn(t, addr, map[string]string{
			"session_id": closedResult.Session.ID,
			"role":       string(RoleSource),
			"device_id":  closedSource.ID,
			"token":      closedResult.SourceJoinToken,
		})
		defer conn.Close()
		errMsg := readRelayAck(t, reader, false)
		if !strings.Contains(errMsg, "revoked") {
			t.Fatalf("error = %q, want revoked session rejection", errMsg)
		}
	})

	t.Run("expired session", func(t *testing.T) {
		past := time.Now().UTC().Add(-5 * time.Minute)
		svc.SetNowForTest(func() time.Time { return past })
		expiredResult, expiredSource, _ := createAuthorizedRelaySession(t, ctx, svc, runtime, time.Minute)
		svc.SetNowForTest(func() time.Time { return time.Now().UTC() })
		conn, reader := openRelayConn(t, addr, map[string]string{
			"session_id": expiredResult.Session.ID,
			"role":       string(RoleSource),
			"device_id":  expiredSource.ID,
			"token":      expiredResult.SourceJoinToken,
		})
		defer conn.Close()
		errMsg := readRelayAck(t, reader, false)
		if !strings.Contains(errMsg, "expired") {
			t.Fatalf("error = %q, want expired session rejection", errMsg)
		}
	})

	t.Run("single side timeout", func(t *testing.T) {
		timeoutResult, timeoutSource, _ := createAuthorizedRelaySession(t, ctx, svc, runtime, 2*time.Minute)
		conn, reader := openRelayConn(t, addr, map[string]string{
			"session_id": timeoutResult.Session.ID,
			"role":       string(RoleSource),
			"device_id":  timeoutSource.ID,
			"token":      timeoutResult.SourceJoinToken,
		})
		defer conn.Close()
		errMsg := readRelayAck(t, reader, false)
		if !strings.Contains(errMsg, "timed out") {
			t.Fatalf("error = %q, want single-side timeout", errMsg)
		}
	})
}

func serveRelayForTest(t *testing.T, runtime *Server) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen returned error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runtime.Serve(ctx, ln)
	}()
	t.Cleanup(func() {
		cancel()
		_ = ln.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("relay Serve returned error: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("relay Serve did not stop")
		}
	})
	return ln.Addr().String()
}

func createAuthorizedRelaySession(t *testing.T, ctx context.Context, svc *cloudhub.Service, runtime *Server, ttl time.Duration) (cloudhub.RelaySessionResult, cloudhub.Device, cloudhub.Device) {
	t.Helper()
	result, source, target := createRelaySessionForRuntimeTest(t, ctx, svc, ttl)
	if err := runtime.AuthorizeRelaySession(result); err != nil {
		t.Fatalf("AuthorizeRelaySession returned error: %v", err)
	}
	return result, source, target
}

func createRelaySessionForRuntimeTest(t *testing.T, ctx context.Context, svc *cloudhub.Service, ttl time.Duration) (cloudhub.RelaySessionResult, cloudhub.Device, cloudhub.Device) {
	t.Helper()
	account, err := svc.CreateAccount(ctx, cloudhub.CreateAccountRequest{Email: "relay-owner-" + time.Now().Format("150405.000000") + "@example.com"})
	if err != nil {
		t.Fatalf("CreateAccount returned error: %v", err)
	}
	network, err := svc.CreateNetwork(ctx, cloudhub.CreateNetworkRequest{AccountID: account.ID, Name: "RelayNet"})
	if err != nil {
		t.Fatalf("CreateNetwork returned error: %v", err)
	}
	invite, err := svc.CreateInvite(ctx, cloudhub.CreateInviteRequest{
		AccountID: account.ID,
		NetworkID: network.ID,
		MaxUses:   2,
		OneTime:   false,
	})
	if err != nil {
		t.Fatalf("CreateInvite returned error: %v", err)
	}
	source, err := svc.JoinDevice(ctx, cloudhub.JoinDeviceRequest{Token: invite.Token, Code: invite.Code, DeviceName: "source"})
	if err != nil {
		t.Fatalf("JoinDevice source returned error: %v", err)
	}
	target, err := svc.JoinDevice(ctx, cloudhub.JoinDeviceRequest{Token: invite.Token, Code: invite.Code, DeviceName: "target"})
	if err != nil {
		t.Fatalf("JoinDevice target returned error: %v", err)
	}
	if _, err := svc.HeartbeatDevice(ctx, cloudhub.HeartbeatDeviceRequest{DeviceID: source.ID, Status: cloudhub.DeviceStatusOnline}); err != nil {
		t.Fatalf("HeartbeatDevice source returned error: %v", err)
	}
	if _, err := svc.HeartbeatDevice(ctx, cloudhub.HeartbeatDeviceRequest{DeviceID: target.ID, Status: cloudhub.DeviceStatusOnline}); err != nil {
		t.Fatalf("HeartbeatDevice target returned error: %v", err)
	}
	result, err := svc.CreateRelaySession(ctx, cloudhub.CreateRelaySessionRequest{
		AccountID:      account.ID,
		NetworkID:      network.ID,
		SourceDeviceID: source.ID,
		TargetDeviceID: target.ID,
		TTL:            ttl,
	})
	if err != nil {
		t.Fatalf("CreateRelaySession returned error: %v", err)
	}
	return result, source, target
}

func openRelayConn(t *testing.T, addr string, handshake map[string]string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("Dial returned error: %v", err)
	}
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if err := json.NewEncoder(conn).Encode(handshake); err != nil {
		_ = conn.Close()
		t.Fatalf("encode handshake returned error: %v", err)
	}
	return conn, bufio.NewReader(conn)
}

func readRelayAck(t *testing.T, reader *bufio.Reader, wantOK bool) string {
	t.Helper()
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read relay ack returned error: %v", err)
	}
	var ack struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(line, &ack); err != nil {
		t.Fatalf("decode relay ack %q returned error: %v", line, err)
	}
	if ack.OK != wantOK {
		t.Fatalf("relay ack = %+v, want ok=%v", ack, wantOK)
	}
	return ack.Error
}

func assertRelayPayload(t *testing.T, reader *bufio.Reader, want string) {
	t.Helper()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(reader, got); err != nil {
		t.Fatalf("ReadFull returned error: %v", err)
	}
	if string(got) != want {
		t.Fatalf("relay payload = %q, want %q", got, want)
	}
}

func assertRelayReadFails(t *testing.T, reader *bufio.Reader) {
	t.Helper()
	_, err := reader.Peek(1)
	if err == nil {
		t.Fatal("relay read succeeded after revoke, want closed connection")
	}
}

func waitForRelaySessionClosed(t *testing.T, ctx context.Context, svc *cloudhub.Service, sessionID string) cloudhub.RelaySession {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		session, err := svc.GetRelaySession(ctx, sessionID)
		if err != nil {
			t.Fatalf("GetRelaySession returned error: %v", err)
		}
		if session.Status == cloudhub.RelaySessionClosed {
			return session
		}
		time.Sleep(10 * time.Millisecond)
	}
	session, err := svc.GetRelaySession(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetRelaySession returned error: %v", err)
	}
	t.Fatalf("relay session = %+v, want closed", session)
	return cloudhub.RelaySession{}
}

func cloudhubHasRiskEvent(events []cloudhub.RiskEvent, kind cloudhub.RiskEventKind) bool {
	for _, event := range events {
		if event.Kind == kind {
			return true
		}
	}
	return false
}
