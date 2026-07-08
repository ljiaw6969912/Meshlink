package onboarding

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

const (
	defaultInviteTTL         = 10 * time.Minute
	defaultInviteMaxUses     = 1
	defaultLongLivedMaxUses  = 3
	defaultInviteMaxFailures = 5
)

type CreateInviteRequest struct {
	Server          string        `json:"server"`
	Protocol        string        `json:"protocol"`
	TTL             time.Duration `json:"ttl"`
	MaxUses         int           `json:"max_uses"`
	LongLived       bool          `json:"long_lived,omitempty"`
	ReplaceExisting bool          `json:"replace_existing,omitempty"`
}

type CreateInviteResult struct {
	Token     string    `json:"token"`
	Code      string    `json:"code"`
	Server    string    `json:"server"`
	Protocol  string    `json:"protocol"`
	Link      string    `json:"link"`
	ExpiresAt time.Time `json:"expires_at"`
	LongLived bool      `json:"long_lived,omitempty"`
	Uses      int       `json:"uses,omitempty"`
	MaxUses   int       `json:"max_uses"`
}

type InviteLink struct {
	Server   string
	Protocol string
	Token    string
}

type InviteStore struct {
	Invites []Invite `json:"invites"`
}

type Invite struct {
	Token       string     `json:"token"`
	Code        string     `json:"code,omitempty"`
	CodeHash    string     `json:"code_hash,omitempty"`
	Server      string     `json:"server"`
	Protocol    string     `json:"protocol"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	LongLived   bool       `json:"long_lived,omitempty"`
	UsedAt      *time.Time `json:"used_at,omitempty"`
	Uses        int        `json:"uses"`
	MaxUses     int        `json:"max_uses"`
	Failures    int        `json:"failures"`
	MaxFailures int        `json:"max_failures"`
	SourceAddr  string     `json:"source_addr,omitempty"`
}

func (m Manager) CreateInvite(req CreateInviteRequest) (CreateInviteResult, error) {
	if req.Server == "" {
		return CreateInviteResult{}, fmt.Errorf("server is required")
	}
	if req.Protocol == "" {
		req.Protocol = "tcp_tls_v1"
	}
	if req.Protocol != "tcp_tls_v1" {
		return CreateInviteResult{}, fmt.Errorf("unsupported transport protocol %q", req.Protocol)
	}
	if req.LongLived {
		req.TTL = 0
		if req.MaxUses <= 0 {
			req.MaxUses = defaultLongLivedMaxUses
		}
	} else if req.TTL <= 0 {
		req.TTL = defaultInviteTTL
	}
	if !req.LongLived && req.MaxUses <= 0 {
		req.MaxUses = defaultInviteMaxUses
	}
	token, err := randomToken()
	if err != nil {
		return CreateInviteResult{}, err
	}
	code, err := randomCode()
	if err != nil {
		return CreateInviteResult{}, err
	}
	now := m.now()
	invite := Invite{
		Token:       token,
		Code:        code,
		CodeHash:    hashInviteCode(token, code),
		Server:      req.Server,
		Protocol:    req.Protocol,
		CreatedAt:   now,
		LongLived:   req.LongLived,
		MaxUses:     req.MaxUses,
		MaxFailures: defaultInviteMaxFailures,
	}
	if req.TTL > 0 {
		invite.ExpiresAt = now.Add(req.TTL)
	}
	store, err := m.loadInviteStore()
	if err != nil {
		return CreateInviteResult{}, err
	}
	if req.ReplaceExisting {
		store.Invites = []Invite{invite}
	} else {
		store.Invites = append(store.Invites, invite)
	}
	if err := m.saveInviteStore(store); err != nil {
		return CreateInviteResult{}, err
	}
	if err := m.writeAudit("invite_created", map[string]any{
		"server":     invite.Server,
		"protocol":   invite.Protocol,
		"long_lived": invite.LongLived,
		"max_uses":   invite.MaxUses,
		"expires_at": invite.ExpiresAt,
	}); err != nil {
		return CreateInviteResult{}, err
	}
	return CreateInviteResult{
		Token:     token,
		Code:      code,
		Server:    req.Server,
		Protocol:  req.Protocol,
		Link:      buildInviteLink(req.Server, req.Protocol, token),
		ExpiresAt: invite.ExpiresAt,
		LongLived: invite.LongLived,
		Uses:      invite.Uses,
		MaxUses:   invite.MaxUses,
	}, nil
}

func (m Manager) LatestInvite() (CreateInviteResult, bool, error) {
	store, err := m.loadInviteStore()
	if err != nil {
		return CreateInviteResult{}, false, err
	}
	if len(store.Invites) == 0 {
		return CreateInviteResult{}, false, nil
	}
	latest := store.Invites[len(store.Invites)-1]
	protocol := latest.Protocol
	if protocol == "" {
		protocol = "tcp_tls_v1"
	}
	return CreateInviteResult{
		Token:     latest.Token,
		Code:      latest.Code,
		Server:    latest.Server,
		Protocol:  protocol,
		Link:      buildInviteLink(latest.Server, protocol, latest.Token),
		ExpiresAt: latest.ExpiresAt,
		LongLived: latest.LongLived,
		Uses:      latest.Uses,
		MaxUses:   latest.MaxUses,
	}, true, nil
}

func ParseInviteLink(raw string) (InviteLink, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return InviteLink{}, err
	}
	if u.Scheme != "meshlink" || u.Host != "join" {
		return InviteLink{}, fmt.Errorf("invalid invite link")
	}
	q := u.Query()
	link := InviteLink{
		Server:   q.Get("server"),
		Protocol: q.Get("protocol"),
		Token:    q.Get("token"),
	}
	if link.Protocol == "" {
		link.Protocol = "tcp_tls_v1"
	}
	if link.Server == "" || link.Token == "" {
		return InviteLink{}, fmt.Errorf("invite link missing server or token")
	}
	return link, nil
}

func buildInviteLink(server, protocol, token string) string {
	q := url.Values{}
	q.Set("v", "1")
	q.Set("server", server)
	q.Set("protocol", protocol)
	q.Set("token", token)
	return (&url.URL{Scheme: "meshlink", Host: "join", RawQuery: q.Encode()}).String()
}

func (m Manager) loadInviteStore() (InviteStore, error) {
	b, err := os.ReadFile(m.inviteStorePath())
	if err != nil {
		if os.IsNotExist(err) {
			return InviteStore{}, nil
		}
		return InviteStore{}, err
	}
	var store InviteStore
	if err := json.Unmarshal(b, &store); err != nil {
		return InviteStore{}, err
	}
	return store, nil
}

func (m Manager) saveInviteStore(store InviteStore) error {
	if err := os.MkdirAll(filepath.Dir(m.inviteStorePath()), 0o700); err != nil {
		return err
	}
	return writePrettyJSON(m.inviteStorePath(), store)
}

func randomToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func randomCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func hashInviteCode(token, code string) string {
	sum := sha256.Sum256([]byte(token + ":" + code))
	return hex.EncodeToString(sum[:])
}

func verifyInviteCode(invite Invite, code string) bool {
	if invite.CodeHash != "" {
		got := hashInviteCode(invite.Token, code)
		return subtle.ConstantTimeCompare([]byte(got), []byte(invite.CodeHash)) == 1
	}
	return invite.Code == code
}
