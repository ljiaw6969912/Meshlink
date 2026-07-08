package diagnose

import (
	"strings"
	"testing"
)

func TestCheckRDPTargetIncludesPlainLanguageContext(t *testing.T) {
	check := CheckRDPTarget(RDPCheckRequest{
		Target:       "10.77.0.9",
		TargetDevice: "office-pc",
		TunnelStatus: "offline",
	})

	if check.Status != Fail {
		t.Fatalf("Status = %s, want fail", check.Status)
	}
	for _, want := range []string{
		"隧道状态：offline",
		"目标设备：office-pc",
		"目标 IP：10.77.0.9",
		"RDP 端口：3389",
		"开启 Windows 远程桌面",
	} {
		if !strings.Contains(check.Detail, want) {
			t.Fatalf("Detail = %q, want %q", check.Detail, want)
		}
	}
}
