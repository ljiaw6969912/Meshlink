package relay

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Role string

const (
	RoleSource Role = "source"
	RoleTarget Role = "target"
)

var (
	ErrNotFound      = errors.New("relay session not found")
	ErrAlreadyExists = errors.New("relay session already authorized")
	ErrUnauthorized  = errors.New("relay join unauthorized")
	ErrAlreadyJoined = errors.New("relay endpoint already joined")
	ErrExpired       = errors.New("relay session expired")
	ErrRevoked       = errors.New("relay session revoked")
	ErrJoinTimeout   = errors.New("relay endpoint timed out waiting for peer")
	ErrByteBudget    = errors.New("relay byte budget exhausted")
)

type AuthorizedSession struct {
	SessionID           string
	AccountID           string
	NetworkID           string
	SourceDeviceID      string
	TargetDeviceID      string
	SourceJoinTokenHash string
	TargetJoinTokenHash string
	ExpiresAt           time.Time
	ByteBudgetLimited   bool
	ByteBudgetRemaining int64
}

type JoinRequest struct {
	SessionID string
	Role      Role
	DeviceID  string
	Token     string
}

type SessionReport struct {
	SessionID           string
	AccountID           string
	NetworkID           string
	SourceDeviceID      string
	TargetDeviceID      string
	StartedAt           time.Time
	EndedAt             time.Time
	BytesSourceToTarget int64
	BytesTargetToSource int64
}

type Broker struct {
	mu                sync.Mutex
	sessions          map[string]*sessionState
	accountBudgets    map[string]*byteBudgetState
	now               func() time.Time
	singleSideTimeout time.Duration
	onClosed          func(SessionReport)
}

type Option func(*Broker)

func WithNow(now func() time.Time) Option {
	return func(b *Broker) {
		if now != nil {
			b.now = now
		}
	}
}

func WithSingleSideTimeout(timeout time.Duration) Option {
	return func(b *Broker) {
		b.singleSideTimeout = timeout
	}
}

func WithClosedCallback(callback func(SessionReport)) Option {
	return func(b *Broker) {
		b.onClosed = callback
	}
}

