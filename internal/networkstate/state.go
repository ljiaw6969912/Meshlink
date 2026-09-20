package networkstate

import "strings"

type State string

const (
	NotJoined    State = "not_joined"
	Disconnected State = "disconnected"
	Connecting   State = "connecting"
	Reconnecting State = "reconnecting"
	Connected    State = "connected"
)

func (s State) Valid() bool {
	switch s {
	case NotJoined, Disconnected, Connecting, Reconnecting, Connected:
		return true
	default:
		return false
	}
}

func Effective(raw State, processState string) State {
	if raw.Valid() {
		return raw
	}
	if raw == "" && strings.EqualFold(strings.TrimSpace(processState), "running") {
		return Connected
	}
	return Disconnected
}

func IsOnline(state State) bool { return state == Connected }

// PeerRuntime describes useful peer connectivity independently of the control
// connection. Effective intentionally retains the separate legacy/cloud policy.
func PeerRuntime(coordinator State, direct bool) State {
	if direct {
		return Connected
	}
	if coordinator.Valid() {
		return coordinator
	}
	return Disconnected
}

func LabelCN(state State) string {
	switch state {
	case NotJoined:
		return "未加入"
	case Disconnected:
		return "已断开"
	case Connecting:
		return "连接中"
	case Reconnecting:
		return "重连中"
	case Connected:
		return "已连接"
	default:
		return "已断开"
	}
}
