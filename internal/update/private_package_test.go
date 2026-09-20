package update

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestPrivateDeploymentPackageIsDeterministicAndContainsNoSecrets(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("private deployment packaging is verified by PowerShell on Windows")
	}
	if _, err := exec.LookPath("powershell"); err != nil {
		t.Skip("powershell is unavailable")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	for _, name := range []string{"mesh-cloudhub.exe", "mesh-agent.exe", "mesh-desktop.exe", "meshctl.exe"} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte("test build artifact: "+name+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out1 := filepath.Join(t.TempDir(), "first")
	out2 := filepath.Join(t.TempDir(), "second")
	for _, outDir := range []string{out1, out2} {
		cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File",
			filepath.Join(root, "scripts", "package-private.ps1"), "-BinDir", binDir, "-OutDir", outDir)
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("package-private.ps1 failed: %v\n%s", err, output)
		}
	}
	versionRaw, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	packageName := "meshlink-private-" + strings.TrimSpace(string(versionRaw))
	zip1 := filepath.Join(out1, packageName+".zip")
	zip2 := filepath.Join(out2, packageName+".zip")
	first, err := os.ReadFile(zip1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(zip2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("repeated package output differs: %s != %s", sha256Hex(first), sha256Hex(second))
	}

	r, err := zip.OpenReader(zip1)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	entries := make(map[string][]byte, len(r.File))
	for _, file := range r.File {
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		content := new(bytes.Buffer)
		if _, err := content.ReadFrom(reader); err != nil {
			_ = reader.Close()
			t.Fatal(err)
		}
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
		entries[filepath.ToSlash(file.Name)] = content.Bytes()
	}
	for _, relative := range []string{
		"bin/mesh-cloudhub.exe", "bin/mesh-agent.exe", "bin/mesh-desktop.exe", "bin/meshctl.exe",
		"configs/private-hub.example.env", "configs/trusted-license-keys.example.json",
		"scripts/run-private-hub.ps1", "docs/ops/private-deployment.zh-CN.md",
		"licenses/README.txt", "offline-updates/README.txt", "manifest.json", "VERSION",
	} {
		name := packageName + "/" + relative
		if _, ok := entries[name]; !ok {
			t.Errorf("package missing %s", name)
		}
	}

	var manifest struct {
		Schema  string `json:"schema"`
		Version string `json:"version"`
		Files   []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
			Size   int    `json:"size"`
		} `json:"files"`
	}
	if err := json.Unmarshal(entries[packageName+"/manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Schema != "meshlink-private-package-v1" || manifest.Version != strings.TrimSpace(string(versionRaw)) {
		t.Fatalf("manifest identity = %q/%q", manifest.Schema, manifest.Version)
	}
	paths := make([]string, 0, len(manifest.Files))
	for _, item := range manifest.Files {
		content, ok := entries[packageName+"/"+item.Path]
		if !ok {
			t.Errorf("manifest references missing file %s", item.Path)
			continue
		}
		if item.Size != len(content) || item.SHA256 != sha256Hex(content) {
			t.Errorf("manifest hash/size mismatch for %s", item.Path)
		}
		paths = append(paths, item.Path)
	}
	if !sort.StringsAreSorted(paths) {
		t.Fatal("manifest file paths are not sorted")
	}
	for name, content := range entries {
		lowerName := strings.ToLower(name)
		if strings.HasSuffix(lowerName, ".key") || strings.HasSuffix(lowerName, ".pem") ||
			strings.HasSuffix(lowerName, ".p12") || strings.HasSuffix(lowerName, ".pfx") ||
			strings.HasSuffix(lowerName, "/private-license.json") {
			t.Errorf("package contains forbidden credential file %s", name)
		}
		upper := strings.ToUpper(string(content))
		if strings.Contains(upper, "BEGIN PRIVATE KEY") || strings.Contains(upper, "BEGIN RSA PRIVATE KEY") ||
			strings.Contains(upper, "BEGIN OPENSSH PRIVATE KEY") {
			t.Errorf("package contains private key material in %s", name)
		}
	}
}

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
