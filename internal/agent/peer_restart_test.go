package agent

import (
	"context"
	"errors"
	"testing"
)

// Lose the socket before shutdown so the old QUIC sessions cannot send a
// graceful close. Recreate the node with its existing identity and a new port.
func TestRebootedPeerReconnectsToAllOnlinePeersWithoutTraffic(t *testing.T) {
	h := newPureP2PHarnessWithPeers(t, []string{"B", "C", "D", "E"})
	h.startPeers("B", "C", "D", "E")
	for _, id := range []string{"B", "C", "D"} {
		waitDirectPair(t, h.peers[id], "E", h.peers["E"], id)
	}
	bcBefore, _ := waitDirectPair(t, h.peers["B"], "C", h.peers["C"], "B")
	old := h.peers["E"]
	if err := old.runtime.candidates.Transport().Conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := old.process.stop(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	delete(h.peers, "E")
	h.startPeers("E")
	for _, id := range []string{"B", "C", "D"} {
		waitDirectPair(t, h.peers[id], "E", h.peers["E"], id)
	}
	bcAfter, _ := waitDirectPair(t, h.peers["B"], "C", h.peers["C"], "B")
	if bcBefore.SessionID != bcAfter.SessionID || bcBefore.Generation != bcAfter.Generation {
		t.Fatal("peer reboot disturbed an unrelated direct connection")
	}
}
