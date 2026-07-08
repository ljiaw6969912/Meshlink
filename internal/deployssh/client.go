package deployssh

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

type AuthConfig struct {
	Username       string `json:"username"`
	Password       string `json:"-"`
	PrivateKeyPEM  string `json:"-"`
	PrivateKeyPath string `json:"private_key_path,omitempty"`
}

func (a AuthConfig) String() string {
	return fmt.Sprintf("{Username:%q Password:%q PrivateKeyPEM:%q PrivateKeyPath:%q}", a.Username, redacted(a.Password), redacted(a.PrivateKeyPEM), a.PrivateKeyPath)
}

type DeployRequest struct {
	Host          string         `json:"host"`
	Port          int            `json:"port"`
	Auth          AuthConfig     `json:"auth"`
	Bundle        Bundle         `json:"bundle"`
	Layout        Layout         `json:"layout"`
	KnownHosts    KnownHostStore `json:"-"`
	AcceptHostKey bool           `json:"accept_host_key"`
}

func (r DeployRequest) String() string {
	return fmt.Sprintf("{Host:%q Port:%d Auth:%s Bundle:{PublicAddress:%q ListenPort:%d Files:%d} Layout:%+v AcceptHostKey:%t}",
		r.Host, r.Port, r.Auth.String(), r.Bundle.PublicAddress, r.Bundle.ListenPort, len(r.Bundle.Files), r.Layout.withDefaults(), r.AcceptHostKey)
}

type Deployer interface {
	Deploy(ctx context.Context, req DeployRequest) (ProvisionResult, error)
}

type DefaultDeployer struct{}

func (DefaultDeployer) Deploy(ctx context.Context, req DeployRequest) (ProvisionResult, error) {
	return Deploy(ctx, req)
}

func Deploy(ctx context.Context, req DeployRequest) (ProvisionResult, error) {
	client, trust, err := Dial(ctx, req)
	if err != nil {
		return ProvisionResult{}, err
	}
	defer client.Close()
	result, err := Provision(ctx, client, ProvisionRequest{Layout: req.Layout, Bundle: req.Bundle})
	result.HostTrust = trust
	return result, err
}

type sshRemoteClient struct {
	client *ssh.Client
}

func Dial(ctx context.Context, req DeployRequest) (*sshRemoteClient, HostTrust, error) {
	if strings.TrimSpace(req.Host) == "" {
		return nil, HostTrust{}, errors.New("云服务器地址不能为空")
	}
	if req.Port == 0 {
		req.Port = 22
	}
	if req.Port <= 0 || req.Port > 65535 {
		return nil, HostTrust{}, errors.New("SSH 端口必须在 1-65535 之间")
	}
	methods, err := BuildAuthMethods(req.Auth)
	if err != nil {
		return nil, HostTrust{}, err
	}
	host := strings.TrimSpace(req.Host)
	addr := net.JoinHostPort(host, strconv.Itoa(req.Port))
	policy := TrustStrict
	if req.AcceptHostKey {
		policy = TrustOnFirstUse
	}
	store := req.KnownHosts
	if store == nil {
		store = NewMemoryKnownHostStore()
	}
	var trust HostTrust
	cfg := &ssh.ClientConfig{
		User:            strings.TrimSpace(req.Auth.Username),
		Auth:            methods,
		Timeout:         20 * time.Second,
		HostKeyCallback: hostKeyCallback(host, req.Port, store, policy, &trust),
	}
	dialer := &net.Dialer{Timeout: 20 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, trust, friendlyDialError(err)
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		var changed *HostFingerprintChangedError
		if errors.As(err, &changed) {
			return nil, trust, err
		}
		var unknown *UnknownHostFingerprintError
		if errors.As(err, &unknown) {
			return nil, trust, err
		}
		return nil, trust, friendlyHandshakeError(err)
	}
	return &sshRemoteClient{client: ssh.NewClient(c, chans, reqs)}, trust, nil
}

func friendlyDialError(err error) error {
	return fmt.Errorf("SSH 网络不可达：请确认云服务器地址、SSH 端口、安全组和本机网络。%w", err)
}

