//go:build windows

package main

import (
	"errors"
	"net/http"
	"testing"

	"meshlink/internal/config"
	"meshlink/internal/onboarding"
	meshupdate "meshlink/internal/update"
	"meshlink/internal/winservice"
)

func TestUpdateDefaultsFollowServerAndPreserveHTTPSPath(t *testing.T) {
	for _, original := range []string{"https://updates.example/files", "http://updates.example/files", "http://10.77.0.1:1263"} {
		host, port := splitUpdateBaseURL(original)
		got, err := updateBaseURLFromHostPort(host, port)
		if err != nil || got != original {
			t.Fatalf("custom address changed on reopen: %s => %s (%v)", original, got, err)
		}
	}
	for _, cfg := range []*config.Config{
		{Mode: "spoke", Connect: "office.example:3222"},
		{Mode: "hub", ServerPublicEndpoint: "office.example:3222"},
	} {
		got, err := serverUpdateURL(cfg)
		if err != nil || got != "https://office.example:3222/updates" {
			t.Fatalf("default %q: %v", got, err)
		}
		host, port := splitUpdateBaseURL(got)
		roundtrip, err := updateBaseURLFromHostPort(host, port)
		if err != nil || roundtrip != got {
			t.Fatalf("HTTPS/path lost by address fields: %s, %v", roundtrip, err)
		}
	}
	if _, err := serverUpdateURL(&config.Config{}); err == nil {
		t.Fatal("missing server accepted")
	}
}

func TestSavedUpdateOverrideDoesNotFollowUserToAnotherServer(t *testing.T) {
	base := t.TempDir()
	m := onboarding.Manager{BaseDir: base, LocalIPv4: func() (string, error) { return "127.0.0.1", nil }}
	started, err := m.StartServerMode(onboarding.StartServerRequest{ServerAddress: "127.0.0.1:3222", ListenPort: 3222, LongLived: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := rememberUpdateURL(base, "http://10.77.0.1:1263"); err != nil {
		t.Fatal(err)
	}
	if got := preferredUpdateURL(base, started.ConfigPath); got != "https://127.0.0.1:3222/updates" {
		t.Fatalf("legacy URL overrides current server: %s", got)
	}
	if err := rememberServerUpdateURL(base, started.ConfigPath, "https://custom.example:9443/files"); err != nil {
		t.Fatal(err)
	}
	if got := preferredUpdateURL(base, started.ConfigPath); got != "https://custom.example:9443/files" {
		t.Fatalf("explicit override lost: %s", got)
	}
	cfg, err := config.Load(started.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ServerPublicEndpoint = "new.example:5443"
	if err := config.Write(started.ConfigPath, *cfg); err != nil {
		t.Fatal(err)
	}
	if got := preferredUpdateURL(base, started.ConfigPath); got != "https://new.example:5443/updates" {
		t.Fatalf("another server reused old override: %s", got)
	}
	client, err := updateHTTPClient(started.ConfigPath, "https://custom.example:9443/files")
	if err != nil {
		t.Fatal(err)
	}
	if client.Transport.(*http.Transport).TLSClientConfig != nil {
		t.Fatal("network credentials sent to custom update server")
	}
	client, err = updateHTTPClient(started.ConfigPath, "https://new.example:5443/updates")
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig := client.Transport.(*http.Transport).TLSClientConfig
	if tlsConfig == nil || tlsConfig.RootCAs == nil || tlsConfig.InsecureSkipVerify || len(tlsConfig.Certificates) == 0 {
		t.Fatal("coordinator update lacks verified mutual TLS")
	}
}

func TestUpdateSuccessRequiresNewVersionAndRestoredServices(t *testing.T) {
	result := meshupdate.ApplyResult{Version: "0.1.11", Status: "success", Services: []string{"MeshlinkAgent"}}
	running := func(string) (winservice.ServiceStatus, error) {
		return winservice.ServiceStatus{Installed: true, State: "running"}, nil
	}
	if err := verifyApplyResult(result, "0.1.11", running); err != nil {
		t.Fatal(err)
	}
	if err := verifyApplyResult(result, "0.1.10", running); err == nil {
		t.Fatal("old desktop reports success")
	}
	stopped := func(string) (winservice.ServiceStatus, error) {
		return winservice.ServiceStatus{Installed: true, State: "stopped"}, nil
	}
	if err := verifyApplyResult(result, "0.1.11", stopped); err == nil {
		t.Fatal("stopped service reports success")
	}
	unreadable := func(string) (winservice.ServiceStatus, error) {
		return winservice.ServiceStatus{}, errors.New("SCM unavailable")
	}
	if err := verifyApplyResult(result, "0.1.11", unreadable); err == nil {
		t.Fatal("unknown service reports success")
	}
	result.Status = "failed"
	if err := verifyApplyResult(result, "0.1.11", running); err == nil {
		t.Fatal("rollback reports success")
	}
}
