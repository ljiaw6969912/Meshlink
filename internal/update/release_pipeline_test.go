package update

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckRejectsInvalidReleaseManifestMetadata(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		sha256  string
		mode    string
		signed  bool
		cert    string
		wantErr string
	}{
		{name: "unexpected package name", file: "payload.zip", sha256: strings.Repeat("a", 64), mode: "development", wantErr: "package file"},
		{name: "missing sha256", file: "meshlink-0.2.0.zip", mode: "development", wantErr: "sha256"},
		{name: "malformed sha256", file: "meshlink-0.2.0.zip", sha256: "abc", mode: "development", wantErr: "sha256"},
		{name: "unsigned release", file: "meshlink-0.2.0.zip", sha256: strings.Repeat("a", 64), mode: "release", wantErr: "signed"},
		{name: "release without certificate identity", file: "meshlink-0.2.0.zip", sha256: strings.Repeat("a", 64), mode: "release", signed: true, wantErr: "certificate"},
		{name: "development falsely marked signed", file: "meshlink-0.2.0.zip", sha256: strings.Repeat("a", 64), mode: "development", signed: true, cert: strings.Repeat("A", 40), wantErr: "unsigned"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `{
  "schema": "meshlink-release-v1",
  "product": "Meshlink",
  "version": "0.2.0",
  "mode": %q,
  "generated_at": "2026-07-15T00:00:00Z",
  "package": {"file": %q, "sha256": %q, "size": 123},
  "signing": {"code_signed": %t, "certificate_thumbprint": %q}
}`, tc.mode, tc.file, tc.sha256, tc.signed, tc.cert)
			}))
			defer server.Close()

			_, err := Check(t.Context(), server.URL, "0.1.5")
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.wantErr)) {
				t.Fatalf("Check error = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestDownloadRejectsIncompletePackageWithoutReplacingKnownGood(t *testing.T) {
	packageBytes := testReleaseZip(t, map[string]string{
		"meshlink/VERSION":   "0.2.0\n",
		"meshlink/README.md": "not an installable update\n",
	})
	sum := sha256.Sum256(packageBytes)
	manifest := Manifest{
		Product: "Meshlink", Version: "0.2.0", GeneratedAt: time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC),
		Package: PackageInfo{File: "meshlink-0.2.0.zip", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(packageBytes))},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(packageBytes)
	}))
	defer server.Close()

	destDir := t.TempDir()
	knownGoodPath := filepath.Join(destDir, manifest.Package.File)
	knownGood := []byte("known-good-package")
	if err := os.WriteFile(knownGoodPath, knownGood, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := DownloadPackage(context.Background(), server.URL, manifest, destDir)
	if err == nil {
		t.Fatalf("DownloadPackage = %q, want required package content error", got)
	}
	after, readErr := os.ReadFile(knownGoodPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(knownGood) {
		t.Fatalf("failed download replaced known-good package: got %q", after)
	}
}

func TestDownloadRejectsBackslashZipEntries(t *testing.T) {
	files := map[string]string{"meshlink\\VERSION": "0.2.0\n"}
	for _, relative := range requiredUpdateFiles[1:] {
		files["meshlink\\"+strings.ReplaceAll(relative, "/", "\\")] = "test artifact\n"
	}
	packageBytes := testReleaseZip(t, files)
	sum := sha256.Sum256(packageBytes)
	manifest := Manifest{
		Product: "Meshlink", Version: "0.2.0", GeneratedAt: time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC),
		Package: PackageInfo{File: "meshlink-0.2.0.zip", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(packageBytes))},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(packageBytes)
	}))
	defer server.Close()

	if _, err := DownloadPackage(context.Background(), server.URL, manifest, t.TempDir()); err == nil || !strings.Contains(strings.ToLower(err.Error()), "unsafe") {
		t.Fatalf("DownloadPackage error = %v, want unsafe ZIP entry rejection", err)
	}
}

func TestApplyScriptValidatesPackageSignatureAndRestoresLastKnownGood(t *testing.T) {
	path, err := WriteApplyScript(ApplyOptions{
		BaseDir: t.TempDir(), PackagePath: filepath.Join(t.TempDir(), "meshlink-0.2.0.zip"),
		Version: "0.2.0", ServiceName: "MeshlinkAgentTask11CTest",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	for _, required := range []string{
		"manifest.json", "Get-FileHash", "Get-AuthenticodeSignature", "last-known-good", "Restore-LastKnownGood", "VERSION",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("apply script missing %q gate", required)
		}
	}
	failureStart := strings.Index(script, "update failed:")
	if failureStart < 0 {
		t.Fatal("apply script missing failure handler")
	}
	failureEnd := strings.Index(script[failureStart:], "exit 1")
	if failureEnd < 0 {
		t.Fatal("apply script missing failure handler")
	}
	failureHandler := script[failureStart : failureStart+failureEnd]
	stopService := strings.Index(failureHandler, "Stop-Service")
	restore := strings.Index(failureHandler, "Restore-LastKnownGood")
	if stopService < 0 || restore < 0 || stopService > restore {
		t.Fatal("apply failure handler must stop the service before restoring last-known-good")
	}
}

func testReleaseZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	temp := filepath.Join(t.TempDir(), "package.zip")
	f, err := os.Create(temp)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, content := range files {
		entry, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(temp)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
