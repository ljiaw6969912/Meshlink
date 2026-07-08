package onboarding

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"meshlink/internal/deployssh"
)

type Manager struct {
	BaseDir           string
	Now               func() time.Time
	HTTPClient        *http.Client
	LocalIPv4         func() (string, error)
	ResolveHost       func(string) ([]net.IP, error)
	SelfRelayDeployer deployssh.Deployer
}

func (m Manager) baseDir() string {
	if m.BaseDir != "" {
		return m.BaseDir
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return "."
}

func (m Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m Manager) configsDir() string {
	return filepath.Join(m.baseDir(), "configs")
}

func (m Manager) certsDir() string {
	return filepath.Join(m.baseDir(), "certs")
}

func (m Manager) invitesDir() string {
	return filepath.Join(m.baseDir(), "invites")
}

func (m Manager) logsDir() string {
	return filepath.Join(m.baseDir(), "logs")
}

func (m Manager) activeConfigPath() string {
	return filepath.Join(m.configsDir(), "active.json")
}

func (m Manager) inviteStorePath() string {
	return filepath.Join(m.invitesDir(), "invites.json")
}

func (m Manager) deviceRegistryPath() string {
	return filepath.Join(m.configsDir(), "devices.json")
}