func friendlyHandshakeError(err error) error {
	if strings.Contains(strings.ToLower(err.Error()), "unable to authenticate") {
		return fmt.Errorf("SSH 认证失败：请确认用户名、密码或私钥是否正确")
	}
	return fmt.Errorf("SSH 连接失败：%w", err)
}

func BuildAuthMethods(auth AuthConfig) ([]ssh.AuthMethod, error) {
	if strings.TrimSpace(auth.Username) == "" {
		return nil, errors.New("SSH 用户名不能为空")
	}
	var methods []ssh.AuthMethod
	if auth.Password != "" {
		methods = append(methods, ssh.Password(auth.Password))
	}
	keyPEM := strings.TrimSpace(auth.PrivateKeyPEM)
	if keyPEM == "" && strings.TrimSpace(auth.PrivateKeyPath) != "" {
		b, err := os.ReadFile(auth.PrivateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("读取 SSH 私钥失败：%w", err)
		}
		keyPEM = string(b)
	}
	if keyPEM != "" {
		signer, err := ssh.ParsePrivateKey([]byte(keyPEM))
		if err != nil {
			return nil, fmt.Errorf("SSH 私钥格式无效，请粘贴 PEM/OpenSSH 私钥内容：%w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if len(methods) == 0 {
		return nil, errors.New("请填写 SSH 密码或私钥")
	}
	return methods, nil
}

func (c *sshRemoteClient) Run(ctx context.Context, command string) (RunResult, error) {
	session, err := c.client.NewSession()
	if err != nil {
		return RunResult{}, err
	}
	defer session.Close()
	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	done := make(chan error, 1)
	go func() {
		done <- session.Run("sh -lc " + shellQuote(command))
	}()
	select {
	case err := <-done:
		result := RunResult{Stdout: stdout.String(), Stderr: stderr.String()}
		if exitErr, ok := err.(*ssh.ExitError); ok {
			result.ExitCode = exitErr.ExitStatus()
		}
		return result, err
	case <-ctx.Done():
		_ = session.Close()
		return RunResult{Stdout: stdout.String(), Stderr: stderr.String()}, ctx.Err()
	}
}

func (c *sshRemoteClient) Upload(ctx context.Context, remotePath string, content []byte, mode fs.FileMode) error {
	tmp := "/tmp/meshlink-upload-" + randomHex(8)
	if err := c.writeRemoteFile(ctx, tmp, content); err != nil {
		return err
	}
	cmd := privileged(fmt.Sprintf("mkdir -p %s && install -m %04o %s %s && rm -f %s",
		shellQuote(pathDir(remotePath)),
		mode.Perm(),
		shellQuote(tmp),
		shellQuote(remotePath),
		shellQuote(tmp),
	))
	if _, err := c.Run(ctx, cmd); err != nil {
		_, _ = c.Run(ctx, "rm -f "+shellQuote(tmp))
		return err
	}
	return nil
}

func (c *sshRemoteClient) writeRemoteFile(ctx context.Context, remotePath string, content []byte) error {
	session, err := c.client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	session.Stdin = bytes.NewReader(content)
	var stderr bytes.Buffer
	session.Stderr = &stderr
	done := make(chan error, 1)
	go func() {
		done <- session.Run("sh -lc " + shellQuote("umask 077 && cat > "+shellQuote(remotePath)))
	}()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return nil
	case <-ctx.Done():
		_ = session.Close()
		return ctx.Err()
	}
}

func (c *sshRemoteClient) Close() error {
	if c == nil || c.client == nil {
		return nil
	}
	return c.client.Close()
}

type TrustPolicy string

const (
	TrustStrict     TrustPolicy = "strict"
	TrustOnFirstUse TrustPolicy = "trust_on_first_use"
)

type HostTrustStatus string

const (
	HostTrustNew     HostTrustStatus = "new"
	HostTrustMatched HostTrustStatus = "matched"
)

type HostTrust struct {
	Status      HostTrustStatus `json:"status,omitempty"`
	Fingerprint string          `json:"fingerprint,omitempty"`
}

type HostFingerprintChangedError struct {
	Host     string
	Port     int
	Expected string
	Actual   string
}

func (e *HostFingerprintChangedError) Error() string {
	return fmt.Sprintf("SSH 主机指纹发生变化，已阻止部署。服务器 %s:%d 之前记录为 %s，本次为 %s。请确认云服务器没有被重装、替换或遭遇中间人攻击后再手动更新记录。", e.Host, e.Port, e.Expected, e.Actual)
}

type UnknownHostFingerprintError struct {
	Host        string
	Port        int
	Fingerprint string
}

func (e *UnknownHostFingerprintError) Error() string {
	return fmt.Sprintf("首次连接云服务器 %s:%d，主机指纹为 %s。请确认后重新部署或先执行云服务器检查。", e.Host, e.Port, e.Fingerprint)
}

type KnownHostStore interface {
	Get(host string, port int) (string, bool, error)
	Set(host string, port int, fingerprint string) error
}

type MemoryKnownHostStore struct {
	mu    sync.Mutex
	hosts map[string]string
}

func NewMemoryKnownHostStore() *MemoryKnownHostStore {
	return &MemoryKnownHostStore{hosts: make(map[string]string)}
}

func (s *MemoryKnownHostStore) Get(host string, port int) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	got, ok := s.hosts[knownHostKey(host, port)]
	return got, ok, nil
}

