package onboarding

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"meshlink/internal/config"
)

func installedSpokeForImport(t *testing.T) (Manager, string, CreateInviteResult, map[string][]byte) {
	t.Helper()
	hub := testManager(t.TempDir())
	if _, err := hub.CreateHub(CreateHubRequest{NodeName: "hub", ListenPort: 8443}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(hub.EnrollHTTPHandler())
	defer server.Close()
	invite, err := hub.CreateInvite(CreateInviteRequest{Server: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	source := testManager(filepath.Join(t.TempDir(), "旧程序 目录"))
	source.HTTPClient = server.Client()
	result, err := source.JoinSpoke(JoinSpokeRequest{InviteLink: invite.Link, Code: invite.Code, NodeName: "original-device"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(result.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(source.configsDir(), "installed-service.json")
	if err := os.Rename(result.ConfigPath, path); err != nil {
		t.Fatal(err)
	}
	snapshot := map[string][]byte{}
	for _, p := range []string{path, resolveConfigPath(source.configsDir(), cfg.CAFile), resolveConfigPath(source.configsDir(), cfg.CertFile), resolveConfigPath(source.configsDir(), cfg.KeyFile), hub.deviceRegistryPath(), hub.inviteStorePath()} {
		snapshot[p], err = os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
	}
	return source, path, invite, snapshot
}

func assertImportedSourceUnchanged(t *testing.T, snapshot map[string][]byte) {
	t.Helper()
	for path, before := range snapshot {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("import modified source or enrollment state %s: %v", path, err)
		}
	}
}

func TestImportInstalledSpokeResumesConsumedInviteWithoutEnrollment(t *testing.T) {
	_, path, invite, snapshot := installedSpokeForImport(t)
	source, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, link := range []string{invite.Link, ""} {
		destination := testManager(filepath.Join(t.TempDir(), "新程序 目录"))
		if err := os.MkdirAll(destination.certsDir(), 0700); err != nil {
			t.Fatal(err)
		}
		orphan := filepath.Join(destination.certsDir(), "original-device-key.pem")
		if err := os.WriteFile(orphan, []byte("existing unrelated key"), 0600); err != nil {
			t.Fatal(err)
		}
		imported, err := destination.ImportInstalledSpoke(path, link)
		if err != nil || !imported {
			t.Fatalf("import: imported=%t err=%v", imported, err)
		}
		cfg, err := config.Load(destination.activeConfigPath())
		if err != nil {
			t.Fatal(err)
		}
		if cfg.NodeID != source.NodeID || cfg.DisplayName != source.DisplayName || cfg.VirtualIP != source.VirtualIP || cfg.Connect != source.Connect {
			t.Fatalf("import changed device identity: %+v", cfg)
		}
		for _, pair := range [][2]string{{source.CAFile, cfg.CAFile}, {source.CertFile, cfg.CertFile}, {source.KeyFile, cfg.KeyFile}} {
			original, err := os.ReadFile(resolveConfigPath(filepath.Dir(path), pair[0]))
			if err != nil {
				t.Fatal(err)
			}
			copied, err := os.ReadFile(resolveConfigPath(destination.configsDir(), pair[1]))
			if err != nil || !bytes.Equal(original, copied) {
				t.Fatalf("certificate material changed: %v", err)
			}
			if filepath.IsAbs(pair[1]) {
				t.Fatal("import retained an absolute certificate path")
			}
		}
		// The real enrollment server is closed and its one-use invitation spent.
		resumed, err := destination.JoinSpoke(JoinSpokeRequest{InviteLink: link, Code: invite.Code})
		if err != nil || resumed.VirtualIP != source.VirtualIP {
			t.Fatalf("imported identity enrolled again: %+v %v", resumed, err)
		}
		bytes, err := os.ReadFile(orphan)
		if err != nil || string(bytes) != "existing unrelated key" {
			t.Fatal("import overwrote an unrelated existing key")
		}
	}
	assertImportedSourceUnchanged(t, snapshot)
}

func TestImportInstalledSpokeSkipsExistingTargetMissingSourceHubAndOtherNetwork(t *testing.T) {
	_, source, _, snapshot := installedSpokeForImport(t)
	for _, kind := range []string{"existing-target", "missing-source", "hub", "other-network"} {
		t.Run(kind, func(t *testing.T) {
			destination := testManager(t.TempDir())
			path, link := source, ""
			switch kind {
			case "existing-target":
				if err := os.MkdirAll(destination.configsDir(), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(destination.activeConfigPath(), []byte("preserve even invalid target"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-source":
				path = filepath.Join(t.TempDir(), "missing.json")
			case "hub":
				hub := testManager(t.TempDir())
				created, err := hub.CreateHub(CreateHubRequest{NodeName: "foreign-hub", ListenPort: 8443})
				if err != nil {
					t.Fatal(err)
				}
				path = created.ConfigPath
			case "other-network":
				link = buildInviteLink("other.example.com:8443", "tcp_tls_v1", "unused-token")
			}
			imported, err := destination.ImportInstalledSpoke(path, link)
			if err != nil || imported {
				t.Fatalf("unsafe import %s: %t %v", kind, imported, err)
			}
			if kind == "existing-target" {
				data, err := os.ReadFile(destination.activeConfigPath())
				if err != nil || string(data) != "preserve even invalid target" {
					t.Fatal("replaced existing target")
				}
			} else if _, err := os.Stat(destination.activeConfigPath()); !os.IsNotExist(err) {
				t.Fatal("skipped import published active config")
			}
			if _, err := os.Stat(destination.certsDir()); !os.IsNotExist(err) {
				t.Fatal("skipped import copied certificate material")
			}
		})
	}
	assertImportedSourceUnchanged(t, snapshot)
}

func TestImportInstalledSpokeRejectsBrokenIdentityWithoutChangingSource(t *testing.T) {
	for _, kind := range []string{"bad-ca", "bad-key", "wrong-node-id"} {
		t.Run(kind, func(t *testing.T) {
			_, path, _, _ := installedSpokeForImport(t)
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "bad-ca":
				err = os.WriteFile(resolveConfigPath(filepath.Dir(path), cfg.CAFile), []byte("invalid CA"), 0600)
			case "bad-key":
				err = os.WriteFile(resolveConfigPath(filepath.Dir(path), cfg.KeyFile), []byte("invalid key"), 0600)
			case "wrong-node-id":
				cfg.NodeID = "different-node"
				err = config.Write(path, *cfg)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := map[string][]byte{}
			for _, p := range []string{path, resolveConfigPath(filepath.Dir(path), cfg.CAFile), resolveConfigPath(filepath.Dir(path), cfg.CertFile), resolveConfigPath(filepath.Dir(path), cfg.KeyFile)} {
				before[p], err = os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
			}
			destination := testManager(t.TempDir())
			imported, err := destination.ImportInstalledSpoke(path, "")
			if err == nil || imported {
				t.Fatalf("accepted broken identity %s", kind)
			}
			if _, err := os.Stat(destination.activeConfigPath()); !os.IsNotExist(err) {
				t.Fatal("bad identity published a config")
			}
			assertImportedSourceUnchanged(t, before)
		})
	}
}

func TestConcurrentInstalledSpokeImportsPublishOnceWithoutOrphanIdentityCopies(t *testing.T) {
	_, path, _, snapshot := installedSpokeForImport(t)
	destination := testManager(t.TempDir())
	var workers sync.WaitGroup
	results := make(chan bool, 6)
	failures := make(chan error, 6)
	for i := 0; i < 6; i++ {
		workers.Go(func() {
			imported, err := destination.ImportInstalledSpoke(path, "")
			results <- imported
			failures <- err
		})
	}
	workers.Wait()
	close(results)
	close(failures)
	winners := 0
	for imported := range results {
		if imported {
			winners++
		}
	}
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("published %d active identities", winners)
	}
	entries, err := os.ReadDir(destination.certsDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		t.Fatalf("failed imports left identity files: %+v", entries)
	}
	entries, err = os.ReadDir(filepath.Join(destination.certsDir(), entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("import retained staging files: %+v", entries)
	}
	assertImportedSourceUnchanged(t, snapshot)
}
