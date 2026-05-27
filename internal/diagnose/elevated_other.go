//go:build !windows

package diagnose

import "os"

func elevated() bool {
	return os.Geteuid() == 0
}
