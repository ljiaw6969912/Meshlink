package proto

import "testing"

func TestInfrastructureModes(t *testing.T) {
	for _, mode := range []string{"hub", "relay", "control", "HUB"} {
		if !IsInfrastructureMode(mode) {
			t.Fatalf("mode %q must be infrastructure", mode)
		}
	}
	if IsInfrastructureMode("spoke") {
		t.Fatal("spoke must remain a user device")
	}
}
