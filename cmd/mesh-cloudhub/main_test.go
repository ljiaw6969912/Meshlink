package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"meshlink/internal/cloudhub"
)

func TestDefaultListenIsLocalOnly(t *testing.T) {
	if defaultListen != "127.0.0.1:18080" {
		t.Fatalf("defaultListen = %q, want 127.0.0.1:18080", defaultListen)
	}
	host, _, err := net.SplitHostPort(defaultListen)
	if err != nil {
		t.Fatalf("defaultListen is not host:port: %v", err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("default listen host = %q, want loopback", host)
	}
}

func TestDefaultRelayListenIsDisabled(t *testing.T) {
	if defaultRelayListen != "" {
		t.Fatalf("defaultRelayListen = %q, want disabled empty address", defaultRelayListen)
	}
}

func TestLoadPrivateLicenseManagerRequiresCompleteControlledConfig(t *testing.T) {
	if _, err := loadPrivateLicenseManager(privateLicenseFlags{DeploymentID: "dep-only"}); err == nil {
		t.Fatal("partial private license config succeeded")
	}
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	keysPath := filepath.Join(dir, "trusted-keys.json")
	keysRaw, err := json.Marshal(map[string]any{"keys": []map[string]string{{"key_id": "license-2026", "public_key": base64.RawURLEncoding.EncodeToString(publicKey)}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keysPath, keysRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := loadPrivateLicenseManager(privateLicenseFlags{
		DeploymentID: "deployment-private", OrganizationID: "org-private",
		LicenseFile: filepath.Join(dir, "private-license.json"), TrustedKeysFile: keysPath,
	})
	if err != nil || manager == nil {
		t.Fatalf("loadPrivateLicenseManager = %v, %v", manager, err)
	}
}

func TestNewHandlerServesHealthz(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	newHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz status = %d, want 200", rec.Code)
	}
}

func TestNewHandlerReportsRelayStatusWithoutSecrets(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	service := cloudhub.NewService(cloudhub.NewMemoryStore())
	newHandlerForService(service,
		cloudhub.WithRelayStatusProvider(func() cloudhub.RelayStatus {
			return cloudhub.RelayStatus{Enabled: true, Listen: "127.0.0.1:18082"}
		}),
	).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz status = %d, want 200", rec.Code)
	}
	var body struct {
		OK    bool                 `json:"ok"`
		Relay cloudhub.RelayStatus `json:"relay"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if !body.OK || !body.Relay.Enabled || body.Relay.Listen != "127.0.0.1:18082" {
		t.Fatalf("health body = %+v, want relay status", body)
	}
	if rec.Body.String() == "" || containsAny(rec.Body.String(), []string{"token", "code", "private_key"}) {
		t.Fatalf("health response leaks sensitive field: %s", rec.Body.String())
	}
}

func containsAny(s string, needles []string) bool {
	for _, needle := range needles {
		if needle != "" && strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
