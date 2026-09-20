package onboarding

import (
	"encoding/json"
	"meshlink/internal/p2p"
	"testing"
)

func TestSelfHostedLegacyPresenceCannotInventDirect(t *testing.T) {
	status := runtimeNodeConnectionStatus(p2p.ConnectionStatus{}, "online")
	b, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if status.PathType != "" || status.PathState != "idle" {
		t.Fatalf("unhandshaken self-hosted presence became direct: %s", b)
	}
}
