package deviceidentity

import "testing"

func TestNormalizeMAC(t *testing.T) {
	for _, input := range []string{"AA-BB-CC-DD-EE-F0", "aa:bb:cc:dd:ee:f0", "aabb.ccdd.eef0", " aa:bb:cc:dd:ee:f0 "} {
		got, err := NormalizeMAC(input)
		if err != nil || got != "aa:bb:cc:dd:ee:f0" {
			t.Fatalf("NormalizeMAC(%q) = %q, %v", input, got, err)
		}
	}
	for _, input := range []string{"", "invalid", "00:00:00:00:00:00", "ff:ff:ff:ff:ff:ff", "01:23:45:67:89:ab", "00:11:22:33:44:55:66:77"} {
		if got, err := NormalizeMAC(input); err == nil || got != "" {
			t.Errorf("accepted unusable MAC %q: %q, %v", input, got, err)
		}
	}
}

func TestSelectMACUsesPermanentHardwareAddressAndStableOrdering(t *testing.T) {
	a := adapter{name: "Ethernet", hardware: true, kind: 6, permanent: "ac:11:22:33:44:55", current: "02:11:22:33:44:55"}
	b := adapter{name: "Wi-Fi", hardware: true, kind: 71, permanent: "aa:11:22:33:44:55", current: "02:11:22:33:44:56"}
	for _, input := range [][]adapter{{a, b}, {b, a}} {
		if got := selectMAC(input); got != b.permanent {
			t.Fatalf("selected %q, want permanent %q", got, b.permanent)
		}
	}
}

func TestSelectMACRejectsVirtualAndUnusableAdapters(t *testing.T) {
	for _, name := range []string{"Meshlink", "Wintun Userspace Tunnel", "WireGuard", "TAP-Windows", "Clash", "sing-box", "Microsoft Wi-Fi Direct Virtual Adapter", "Hyper-V Virtual Ethernet Adapter", "VMware Virtual Ethernet", "Proxy Adapter"} {
		t.Run(name, func(t *testing.T) {
			if got := selectMAC([]adapter{{name: name, hardware: true, kind: 6, permanent: "aa:11:22:33:44:55"}}); got != "" {
				t.Fatalf("selected virtual %q", got)
			}
		})
	}
	for _, a := range []adapter{
		{hardware: false, kind: 6, permanent: "aa:11:22:33:44:55"},
		{hardware: true, filter: true, kind: 6, permanent: "aa:11:22:33:44:55"},
		{hardware: true, kind: 24, permanent: "aa:11:22:33:44:55"},
		{hardware: true, kind: 131, permanent: "aa:11:22:33:44:55"},
		{hardware: true, kind: 6, permanent: "00:00:00:00:00:00"},
	} {
		if got := selectMAC([]adapter{a}); got != "" {
			t.Fatalf("selected unusable adapter: %q", got)
		}
	}
}

func TestSelectMACAllowsLocallyAdministeredPhysicalWiFi(t *testing.T) {
	if got := selectMAC([]adapter{{name: "Intel Wi-Fi", hardware: true, kind: 71, current: "02:11:22:33:44:55"}}); got != "02:11:22:33:44:55" {
		t.Fatalf("got %q", got)
	}
	if got := selectMAC(nil); got != "" {
		t.Fatalf("empty adapters returned %q", got)
	}
}
