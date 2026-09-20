package networkstate

import "testing"

func TestPeerRuntimeKeepsHealthyDirectOnlineWithoutCoordinator(t *testing.T) {
	if got := PeerRuntime(Reconnecting, true); got != Connected {
		t.Fatalf("healthy direct = %s", got)
	}
	if got := PeerRuntime(Reconnecting, false); got != Reconnecting {
		t.Fatalf("waiting control = %s", got)
	}
}

func TestEffectiveStateBackfillsLegacyRuntimeStatus(t *testing.T) {
	tests := []struct {
		raw     State
		process string
		want    State
	}{
		{raw: Connected, process: "running", want: Connected},
		{raw: "", process: "running", want: Connected},
		{raw: "", process: "stopped", want: Disconnected},
		{raw: "unexpected", process: "running", want: Disconnected},
	}
	for _, tc := range tests {
		if got := Effective(tc.raw, tc.process); got != tc.want {
			t.Fatalf("Effective(%q, %q) = %q, want %q", tc.raw, tc.process, got, tc.want)
		}
	}
}

func TestStateContract(t *testing.T) {
	tests := []struct {
		state  State
		label  string
		online bool
	}{
		{state: NotJoined, label: "未加入"},
		{state: Disconnected, label: "已断开"},
		{state: Connecting, label: "连接中"},
		{state: Reconnecting, label: "重连中"},
		{state: Connected, label: "已连接", online: true},
	}
	for _, tc := range tests {
		if !tc.state.Valid() {
			t.Fatalf("state %q must be valid", tc.state)
		}
		if got := LabelCN(tc.state); got != tc.label {
			t.Fatalf("LabelCN(%q) = %q, want %q", tc.state, got, tc.label)
		}
		if got := IsOnline(tc.state); got != tc.online {
			t.Fatalf("IsOnline(%q) = %v, want %v", tc.state, got, tc.online)
		}
	}
	if State("unexpected").Valid() {
		t.Fatal("unexpected state must be invalid")
	}
	if got := LabelCN("unexpected"); got != "已断开" {
		t.Fatalf("LabelCN(unexpected) = %q, want 已断开", got)
	}
}
