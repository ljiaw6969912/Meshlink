//go:build windows

package winservice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"meshlink/internal/runner"
)

const DefaultName = "MeshlinkAgent"

type ServiceStatus struct {
	Installed  bool   `json:"installed"`
	State      string `json:"state,omitempty"`
	ConfigPath string `json:"config_path,omitempty"`
}

func Install(name, configPath string) error {
	if name == "" {
		name = DefaultName
	}
	if configPath == "" {
		return errors.New("config path is required")
	}
	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	exePath, err = filepath.Abs(exePath)
	if err != nil {
		return err
	}
	configPath, err = filepath.Abs(configPath)
	if err != nil {
		return err
	}

	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	if existing, err := m.OpenService(name); err == nil {
		defer existing.Close()
		cfg, err := existing.Config()
		if err != nil {
			return err
		}
		cfg.DisplayName = "Meshlink Agent"
		cfg.Description = "Self-hosted TCP/TLS mesh access agent"
		cfg.StartType = mgr.StartAutomatic
		cfg.ServiceStartName = "LocalSystem"
		cfg.Password = ""
		cfg.BinaryPathName = serviceBinaryPath(exePath, name, configPath)
		if err := existing.UpdateConfig(cfg); err != nil {
			return err
		}
		return configureRecovery(existing)
	}

	s, err := m.CreateService(name, exePath, mgr.Config{
		DisplayName:      "Meshlink Agent",
		Description:      "Self-hosted TCP/TLS mesh access agent",
		StartType:        mgr.StartAutomatic,
		ServiceStartName: "LocalSystem",
	}, "-service", "run", "-service-name", name, "-config", configPath)
	if err != nil {
		return err
	}
	defer s.Close()
	return configureRecovery(s)
}

func serviceBinaryPath(exePath, name, configPath string) string {
	return syscall.EscapeArg(exePath) +
		" -service run -service-name " + syscall.EscapeArg(name) +
		" -config " + syscall.EscapeArg(configPath)
}

func Uninstall(name string) error {
	if name == "" {
		name = DefaultName
	}
	m, s, err := open(name)
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	return s.Delete()
}

func Start(name string) error {
	if name == "" {
		name = DefaultName
	}
	m, s, err := open(name)
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	_ = configureRecovery(s)
	return s.Start()
}

func Stop(name string) error {
	if name == "" {
		name = DefaultName
	}
	m, s, err := open(name)
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	status, err := s.Control(svc.Stop)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(20 * time.Second)
	for status.State != svc.Stopped {
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for service to stop")
		}
		time.Sleep(300 * time.Millisecond)
		status, err = s.Query()
		if err != nil {
			return err
		}
	}
	return nil
}

func Status(name string) (ServiceStatus, error) {
	if name == "" {
		name = DefaultName
	}
	m, s, err := open(name)
	if err != nil {
		return ServiceStatus{Installed: false}, nil
	}
	defer m.Disconnect()
	defer s.Close()
	status, err := s.Query()
	if err != nil {
		return ServiceStatus{}, err
	}
	cfg, _ := s.Config()
	return ServiceStatus{Installed: true, State: stateName(status.State), ConfigPath: configPathFromBinaryPath(cfg.BinaryPathName)}, nil
}

func Run(name, configPath string) error {
	if name == "" {
		name = DefaultName
	}
	return svc.Run(name, serviceHandler{
		name:       name,
		configPath: configPath,
	})
}

type serviceHandler struct {
	name       string
	configPath string
}

func (h serviceHandler) Execute(_ []string, changes <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger, closer, err := runner.FileLogger(h.configPath, h.name)
	if err != nil {
		logger = runner.ConsoleLogger(os.Stdout)
	} else {
		defer closer.Close()
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- runner.Run(ctx, h.configPath, logger, runner.WithServiceName(h.name))
	}()

	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case change := <-changes:
			switch change.Cmd {
			case svc.Interrogate:
				status <- change.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case err := <-errCh:
					if err != nil {
						logger.Error("agent stopped with error", "err", err)
					}
				case <-time.After(20 * time.Second):
					logger.Error("timed out waiting for agent shutdown")
				}
				status <- svc.Status{State: svc.Stopped}
				return false, 0
			default:
				logger.Warn("unsupported service command", "cmd", change.Cmd)
			}
		case err := <-errCh:
			if err != nil {
				logger.Error("agent stopped with error", "err", err)
				status <- svc.Status{State: svc.Stopped}
				return false, 1
			}
			status <- svc.Status{State: svc.Stopped}
			return false, 0
		}
	}
}

func open(name string) (*mgr.Mgr, *mgr.Service, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, nil, err
	}
	s, err := m.OpenService(name)
	if err != nil {
		m.Disconnect()
		return nil, nil, err
	}
	return m, s, nil
}

func configureRecovery(s *mgr.Service) error {
	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}
	if err := s.SetRecoveryActions(actions, 24*60*60); err != nil {
		return err
	}
	return s.SetRecoveryActionsOnNonCrashFailures(true)
}

func configPathFromBinaryPath(binaryPath string) string {
	args, err := windows.DecomposeCommandLine(binaryPath)
	if err != nil {
		return ""
	}
	for i := 0; i < len(args)-1; i++ {
		if strings.EqualFold(args[i], "-config") {
			return strings.TrimSpace(args[i+1])
		}
	}
	return ""
}

func stateName(state svc.State) string {
	switch state {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "start pending"
	case svc.StopPending:
		return "stop pending"
	case svc.Running:
		return "running"
	case svc.ContinuePending:
		return "continue pending"
	case svc.PausePending:
		return "pause pending"
	case svc.Paused:
		return "paused"
	default:
		return fmt.Sprintf("unknown(%d)", state)
	}
}