func NewBroker(opts ...Option) *Broker {
	b := &Broker{
		sessions:          map[string]*sessionState{},
		accountBudgets:    map[string]*byteBudgetState{},
		singleSideTimeout: 30 * time.Second,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

func HashJoinToken(sessionID string, role Role, token string) string {
	sum := sha256.Sum256([]byte(sessionID + ":" + string(role) + ":" + token))
	return hex.EncodeToString(sum[:])
}

func (b *Broker) Authorize(grant AuthorizedSession) error {
	grant.SessionID = strings.TrimSpace(grant.SessionID)
	grant.AccountID = strings.TrimSpace(grant.AccountID)
	grant.NetworkID = strings.TrimSpace(grant.NetworkID)
	grant.SourceDeviceID = strings.TrimSpace(grant.SourceDeviceID)
	grant.TargetDeviceID = strings.TrimSpace(grant.TargetDeviceID)
	grant.SourceJoinTokenHash = strings.TrimSpace(grant.SourceJoinTokenHash)
	grant.TargetJoinTokenHash = strings.TrimSpace(grant.TargetJoinTokenHash)
	if grant.SessionID == "" {
		return fmt.Errorf("session_id is required")
	}
	if grant.SourceDeviceID == "" {
		return fmt.Errorf("source_device_id is required")
	}
	if grant.TargetDeviceID == "" {
		return fmt.Errorf("target_device_id is required")
	}
	if grant.SourceJoinTokenHash == "" || grant.TargetJoinTokenHash == "" {
		return fmt.Errorf("join token hashes are required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.sessions[grant.SessionID]; ok {
		return ErrAlreadyExists
	}
	state := &sessionState{grant: grant}
	if grant.ByteBudgetLimited {
		state.byteBudget = b.retainAccountBudgetLocked(grant)
	}
	b.sessions[grant.SessionID] = state
	return nil
}

func (b *Broker) Join(ctx context.Context, req JoinRequest) (net.Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.DeviceID = strings.TrimSpace(req.DeviceID)
	req.Token = strings.TrimSpace(req.Token)

	b.mu.Lock()
	state, err := b.validatedJoinStateLocked(req)
	if err != nil {
		b.mu.Unlock()
		return nil, err
	}
	if peerWaitingLocked(state, req.Role) {
		conn := b.pairLocked(state, req)
		b.mu.Unlock()
		return conn, nil
	}
	if endpointJoinedLocked(state, req.Role) {
		b.mu.Unlock()
		return nil, ErrAlreadyJoined
	}
	waiter := &joinWaiter{result: make(chan joinResult, 1)}
	setWaiterLocked(state, req.Role, waiter)
	b.mu.Unlock()

	conn, err := b.waitForPeer(ctx, state, req.Role, waiter)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func (b *Broker) Revoke(sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	b.mu.Lock()
	state, ok := b.sessions[sessionID]
	if !ok {
		b.mu.Unlock()
		return ErrNotFound
	}
	state.revoked = true
	if state.sourceWaiter != nil {
		state.sourceWaiter.result <- joinResult{err: ErrRevoked}
		state.sourceWaiter = nil
		state.sourceJoined = false
	}
	if state.targetWaiter != nil {
		state.targetWaiter.result <- joinResult{err: ErrRevoked}
		state.targetWaiter = nil
		state.targetJoined = false
	}
	sourceConn := state.sourceConn
	targetConn := state.targetConn
	if sourceConn == nil && targetConn == nil {
		b.releaseSessionBudgetLocked(state)
	}
	b.mu.Unlock()
	if sourceConn != nil {
		_ = sourceConn.Close()
	}
	if targetConn != nil {
		_ = targetConn.Close()
	}
	return nil
}

type sessionState struct {
	grant AuthorizedSession

	revoked bool

	sourceWaiter *joinWaiter
	targetWaiter *joinWaiter
	sourceJoined bool
	targetJoined bool
	sourceConn   *meteredConn
	targetConn   *meteredConn

	startedAt    time.Time
	sourceClosed bool
	targetClosed bool
	reported     bool
	activeWrites int

	bytesSourceToTarget atomic.Int64
	bytesTargetToSource atomic.Int64

	byteBudget      *byteBudgetState
	budgetReleased  bool
	budgetCloseOnce sync.Once
}

type byteBudgetState struct {
	key       string
	refs      int
	remaining atomic.Int64
}

type joinWaiter struct {
	result chan joinResult
}

type joinResult struct {
	conn net.Conn
	err  error
}

func (b *Broker) validatedJoinStateLocked(req JoinRequest) (*sessionState, error) {
	if req.SessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	if req.DeviceID == "" {
		return nil, fmt.Errorf("device_id is required")
	}
	if req.Token == "" {
		return nil, ErrUnauthorized
	}
	state, ok := b.sessions[req.SessionID]
	if !ok {
		return nil, ErrNotFound
	}
	if state.revoked {
		return nil, ErrRevoked
	}
	if !state.grant.ExpiresAt.IsZero() && b.now().After(state.grant.ExpiresAt) {
		b.releaseSessionBudgetLocked(state)
		return nil, ErrExpired
	}
	switch req.Role {
	case RoleSource:
		if req.DeviceID != state.grant.SourceDeviceID {
			return nil, ErrUnauthorized
		}
		if !equalHash(HashJoinToken(req.SessionID, RoleSource, req.Token), state.grant.SourceJoinTokenHash) {
			return nil, ErrUnauthorized
		}
	case RoleTarget:
		if req.DeviceID != state.grant.TargetDeviceID {
			return nil, ErrUnauthorized
		}
		if !equalHash(HashJoinToken(req.SessionID, RoleTarget, req.Token), state.grant.TargetJoinTokenHash) {
			return nil, ErrUnauthorized
		}
	default:
		return nil, fmt.Errorf("relay role must be source or target")
	}
	return state, nil
}

func peerWaitingLocked(state *sessionState, role Role) bool {
	switch role {
	case RoleSource:
		return state.targetWaiter != nil
	case RoleTarget:
		return state.sourceWaiter != nil
	default:
		return false
	}
}

func endpointJoinedLocked(state *sessionState, role Role) bool {
	switch role {
	case RoleSource:
		return state.sourceJoined
	case RoleTarget:
		return state.targetJoined
	default:
		return false
	}
}

func setWaiterLocked(state *sessionState, role Role, waiter *joinWaiter) {
	switch role {
	case RoleSource:
		state.sourceWaiter = waiter
		state.sourceJoined = true
	case RoleTarget:
		state.targetWaiter = waiter
		state.targetJoined = true
	}
}

func clearWaiterLocked(state *sessionState, role Role, waiter *joinWaiter) bool {
	switch role {
	case RoleSource:
		if state.sourceWaiter == waiter {
			state.sourceWaiter = nil
			state.sourceJoined = false
			return true
		}
	case RoleTarget:
		if state.targetWaiter == waiter {
			state.targetWaiter = nil
			state.targetJoined = false
			return true
		}
	}
	return false
}

func (b *Broker) pairLocked(state *sessionState, req JoinRequest) net.Conn {
	sourceRaw, targetRaw := net.Pipe()
	sourceConn := &meteredConn{
		Conn:      sourceRaw,
		broker:    b,
		sessionID: state.grant.SessionID,
		role:      RoleSource,
		state:     state,
	}
	targetConn := &meteredConn{
		Conn:      targetRaw,
		broker:    b,
		sessionID: state.grant.SessionID,
		role:      RoleTarget,
		state:     state,
	}
	state.sourceConn = sourceConn
	state.targetConn = targetConn
	state.sourceJoined = true
	state.targetJoined = true
	state.startedAt = b.now()
	switch req.Role {
	case RoleSource:
		state.targetWaiter.result <- joinResult{conn: targetConn}
		state.targetWaiter = nil
		return sourceConn
	case RoleTarget:
		state.sourceWaiter.result <- joinResult{conn: sourceConn}
		state.sourceWaiter = nil
		return targetConn
	default:
		return nil
	}
}

func (b *Broker) waitForPeer(ctx context.Context, state *sessionState, role Role, waiter *joinWaiter) (net.Conn, error) {
	var timeout <-chan time.Time
	var timer *time.Timer
	if b.singleSideTimeout > 0 {
		timer = time.NewTimer(b.singleSideTimeout)
		timeout = timer.C
		defer timer.Stop()
	}
	select {
	case result := <-waiter.result:
		return result.conn, result.err
	case <-ctx.Done():
		if result, ok := receiveMatchedResult(waiter); ok {
			return result.conn, result.err
		}
		b.mu.Lock()
		removed := clearWaiterLocked(state, role, waiter)
		b.mu.Unlock()
		if !removed {
			result := <-waiter.result
			return result.conn, result.err
		}
		return nil, ctx.Err()
	case <-timeout:
		if result, ok := receiveMatchedResult(waiter); ok {
			return result.conn, result.err
		}
		b.mu.Lock()
		removed := clearWaiterLocked(state, role, waiter)
		b.mu.Unlock()
		if !removed {
			result := <-waiter.result
			return result.conn, result.err
		}
		return nil, ErrJoinTimeout
	}
}

func receiveMatchedResult(waiter *joinWaiter) (joinResult, bool) {
	select {
	case result := <-waiter.result:
		return result, true
	default:
		return joinResult{}, false
	}
}

type meteredConn struct {
	net.Conn
	broker    *Broker
	sessionID string
	role      Role
	state     *sessionState
	closeOnce sync.Once
}

func (c *meteredConn) Write(p []byte) (int, error) {
	originalLen := len(p)
	if originalLen == 0 {
		return c.Conn.Write(p)
	}
	if !c.broker.beginWrite(c.sessionID) {
		return 0, ErrRevoked
	}
	defer c.broker.endWrite(c.sessionID)
	allowed := c.state.reserveBytes(originalLen)
	if allowed <= 0 {
		c.closeBudgetExhausted()
		return 0, ErrByteBudget
	}
	partial := allowed < originalLen
	if partial {
		p = p[:allowed]
	}
	n, err := c.Conn.Write(p)
	if n < allowed {
		c.state.releaseBytes(allowed - n)
	}
	if n > 0 {
		switch c.role {
		case RoleSource:
			c.state.bytesSourceToTarget.Add(int64(n))
		case RoleTarget:
			c.state.bytesTargetToSource.Add(int64(n))
		}
	}
	if partial && err == nil {
		err = ErrByteBudget
	}
	if c.state.budgetRemaining() <= 0 {
		c.closeBudgetExhausted()
	}
	return n, err
}

func (c *meteredConn) Close() error {
	err := c.Conn.Close()
	c.closeOnce.Do(func() {
		c.broker.endpointClosed(c.sessionID, c.role)
	})
	return err
}

func (b *Broker) endpointClosed(sessionID string, role Role) {
	b.mu.Lock()
	state, ok := b.sessions[sessionID]
	if !ok {
		b.mu.Unlock()
		return
	}
	switch role {
	case RoleSource:
		state.sourceClosed = true
	case RoleTarget:
		state.targetClosed = true
	}
	callback, report := b.finalizeClosedLocked(sessionID, state)
	b.mu.Unlock()
	if callback != nil {
		callback(report)
	}
}

func (b *Broker) beginWrite(sessionID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	state, ok := b.sessions[sessionID]
	if !ok || state.revoked || state.reported || state.sourceClosed || state.targetClosed {
		return false
	}
	state.activeWrites++
	return true
}

func (b *Broker) endWrite(sessionID string) {
	b.mu.Lock()
	state, ok := b.sessions[sessionID]
	if !ok {
		b.mu.Unlock()
		return
	}
	if state.activeWrites > 0 {
		state.activeWrites--
	}
	callback, report := b.finalizeClosedLocked(sessionID, state)
	b.mu.Unlock()
	if callback != nil {
		callback(report)
	}
}

func (b *Broker) finalizeClosedLocked(sessionID string, state *sessionState) (func(SessionReport), SessionReport) {
	if !state.sourceClosed || !state.targetClosed || state.reported || state.activeWrites != 0 {
		return nil, SessionReport{}
	}
	state.reported = true
	report := SessionReport{
		SessionID:           state.grant.SessionID,
		AccountID:           state.grant.AccountID,
		NetworkID:           state.grant.NetworkID,
		SourceDeviceID:      state.grant.SourceDeviceID,
		TargetDeviceID:      state.grant.TargetDeviceID,
		StartedAt:           state.startedAt,
		EndedAt:             b.now(),
		BytesSourceToTarget: state.bytesSourceToTarget.Load(),
		BytesTargetToSource: state.bytesTargetToSource.Load(),
	}
	callback := b.onClosed
	b.releaseSessionBudgetLocked(state)
	delete(b.sessions, sessionID)
	return callback, report
}

func (c *meteredConn) closeBudgetExhausted() {
	c.state.budgetCloseOnce.Do(func() {
		if c.state.sourceConn != nil {
			_ = c.state.sourceConn.Close()
		}
		if c.state.targetConn != nil {
			_ = c.state.targetConn.Close()
		}
	})
}

func (s *sessionState) reserveBytes(want int) int {
	if want <= 0 {
		return 0
	}
	if s.byteBudget == nil {
		return want
	}
	for {
		remaining := s.byteBudget.remaining.Load()
		if remaining <= 0 {
			return 0
		}
		allowed := int64(want)
		if allowed > remaining {
			allowed = remaining
		}
		if s.byteBudget.remaining.CompareAndSwap(remaining, remaining-allowed) {
			return int(allowed)
		}
	}
}

func (s *sessionState) releaseBytes(count int) {
	if count <= 0 || s.byteBudget == nil {
		return
	}
	s.byteBudget.remaining.Add(int64(count))
}

func (s *sessionState) budgetRemaining() int64 {
	if s.byteBudget == nil {
		return 1
	}
	return s.byteBudget.remaining.Load()
}

func equalHash(got, want string) bool {
	if len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func (b *Broker) retainAccountBudgetLocked(grant AuthorizedSession) *byteBudgetState {
	key := strings.TrimSpace(grant.AccountID)
	if key == "" {
		key = grant.SessionID
	}
	if budget, ok := b.accountBudgets[key]; ok {
		budget.refs++
		return budget
	}
	budget := &byteBudgetState{key: key, refs: 1}
	budget.remaining.Store(maxInt64(grant.ByteBudgetRemaining, 0))
	b.accountBudgets[key] = budget
	return budget
}

func (b *Broker) releaseSessionBudgetLocked(state *sessionState) {
	if state == nil || state.byteBudget == nil || state.budgetReleased {
		return
	}
	state.budgetReleased = true
	budget := state.byteBudget
	budget.refs--
	if budget.refs <= 0 {
		delete(b.accountBudgets, budget.key)
	}
}
