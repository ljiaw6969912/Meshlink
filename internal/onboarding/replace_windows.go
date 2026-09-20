//go:build windows

package onboarding

import (
	"errors"
	"os"
	"syscall"
	"time"
)

func replaceJSONFile(from, to string) error {
	// Go's Windows readers don't request FILE_SHARE_DELETE. A complete reader
	// therefore briefly prevents replacement; leave the old generation intact
	// and retry the rename instead of truncating or deleting the destination.
	return retryJSONSharing(func() error { return os.Rename(from, to) })
}

func readJSONFile(path string) ([]byte, error) {
	var data []byte
	err := retryJSONSharing(func() error { var err error; data, err = os.ReadFile(path); return err })
	return data, err
}

func retryJSONSharing(operation func() error) error {
	deadline := time.Now().Add(time.Second)
	for {
		err := operation()
		if err == nil || (!errors.Is(err, syscall.ERROR_ACCESS_DENIED) && !errors.Is(err, syscall.Errno(32))) || !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(5 * time.Millisecond)
	}
}
