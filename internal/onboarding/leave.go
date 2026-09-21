package onboarding

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"meshlink/internal/config"
)

const leaveTransactionDirName = ".leave-network"

var (
	leaveRename = os.Rename
	leaveRemove = os.Remove
)

type leaveFile struct {
	original string
	staged   string
}

func (m Manager) LeaveNetwork(serviceName string) error {
	configPath := m.activeConfigPath()
	baseDir, err := filepath.Abs(m.baseDir())
	if err != nil {
		return err
	}
	transactionDir, err := containedPath(baseDir, filepath.Join(filepath.Dir(configPath), leaveTransactionDirName))
	if err != nil {
		return err
	}
	if _, err := os.Stat(transactionDir); err == nil {
		if err := recoverLeaveTransaction(baseDir, configPath, serviceName, transactionDir); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(cfg.Mode), "spoke") {
		return fmt.Errorf("leave network is only supported for spoke mode")
	}

	files, err := managedLeaveFiles(baseDir, configPath, serviceName, cfg, transactionDir)
	if err != nil {
		return err
	}
	if err := os.Mkdir(transactionDir, 0o700); err != nil {
		return err
	}
	moved := make([]leaveFile, 0, len(files))
	for _, file := range files {
		if err := leaveRename(file.original, file.staged); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return rollbackLeaveTransaction(transactionDir, moved, err)
		}
		moved = append(moved, file)
	}
	return purgeLeaveTransaction(transactionDir, files)
}

func managedLeaveFiles(baseDir, configPath, serviceName string, cfg *config.Config, transactionDir string) ([]leaveFile, error) {
	configDir := filepath.Dir(configPath)
	paths := []string{
		statusPath(configPath, serviceName),
		resolveConfigPath(configDir, cfg.KeyFile),
		resolveConfigPath(configDir, cfg.CertFile),
		resolveConfigPath(configDir, cfg.CAFile),
		configPath,
		filepath.Join(configDir, "join-state.json"),
	}
	files := make([]leaveFile, len(paths))
	for i, path := range paths {
		contained, err := containedPath(baseDir, path)
		if err != nil {
			return nil, err
		}
		files[i] = leaveFile{
			original: contained,
			staged:   filepath.Join(transactionDir, fmt.Sprintf("%02d", i)),
		}
	}
	// Keep the existing "04" commit marker compatible with older versions,
	// but stage the new invitation record before committing the identity.
	files[4], files[5] = files[5], files[4]
	return files, nil
}

func recoverLeaveTransaction(baseDir, configPath, serviceName, transactionDir string) error {
	stagedConfig := filepath.Join(transactionDir, "04")
	if _, err := os.Stat(stagedConfig); err == nil {
		return purgeLeaveTransaction(transactionDir, stagedLeaveFiles(transactionDir))
	} else if !os.IsNotExist(err) {
		return err
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return leaveRemove(transactionDir)
		}
		return err
	}
	files, err := managedLeaveFiles(baseDir, configPath, serviceName, cfg, transactionDir)
	if err != nil {
		return err
	}
	return rollbackLeaveTransaction(transactionDir, files, nil)
}

func stagedLeaveFiles(transactionDir string) []leaveFile {
	files := make([]leaveFile, 6)
	for i := range files {
		files[i].staged = filepath.Join(transactionDir, fmt.Sprintf("%02d", i))
	}
	// Delete the commit marker last so any interrupted purge is recoverable.
	files[4], files[5] = files[5], files[4]
	return files
}

func rollbackLeaveTransaction(transactionDir string, files []leaveFile, cause error) error {
	for i := len(files) - 1; i >= 0; i-- {
		if _, err := os.Stat(files[i].staged); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return fmt.Errorf("leave rollback after %v: %w", cause, err)
		}
		if err := leaveRename(files[i].staged, files[i].original); err != nil {
			return fmt.Errorf("leave rollback after %v: %w", cause, err)
		}
	}
	if err := os.Remove(transactionDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("leave rollback after %v: %w", cause, err)
	}
	return cause
}

func purgeLeaveTransaction(transactionDir string, files []leaveFile) error {
	for _, file := range files {
		if err := leaveRemove(file.staged); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("leave cleanup remains recoverable in %q: %w", transactionDir, err)
		}
	}
	if err := leaveRemove(transactionDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("leave cleanup remains recoverable in %q: %w", transactionDir, err)
	}
	return nil
}

func resolveConfigPath(configDir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(configDir, path)
}

func containedPath(baseDir, candidate string) (string, error) {
	candidate, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(baseDir, candidate)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("managed identity path %q escapes base directory", candidate)
	}
	return candidate, nil
}
