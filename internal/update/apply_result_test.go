package update

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyResultBOMAndConsumption(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "updates"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "updates", "update-result.json")
	data := "\xef\xbb\xbf" + `{"version":"0.2.0","status":"success","message":"installed","completed_at":"2026-09-21T00:00:00Z","services":["MeshlinkAgent"]}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := ReadApplyResult(base)
	if err != nil || result.Status != "success" || len(result.Services) != 1 {
		t.Fatalf("ReadApplyResult: %+v %v", result, err)
	}
	if _, err := ConsumeApplyResult(base); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadApplyResult(base); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("consumed result remains: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"status":"success"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ConsumeApplyResult(base); err == nil {
		t.Fatal("accepted incomplete result")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("invalid result should remain for diagnosis")
	}
}
