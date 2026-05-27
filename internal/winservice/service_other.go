//go:build !windows

package winservice

import "fmt"

const DefaultName = "MeshlinkAgent"

type ServiceStatus struct {
	Installed bool   `json:"installed"`
	State     string `json:"state,omitempty"`
}

func Install(string, string) error { return unsupported() }
func Uninstall(string) error       { return unsupported() }
func Start(string) error           { return unsupported() }
func Stop(string) error            { return unsupported() }
func Run(string, string) error     { return unsupported() }
func Status(string) (ServiceStatus, error) {
	return ServiceStatus{}, unsupported()
}

func unsupported() error {
	return fmt.Errorf("Windows service management is only available on Windows")
}
