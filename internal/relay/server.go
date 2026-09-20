package relay

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"meshlink/internal/cloudhub"
)

const maxHandshakeBytes = 8 << 10

type Server struct {
	service          *cloudhub.Service
	broker           *Broker
	endpoint         string
	handshakeTimeout time.Duration
	onError          func(error)

	mu     sync.RWMutex
	listen string
}

type ServerOption func(*serverConfig)

type serverConfig struct {
	endpoint         string
	joinTimeout      time.Duration
	handshakeTimeout time.Duration
	onError          func(error)
}

func WithEndpoint(endpoint string) ServerOption {
	return func(cfg *serverConfig) {
		cfg.endpoint = strings.TrimSpace(endpoint)
	}
}

func WithJoinTimeout(timeout time.Duration) ServerOption {
	return func(cfg *serverConfig) {
		cfg.joinTimeout = timeout
	}
}

func WithHandshakeTimeout(timeout time.Duration) ServerOption {
	return func(cfg *serverConfig) {
		cfg.handshakeTimeout = timeout
	}
}

func WithErrorCallback(callback func(error)) ServerOption {
	return func(cfg *serverConfig) {
		cfg.onError = callback
	}
}

func NewServer(service *cloudhub.Service, opts ...ServerOption) *Server {
	cfg := serverConfig{
		joinTimeout:      30 * time.Second,
		handshakeTimeout: 5 * time.Second,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	s := &Server{
		service:          service,
		endpoint:         cfg.endpoint,
		handshakeTimeout: cfg.handshakeTimeout,
		onError:          cfg.onError,
	}
	s.broker = NewBroker(
		WithSingleSideTimeout(cfg.joinTimeout),
		WithClosedCallback(s.handleClosed),
	)
	return s
}

func (s *Server) ListenAndServe(ctx context.Context, listen string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp", listen)
	if err != nil {
		return err
	}
	return s.Serve(ctx, ln)
}

func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if ln == nil {
		return fmt.Errorf("relay listener is required")
	}
	s.setListen(ln.Addr().String())

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = ln.Close()
		case <-done:
		}
	}()
	defer close(done)

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handleConn(ctx, conn)
	}
}

func (s *Server) ListenAddr() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.listen
}

func (s *Server) Endpoint() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.endpoint != "" {
		return s.endpoint
	}
	return s.listen
}

func (s *Server) AuthorizeRelaySession(result cloudhub.RelaySessionResult) error {
	session := result.Session
	if strings.TrimSpace(session.ID) == "" {
		return fmt.Errorf("session_id is required")
	}
	if session.Status == cloudhub.RelaySessionClosed {
		return ErrRevoked
	}
	if strings.TrimSpace(result.SourceJoinToken) == "" || strings.TrimSpace(result.TargetJoinToken) == "" {
		return ErrUnauthorized
	}
	budget := result.RelayByteBudget
	if s.service != nil {
		if loaded, err := s.service.GetRelaySessionByteBudget(context.Background(), session.ID); err == nil {
			budget = loaded
		}
	}
	return s.broker.Authorize(AuthorizedSession{
		SessionID:           session.ID,
		AccountID:           session.AccountID,
		NetworkID:           session.NetworkID,
		SourceDeviceID:      session.SourceDeviceID,
		TargetDeviceID:      session.TargetDeviceID,
		SourceJoinTokenHash: HashJoinToken(session.ID, RoleSource, result.SourceJoinToken),
		TargetJoinTokenHash: HashJoinToken(session.ID, RoleTarget, result.TargetJoinToken),
		ExpiresAt:           session.ExpiresAt,
		ByteBudgetLimited:   budget.Limited,
		ByteBudgetRemaining: budget.RemainingBytes,
	})
}

func (s *Server) RevokeRelaySession(sessionID string) error {
	return s.broker.Revoke(sessionID)
}

func (s *Server) setListen(listen string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listen = listen
	if s.endpoint == "" {
		s.endpoint = listen
	}
}

