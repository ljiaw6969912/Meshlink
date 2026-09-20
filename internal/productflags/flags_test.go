package productflags

import "testing"

func TestOfficialHubMVPDefaultsOffAndRequiresExplicitTrue(t *testing.T) {
	t.Setenv(OfficialHubMVPEnv, "")
	if OfficialHubMVPEnabled() {
		t.Fatal("official Hub must default off")
	}
	for _, value := range []string{"1", "true", "TRUE"} {
		t.Setenv(OfficialHubMVPEnv, value)
		if !OfficialHubMVPEnabled() {
			t.Fatalf("value %q must enable MVP", value)
		}
	}
}
