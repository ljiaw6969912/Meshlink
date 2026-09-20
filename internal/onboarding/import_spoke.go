package onboarding

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"meshlink/internal/config"
)

// ImportInstalledSpoke copies a valid installed client identity into an empty
// installation. It never reads an invitation quota, enrolls, or modifies the
// source. A service config can have any filename and resolves certificate paths
// relative to its own directory, independently of the destination layout.
func (m Manager) ImportInstalledSpoke(sourceConfigPath, inviteLink string) (bool, error) {
	if _, err := os.Lstat(m.activeConfigPath()); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if strings.TrimSpace(sourceConfigPath) == "" {
		return false, nil
	}
	source, err := readJSONFile(sourceConfigPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("读取已安装客户端配置失败：%w", err)
	}
	var cfg config.Config
	if err := json.Unmarshal(source, &cfg); err != nil {
		return false, fmt.Errorf("已安装客户端配置无效：%w", err)
	}
	if cfg.Mode != "spoke" {
		return false, nil
	}
	// Load can migrate and rewrite its input. Validate this in-memory copy so
	// even an unsupported or damaged old config stays byte-for-byte unchanged.
	if err := cfg.Validate(); err != nil {
		return false, fmt.Errorf("已安装客户端配置验证失败，原文件保持不变：%w", err)
	}
	if inviteLink != "" {
		invite, err := ParseInviteLink(inviteLink)
		if err != nil {
			return false, err
		}
		if !strings.EqualFold(cfg.Connect, meshConnectAddress(invite.Server)) {
			return false, nil
		}
	}
	sourceDir := filepath.Dir(sourceConfigPath)
	material := make([][]byte, 3)
	for i, path := range []string{cfg.CAFile, cfg.CertFile, cfg.KeyFile} {
		material[i], err = os.ReadFile(resolveConfigPath(sourceDir, path))
		if err != nil {
			return false, fmt.Errorf("读取已安装客户端证书或私钥失败，未重新登记：%w", err)
		}
	}
	pair, err := tls.X509KeyPair(material[1], material[2])
	if err != nil {
		return false, fmt.Errorf("已安装客户端证书与私钥不匹配，未重新登记：%w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return false, err
	}
	if leaf.Subject.CommonName != cfg.NodeID {
		return false, fmt.Errorf("已安装客户端证书身份与配置 NodeID 不一致，未重新登记")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(material[0]) {
		return false, fmt.Errorf("已安装客户端 CA 证书无效，未重新登记")
	}
	intermediates := x509.NewCertPool()
	for _, raw := range pair.Certificate[1:] {
		cert, err := x509.ParseCertificate(raw)
		if err != nil {
			return false, err
		}
		intermediates.AddCert(cert)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return false, fmt.Errorf("已安装客户端证书验证失败，未重新登记：%w", err)
	}
	if err := os.MkdirAll(m.configsDir(), 0o700); err != nil {
		return false, err
	}
	if err := os.MkdirAll(m.certsDir(), 0o700); err != nil {
		return false, err
	}
	identityDir, err := os.MkdirTemp(m.certsDir(), "import-")
	if err != nil {
		return false, err
	}
	files := []string{filepath.Join(identityDir, "ca.pem"), filepath.Join(identityDir, "node.pem"), filepath.Join(identityDir, "node-key.pem")}
	keepIdentity := false
	defer func() {
		if !keepIdentity {
			// Only remove the three files and empty directory created here;
			// never recursively delete or overwrite unrelated identity material.
			for _, path := range files {
				_ = os.Remove(path)
			}
			_ = os.Remove(identityDir)
		}
	}()
	for i, path := range files {
		if err := os.WriteFile(path, material[i], 0o600); err != nil {
			return false, err
		}
	}
	for i, target := range []*string{&cfg.CAFile, &cfg.CertFile, &cfg.KeyFile} {
		relative, err := filepath.Rel(m.configsDir(), files[i])
		if err != nil {
			return false, err
		}
		*target = filepath.ToSlash(relative)
	}
	staged, err := os.CreateTemp(m.configsDir(), ".import-active-*.json")
	if err != nil {
		return false, err
	}
	stagedPath := staged.Name()
	defer os.Remove(stagedPath)
	if err := staged.Close(); err != nil {
		return false, err
	}
	if err := config.Write(stagedPath, cfg); err != nil {
		return false, err
	}
	// A hard link publishes the completed config atomically and cannot replace
	// a config created by another caller after our initial empty-target check.
	// Both names are in the same destination directory/filesystem.
	if err := os.Link(stagedPath, m.activeConfigPath()); err != nil {
		if _, statErr := os.Lstat(m.activeConfigPath()); statErr == nil {
			return false, nil
		}
		return false, fmt.Errorf("发布已安装客户端配置失败：%w", err)
	}
	keepIdentity = true
	return true, nil
}
