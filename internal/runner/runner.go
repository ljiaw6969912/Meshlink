package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"

	"meshlink/internal/agent"
	"meshlink/internal/config"
)

type Option func(*options)

type options struct {
	serviceName string
}

func WithServiceName(name string) Option {
	return func(opts *options) {
		opts.serviceName = name
	}
}

func Run(ctx context.Context, configPath string, logger *slog.Logger, opts ...Option) error {
	var settings options
	for _, opt := range opts {
		opt(&settings)
	}
	absConfigPath, err := filepath.Abs(configPath)
	if err == nil {
		configPath = absConfigPath
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	resolveConfigPaths(cfg, configPath)

	runner, err := agent.New(cfg, logger,
		agent.WithStatusPath(StatusPath(configPath, settings.serviceName)),
		agent.WithBaseDir(agent.BaseDirFromConfigPath(configPath)),
	)
	if err != nil {
		return fmt.Errorf("create agent: %w", err)
	}

	if err := runner.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("agent stopped: %w", err)
	}
	return nil
}

func StatusPath(configPath, serviceName string) string {
	if serviceName == "" {
		serviceName = "mesh-agent"
	}
	return filepath.Join(filepath.Dir(configPath), "logs", serviceName+".status.json")
}

func resolveConfigPaths(cfg *config.Config, configPath string) {
	cfg.CAFile = resolveConfigPath(cfg.CAFile, configPath)
	cfg.CertFile = resolveConfigPath(cfg.CertFile, configPath)
	cfg.KeyFile = resolveConfigPath(cfg.KeyFile, configPath)
}

func resolveConfigPath(path string, configPath string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}

	configDir := filepath.Dir(configPath)
	candidates := []string{
		filepath.Join(configDir, path),
		filepath.Join(filepath.Dir(configDir), path),
	}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, path),
			filepath.Join(filepath.Dir(exeDir), path),
		)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, path))
	}

	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return filepath.Join(configDir, path)
}

func ConsoleLogger(w io.Writer) *slog.Logger {
	log.SetOutput(w)
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}

func FileLogger(configPath, serviceName string) (*slog.Logger, io.Closer, error) {
	if serviceName == "" {
		serviceName = "mesh-agent"
	}
	dir := filepath.Join(filepath.Dir(configPath), "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, serviceName+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, err
	}
	return ConsoleLogger(file), file, nil
}
