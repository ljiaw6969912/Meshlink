//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	meshupdate "meshlink/internal/update"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func runServiceAction(action, name, listen, releaseDir string) error {
	if name == "" {
		name = defaultServiceName
	}
	switch action {
	case "install":
		return installService(name, listen, releaseDir)
	case "uninstall":
		return deleteService(name)
	case "start":
		return startService(name)
	case "stop":
		return stopService(name)
	case "run":
		return svc.Run(name, updateServiceHandler{name: name, listen: listen, releaseDir: releaseDir})
	default:
		return fmt.Errorf("unknown service action %q", action)
	}
}

func installService(name, listen, releaseDir string) error {
	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	exePath, err = filepath.Abs(exePath)
	if err != nil {
		return err
	}
	releaseDir, err = filepath.Abs(releaseDir)
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
		DisplayName:      "Meshlink 更新服务",
		Description:      "Serves Meshlink release packages over the private mesh",
		StartType:        mgr.StartAutomatic,
		ServiceStartName: "LocalSystem",
	}, "-service", "run", "-service-name", name, "-listen", listen, "-dir", releaseDir)
	if err != nil {
		return err
	}
	defer s.Close()
	return nil
}

func deleteService(name string) error {
	m, s, err := openService(name)
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	return s.Delete()
}

func startService(name string) error {
	m, s, err := openService(name)
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	return s.Start()
}

func stopService(name string) error {
	m, s, err := openService(name)
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

func openService(name string) (*mgr.Mgr, *mgr.Service, error) {
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

type updateServiceHandler struct {
	name       string
	listen     string
	releaseDir string
}

func (h updateServiceHandler) Execute(_ []string, changes <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger, closer := serviceLogger(h.releaseDir, h.name)
	if closer != nil {
		defer closer.Close()
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- meshupdate.RunServer(ctx, h.listen, h.releaseDir, logger)
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
					if err != nil && !errors.Is(err, context.Canceled) {
						logger.Error("update server stopped with error", "err", err)
					}
				case <-time.After(10 * time.Second):
					logger.Error("timed out waiting for update server shutdown")
				}
				status <- svc.Status{State: svc.Stopped}
				return false, 0
			default:
				logger.Warn("unsupported service command", "cmd", change.Cmd)
			}
		case err := <-errCh:
			if err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("update server stopped with error", "err", err)
				status <- svc.Status{State: svc.Stopped}
				return false, 1
			}
			status <- svc.Status{State: svc.Stopped}
			return false, 0
		}
	}
}

func serviceLogger(releaseDir, serviceName string) (*slog.Logger, *os.File) {
	if serviceName == "" {
		serviceName = defaultServiceName
	}
	if err := os.MkdirAll(releaseDir, 0o700); err != nil {
		return slog.New(slog.NewTextHandler(os.Stdout, nil)), nil
	}
	file, err := os.OpenFile(filepath.Join(releaseDir, serviceName+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return slog.New(slog.NewTextHandler(os.Stdout, nil)), nil
	}
	return slog.New(slog.NewTextHandler(file, &slog.HandlerOptions{Level: slog.LevelInfo})), file
}
