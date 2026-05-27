//go:build !windows

package rdp

import "fmt"

func Open(string) error {
	return fmt.Errorf("RDP launcher is only implemented on Windows")
}
