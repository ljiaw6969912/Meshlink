package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestFixedReleaseServerCheckDownloadAndIsolation(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{}
	for _, relative := range append(append([]string{}, requiredUpdateFiles...), "bin/linux/mesh-agent", "bin/wintun.dll", "scripts/support-triage.ps1", "start-meshlink.bat") {
		files["meshlink/"+relative] = "fixture"
	}
	files["meshlink/VERSION"] = "0.2.0\n"
	document := packageManifest{Schema: PackageManifestSchema, Product: "Meshlink", Version: "0.2.0", Mode: DevelopmentMode, BuildTime: time.Now().UTC().Format(time.RFC3339)}
	keys := []string{}
	for key := range files {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		sum := sha256.Sum256([]byte(files[key]))
		document.Files = append(document.Files, packageFileInfo{Path: key[len("meshlink/"):], SHA256: hex.EncodeToString(sum[:]), Size: int64(len(files[key]))})
	}
	raw, _ := json.Marshal(document)
	files["meshlink/manifest.json"] = string(raw)
	archive := testReleaseZip(t, files)
	sum := sha256.Sum256(archive)
	manifest := Manifest{Schema: ReleaseManifestSchema, Product: "Meshlink", Version: "0.2.0", Mode: DevelopmentMode, GeneratedAt: time.Now().UTC(), Package: PackageInfo{File: "meshlink-0.2.0.zip", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(archive))}}
	raw, _ = json.Marshal(manifest)
	for name, data := range map[string][]byte{ManifestFile: raw, "meshlink-无配置.zip": archive, "private.key": []byte("secret")} {
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	handler, err := NewServerHandler(root)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	checked, err := Check(t.Context(), server.URL, "0.1.0")
	if err != nil || !checked.UpdateAvailable {
		t.Fatalf("check: %+v %v", checked, err)
	}
	if _, err := DownloadPackage(t.Context(), server.URL, checked.Manifest, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/private.key", "/meshlink/configs/client.json", "/meshlink/certs/client.key", "/meshlink/", "/meshlink-0.1.0.zip", "/meshlink-无配置.zip", "/../private.key", "/%2e%2e/private.key"} {
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Errorf("%s: %d", path, resp.StatusCode)
		}
	}
	for _, method := range []string{"POST", "PUT", "DELETE", "OPTIONS"} {
		req, _ := http.NewRequest(method, server.URL+"/manifest.json", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 405 {
			t.Errorf("%s: %d", method, resp.StatusCode)
		}
	}
	for _, path := range []string{"/manifest.json", "/healthz", "/" + manifest.Package.File} {
		resp, err := http.Head(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || len(body) != 0 {
			t.Errorf("HEAD %s: %d %q", path, resp.StatusCode, body)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "meshlink-无配置.zip"), []byte("publication in progress"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(t.Context(), server.URL, "0.1.0"); err == nil {
		t.Fatal("mismatched publication accepted")
	}
	if _, err := DownloadPackage(t.Context(), server.URL, manifest, t.TempDir()); err == nil {
		t.Fatal("mismatched download accepted")
	}
}
