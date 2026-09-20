package onboarding

import "testing"

func TestServerInvitesCannotBeCreatedByClientsOrForAnotherServer(t *testing.T) {
	m := testManager(t.TempDir())
	req := CreateInviteRequest{Server: "a.example:8443", LongLived: true, MaxUses: -1}
	if _, err := m.CreateServerInvite(req); err == nil {
		t.Fatal("empty client generated an unusable invite")
	}
	server, err := m.StartServerMode(StartServerRequest{ServerAddress: req.Server, ListenPort: 8443, LongLived: true, MaxUses: -1})
	if err != nil {
		t.Fatal(err)
	}
	if !m.IsOwnServerInvite(server.Invite.Link) {
		t.Fatal("server would enroll itself again")
	}
	if m.IsOwnServerInvite(buildInviteLink(req.Server, "tcp_tls_v1", "foreign-token")) {
		t.Fatal("foreign token was treated as this server's invitation")
	}
	if _, err := m.CreateServerInvite(req); err != nil {
		t.Fatal(err)
	}
	req.Server = "another.example:8443"
	if _, err := m.CreateServerInvite(req); err == nil {
		t.Fatal("generated credentials for another server")
	}
}
