//go:build windows

package winservice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"meshlink/internal/runner"
)

const DefaultName = "MeshlinkAgent"

type ServiceStatus struct {
	Installed bool   `json:"installed"`
	State     string `json:"state,omitempty"`
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
		existing.Close()
		return fmt.Errorf("service %q already exists", name)
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
	return nil
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
	return ServiceStatus{Installed: true, State: stateName(status.State)}, nil
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
