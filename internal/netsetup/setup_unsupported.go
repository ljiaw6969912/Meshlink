//go:build !windows && !linux

package netsetup

import (
	"fmt"
	"log/slog"
	"runtime"
)

func applyPlatform(Plan, *slog.Logger) error {
	return fmt.Errorf("automatic network setup is not implemented on %s", runtime.GOOS)
}