type handshakeRequest struct {
	SessionID string `json:"session_id"`
	Role      string `json:"role"`
	DeviceID  string `json:"device_id"`
	Token     string `json:"token"`
}

func (s *Server) handleConn(ctx context.Context, client net.Conn) {
	defer client.Close()
	if s.handshakeTimeout > 0 {
		_ = client.SetDeadline(time.Now().Add(s.handshakeTimeout))
	}
	reader := bufio.NewReaderSize(client, maxHandshakeBytes)
	req, err := readHandshake(reader)
	if err != nil {
		_ = writeHandshakeResponse(client, false, err.Error())
		return
	}
	_ = client.SetDeadline(time.Time{})

	relayConn, err := s.broker.Join(ctx, JoinRequest{
		SessionID: req.SessionID,
		Role:      Role(strings.TrimSpace(req.Role)),
		DeviceID:  req.DeviceID,
		Token:     req.Token,
	})
	if err != nil {
		_ = writeHandshakeResponse(client, false, err.Error())
		return
	}
	defer relayConn.Close()

	if s.service != nil {
		if _, err := s.service.ActivateRelaySession(ctx, cloudhub.ActivateRelaySessionRequest{SessionID: req.SessionID}); err != nil {
			_ = writeHandshakeResponse(client, false, err.Error())
			return
		}
	}
	if err := writeHandshakeResponse(client, true, ""); err != nil {
		return
	}
	bridgeConnections(client, reader, relayConn)
}

func readHandshake(reader *bufio.Reader) (handshakeRequest, error) {
	line, err := readLimitedLine(reader, maxHandshakeBytes)
	if err != nil {
		return handshakeRequest{}, err
	}
	var req handshakeRequest
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return handshakeRequest{}, fmt.Errorf("invalid relay handshake: %w", err)
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.Role = strings.TrimSpace(req.Role)
	req.DeviceID = strings.TrimSpace(req.DeviceID)
	req.Token = strings.TrimSpace(req.Token)
	if req.SessionID == "" {
		return handshakeRequest{}, fmt.Errorf("session_id is required")
	}
	if req.Role == "" {
		return handshakeRequest{}, fmt.Errorf("role is required")
	}
	if req.DeviceID == "" {
		return handshakeRequest{}, fmt.Errorf("device_id is required")
	}
	if req.Token == "" {
		return handshakeRequest{}, ErrUnauthorized
	}
	return req, nil
}

func readLimitedLine(reader *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > max {
			return nil, fmt.Errorf("relay handshake is too large")
		}
		if err == nil {
			return line, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) && len(line) > 0 {
			return line, nil
		}
		return nil, fmt.Errorf("read relay handshake: %w", err)
	}
}

func writeHandshakeResponse(conn net.Conn, ok bool, message string) error {
	resp := struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}{
		OK:    ok,
		Error: message,
	}
	return json.NewEncoder(conn).Encode(resp)
}

func bridgeConnections(client net.Conn, clientReader io.Reader, relayConn net.Conn) {
	var once sync.Once
	closeBoth := func() {
		_ = client.Close()
		_ = relayConn.Close()
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(relayConn, clientReader)
		once.Do(closeBoth)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(client, relayConn)
		once.Do(closeBoth)
	}()
	wg.Wait()
}

func (s *Server) handleClosed(report SessionReport) {
	if s.service == nil {
		return
	}
	_, err := s.service.CloseRelaySession(context.Background(), cloudhub.CloseRelaySessionRequest{
		SessionID:     report.SessionID,
		RelayBytesIn:  report.BytesSourceToTarget,
		RelayBytesOut: report.BytesTargetToSource,
	})
	if err != nil {
		s.reportError(fmt.Errorf("record relay session close %s: %w", report.SessionID, err))
	}
}

func (s *Server) reportError(err error) {
	if err == nil || s.onError == nil {
		return
	}
	s.onError(err)
}
