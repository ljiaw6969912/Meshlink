//go:build !linux && !windows

package main

import (
	"fmt"
	"os"
)

func platformPreflight() error  { return fmt.Errorf("mesh-coordinator requires Linux") }
func lockFile(_ *os.File) error { return fmt.Errorf("mesh-coordinator requires Linux") }
