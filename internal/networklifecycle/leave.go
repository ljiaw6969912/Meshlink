package networklifecycle

import (
	"strings"

	"meshlink/internal/winservice"
)

type NetworkLeaver interface {
	LeaveNetwork(serviceName string) error
}

type ServiceOps struct {
	Status    func(string) (winservice.ServiceStatus, error)
	Stop      func(string) error
	Uninstall func(string) error
}

func Leave(manager NetworkLeaver, serviceName string) error {
	return LeaveWithOps(manager, serviceName, ServiceOps{
		Status:    winservice.Status,
		Stop:      winservice.Stop,
		Uninstall: winservice.Uninstall,
	})
}

func LeaveWithOps(manager NetworkLeaver, serviceName string, ops ServiceOps) error {
	status, err := ops.Status(serviceName)
	if err != nil {
		return err
	}
	if status.Installed {
		if !strings.EqualFold(strings.TrimSpace(status.State), "stopped") {
			if err := ops.Stop(serviceName); err != nil {
				return err
			}
		}
		if err := ops.Uninstall(serviceName); err != nil {
			return err
		}
	}
	return manager.LeaveNetwork(serviceName)
}