func (s *MemoryKnownHostStore) Set(host string, port int, fingerprint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hosts[knownHostKey(host, port)] = fingerprint
	return nil
}

type FileKnownHostStore struct {
	Path string
}

func (s FileKnownHostStore) Get(host string, port int) (string, bool, error) {
	data, err := s.load()
	if err != nil {
		return "", false, err
	}
	got, ok := data.Hosts[knownHostKey(host, port)]
	return got, ok, nil
}

func (s FileKnownHostStore) Set(host string, port int, fingerprint string) error {
	data, err := s.load()
	if err != nil {
		return err
	}
	if data.Hosts == nil {
		data.Hosts = make(map[string]string)
	}
	data.Hosts[knownHostKey(host, port)] = fingerprint
	return s.save(data)
}

type knownHostFile struct {
	Hosts map[string]string `json:"hosts"`
}

func (s FileKnownHostStore) load() (knownHostFile, error) {
	if s.Path == "" {
		return knownHostFile{Hosts: make(map[string]string)}, nil
	}
	b, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return knownHostFile{Hosts: make(map[string]string)}, nil
		}
		return knownHostFile{}, err
	}
	var data knownHostFile
	if err := json.Unmarshal(b, &data); err != nil {
		return knownHostFile{}, err
	}
	if data.Hosts == nil {
		data.Hosts = make(map[string]string)
	}
	return data, nil
}

func (s FileKnownHostStore) save(data knownHostFile) error {
	if s.Path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.Path, append(b, '\n'), 0o600)
}

func VerifyHostFingerprint(host string, port int, fingerprint string, store KnownHostStore, policy TrustPolicy) (HostTrust, error) {
	if store == nil {
		store = NewMemoryKnownHostStore()
	}
	known, ok, err := store.Get(host, port)
	if err != nil {
		return HostTrust{}, err
	}
	if !ok {
		if policy != TrustOnFirstUse {
			return HostTrust{}, &UnknownHostFingerprintError{Host: host, Port: port, Fingerprint: fingerprint}
		}
		if err := store.Set(host, port, fingerprint); err != nil {
			return HostTrust{}, err
		}
		return HostTrust{Status: HostTrustNew, Fingerprint: fingerprint}, nil
	}
	if known != fingerprint {
		return HostTrust{}, &HostFingerprintChangedError{Host: host, Port: port, Expected: known, Actual: fingerprint}
	}
	return HostTrust{Status: HostTrustMatched, Fingerprint: fingerprint}, nil
}

func hostKeyCallback(host string, port int, store KnownHostStore, policy TrustPolicy, trust *HostTrust) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		got, err := VerifyHostFingerprint(host, port, ssh.FingerprintSHA256(key), store, policy)
		if err != nil {
			return err
		}
		if trust != nil {
			*trust = got
		}
		return nil
	}
}

func knownHostKey(host string, port int) string {
	return strings.TrimSpace(host) + ":" + strconv.Itoa(port)
}

func redacted(value string) string {
	if value == "" {
		return ""
	}
	return "<redacted>"
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}

func pathDir(p string) string {
	i := strings.LastIndex(p, "/")
	if i <= 0 {
		return "."
	}
	return p[:i]
}
