package onboarding

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"meshlink/internal/config"
)

// SelectRole retains the original installation and keeps the other role in a
// separate profile, so a client's trusted CA is never overwritten by a new hub.
func SelectRole(baseDir, mode string) (Manager, error) {
	m := Manager{BaseDir: baseDir}
	if mode != "hub" && mode != "spoke" {
		return m, fmt.Errorf("unsupported role %q", mode)
	}
	cfg, err := config.Load(m.activeConfigPath())
	if os.IsNotExist(err) {
		profile := Manager{BaseDir: filepath.Join(baseDir, "profiles", mode)}
		if _, err := os.Stat(profile.activeConfigPath()); err == nil {
			return profile, nil
		}
		return m, nil
	}
	if err != nil {
		return m, err
	}
	if cfg.Mode == mode {
		return m, nil
	}
	m.BaseDir = filepath.Join(baseDir, "profiles", mode)
	return m, nil
}

// ManagerForConfig only follows configuration files belonging to this copy of
// the application. An old service in another folder may be a different hub.
func ManagerForConfig(baseDir, configPath string) Manager {
	m := Manager{BaseDir: baseDir}
	base, err := filepath.Abs(baseDir)
	if err != nil {
		return m
	}
	path, err := filepath.Abs(configPath)
	if err != nil || !strings.EqualFold(filepath.Base(path), "active.json") || !strings.EqualFold(filepath.Base(filepath.Dir(path)), "configs") {
		return m
	}
	profile := filepath.Dir(filepath.Dir(path))
	rel, err := filepath.Rel(base, profile)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return m
	}
	m.BaseDir = profile
	return m
}
