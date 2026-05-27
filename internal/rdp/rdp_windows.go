//go:build windows

package rdp

import (
	"errors"
	"os/exec"
	"strings"
)

func Open(target string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return errors.New("RDP target is required")
	}
	return exec.Command("mstsc.exe", "/v:"+target).Start()
}
