package deviceidentity

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsAdapterIdentityIgnoresConnectionState(t *testing.T) {
	row := windows.MibIfRow2{Type: windows.IF_TYPE_IEEE80211, PhysicalAddressLength: 6, InterfaceAndOperStatusFlags: 1}
	copy(row.PermanentPhysicalAddress[:], []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xf0})
	copy(row.PhysicalAddress[:], []byte{0x02, 0xbb, 0xcc, 0xdd, 0xee, 0xf0})
	for _, status := range []uint32{1, 2, 5, 7} {
		row.OperStatus = status
		row.MediaConnectState = status
		row.InterfaceIndex = status
		if got := selectMAC([]adapter{adapterFromRow(row)}); got != "aa:bb:cc:dd:ee:f0" {
			t.Fatalf("status %d changed identity to %q", status, got)
		}
	}
	row.InterfaceAndOperStatusFlags |= 2
	if got := selectMAC([]adapter{adapterFromRow(row)}); got != "" {
		t.Fatalf("filter interface selected: %q", got)
	}
}

func TestLocalMACDiscoveryReturnsCanonicalOrEmpty(t *testing.T) {
	if got := LocalMAC(); got != "" {
		normalized, err := NormalizeMAC(got)
		if err != nil || normalized != got {
			t.Fatalf("noncanonical result %q: %v", got, err)
		}
	}
}
