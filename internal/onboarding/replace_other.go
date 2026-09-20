//go:build !windows

package onboarding

import "os"

func replaceJSONFile(from, to string) error    { return os.Rename(from, to) }
func readJSONFile(path string) ([]byte, error) { return os.ReadFile(path) }
