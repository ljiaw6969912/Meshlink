package update

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ApplyResult is written only after installation and service restoration finish.
type ApplyResult struct {
	Version     string    `json:"version"`
	Status      string    `json:"status"`
	Message     string    `json:"message"`
	CompletedAt time.Time `json:"completed_at"`
	Services    []string  `json:"services"`
}

func ReadApplyResult(baseDir string) (ApplyResult, error) {
	var result ApplyResult
	raw, err := os.ReadFile(filepath.Join(baseDir, "updates", "update-result.json"))
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf}), &result)
	if err != nil {
		return result, err
	}
	if (result.Status != "success" && result.Status != "failed") || result.Version == "" || result.CompletedAt.IsZero() {
		return ApplyResult{}, fmt.Errorf("invalid update result")
	}
	return result, nil
}

// ConsumeApplyResult removes a valid result after reading it. Call only after the
// desktop has checked the installed version and the named services.
func ConsumeApplyResult(baseDir string) (ApplyResult, error) {
	result, err := ReadApplyResult(baseDir)
	if err != nil {
		return result, err
	}
	return result, os.Remove(filepath.Join(baseDir, "updates", "update-result.json"))
}
