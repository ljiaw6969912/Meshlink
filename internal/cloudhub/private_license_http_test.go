package cloudhub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"meshlink/internal/licensing"
)

func TestPrivateDeploymentLoopbackImportJoinAndExpiryClosure(t *testing.T) {
	f := newPrivateLicenseFixture(t, licensing.ExpiryPolicyContinueExisting, "", nil)
	server := httptest.NewServer(NewServer(f.svc))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1" {
		t.Fatalf("test server is not loopback-only: %s", server.URL)
	}
	client := &Client{BaseURL: server.URL, HTTPClient: server.Client(), ActorAccountID: f.owner.ID}

	payload := f.payload(licensing.ExpiryPolicyContinueExisting, "")
	payload.LicenseID = "lic-loopback-001"
	raw, err := licensing.Sign(payload, f.priv)
	if err != nil {
		t.Fatal(err)
	}
	if summary, err := client.ImportPrivateLicense(context.Background(), ImportPrivateLicenseRequest{OrganizationID: f.org.ID, SignedLicense: raw}); err != nil || summary.LicenseID != payload.LicenseID {
		t.Fatalf("loopback license import = %+v, %v", summary, err)
	}
	firstInvite, err := client.CreateInvite(context.Background(), CreateInviteRequest{AccountID: f.owner.ID, NetworkID: f.network.ID, OneTime: true, MaxUses: 1, TTL: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	newInvite, err := client.CreateInvite(context.Background(), CreateInviteRequest{AccountID: f.owner.ID, NetworkID: f.network.ID, OneTime: true, MaxUses: 1, TTL: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	existing, err := client.JoinDevice(context.Background(), JoinDeviceRequest{Token: firstInvite.Token, Code: firstInvite.Code, DeviceName: "existing-loopback", Fingerprint: "fp-loopback-existing"})
	if err != nil {
		t.Fatal(err)
	}
	*f.now = f.expires
	if _, err := client.HeartbeatDevice(context.Background(), HeartbeatDeviceRequest{AccountID: f.owner.ID, NetworkID: f.network.ID, DeviceID: existing.ID, Status: DeviceStatusOnline}); err != nil {
		t.Fatalf("existing heartbeat after continue_existing expiry = %v", err)
	}
	if _, err := client.JoinDevice(context.Background(), JoinDeviceRequest{Token: newInvite.Token, Code: newInvite.Code, DeviceName: "new-loopback", Fingerprint: "fp-loopback-new"}); err == nil || !strings.Contains(err.Error(), licensing.ErrPolicyDenied.Error()) {
		t.Fatalf("new device after expiry error = %v", err)
	}
}

func TestPrivateLicenseHTTPClientClosureAndRedaction(t *testing.T) {
	f := newPrivateLicenseFixture(t, licensing.ExpiryPolicyContinueExisting, "", nil)
	server := httptest.NewServer(NewServer(f.svc))
	defer server.Close()
	ownerClient := &Client{BaseURL: server.URL, HTTPClient: server.Client(), ActorAccountID: f.owner.ID}

	summary, err := ownerClient.GetPrivateLicenseSummary(context.Background(), f.org.ID)
	if err != nil || summary.LicenseID != "lic-private-001" {
		t.Fatalf("GetPrivateLicenseSummary = %+v, %v", summary, err)
	}
	payload := f.payload(licensing.ExpiryPolicyContinueExisting, "")
	payload.LicenseID = "lic-private-002"
	raw, err := licensing.Sign(payload, f.priv)
	if err != nil {
		t.Fatal(err)
	}
	imported, err := ownerClient.ImportPrivateLicense(context.Background(), ImportPrivateLicenseRequest{OrganizationID: f.org.ID, SignedLicense: raw})
	if err != nil || imported.LicenseID != payload.LicenseID {
		t.Fatalf("ImportPrivateLicense = %+v, %v", imported, err)
	}

	req, err := http.NewRequest(http.MethodGet, server.URL+"/api/organizations/"+f.org.ID+"/private-license", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(ActorAccountHeader, f.owner.ID)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", resp.StatusCode, body)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Contains(text, "signature") || strings.Contains(text, "signed_license") || strings.Contains(text, "payload") || strings.Contains(text, "Example Customer") {
		t.Fatalf("HTTP summary leaked signed material: %s", text)
	}

	operator := f.createMember(MembershipRoleOperator)
	operatorClient := &Client{BaseURL: server.URL, HTTPClient: server.Client(), ActorAccountID: operator.ID}
	if _, err := operatorClient.GetPrivateLicenseSummary(context.Background(), f.org.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("operator client error = %v, want forbidden", err)
	}
}
