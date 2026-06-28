//go:build !windows

package main

import "fmt"

func runServiceAction(action, name, listen, releaseDir string) error {
	return fmt.Errorf("service action %q is only supported on Windows", action)
}
