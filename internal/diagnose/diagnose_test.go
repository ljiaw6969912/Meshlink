package diagnose

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"meshlink/internal/p2p"
	"meshlink/internal/winservice"
)

func TestCheckRDPTargetStopsBeforeDialWhenTargetOffline(t *testing.T) {
	original := dialRDP
	t.Cleanup(func() { dialRDP = original })
	called := false
	dialRDP = func(context.Context, string, string) (net.Conn, error) {
		called = true
		return nil, errors.New("must not dial")
	}

	check := CheckRDPTarget(RDPCheckRequest{
		Target:       "10.77.0.9",
		TargetDevice: "office-pc",
		NetworkState: "connected",
		TargetStatus: "offline",
	})

	if called || check.Status != Fail || !strings.Contains(check.Detail, "目标设备离线") || !strings.Contains(check.Detail, "本机网络状态：connected") || !strings.Contains(check.Detail, "目标设备状态：offline") {
		t.Fatalf("check = %+v, called = %v", check, called)
	}
}

func TestCheckRDPTargetStopsBeforeDialWhenLocalNetworkDisconnected(t *testing.T) {
	original := dialRDP
	t.Cleanup(func() { dialRDP = original })
	called := false
	dialRDP = func(context.Context, string, string) (net.Conn, error) {
		called = true
		return nil, errors.New("must not dial")
	}

	check := CheckRDPTarget(RDPCheckRequest{
		Target:       "10.77.0.9",
		TargetDevice: "office-pc",
		NetworkState: "disconnected",
		TargetStatus: "offline",
	})

	if called || check.Status != Fail || !strings.Contains(check.Detail, "网络未连接") || strings.Contains(check.Detail, "目标设备离线") {
		t.Fatalf("check = %+v, called = %v", check, called)
	}
}

func TestCheckRDPTargetKeepsLegacyRequestProbeableWhenNoStateIsProvided(t *testing.T) {
	original := dialRDP
	t.Cleanup(func() { dialRDP = original })
	local, remote := net.Pipe()
	t.Cleanup(func() { _ = remote.Close() })
	dialRDP = func(context.Context, string, string) (net.Conn, error) {
		return local, nil
	}

	check := CheckRDPTarget(RDPCheckRequest{Target: "10.77.0.9"})
	if check.Status != OK {
		t.Fatalf("check = %+v, want probe to remain available for legacy request", check)
	}
}

func TestCheckRDPTargetKeepsLegacyTargetOfflineMeaning(t *testing.T) {
	original := dialRDP
	t.Cleanup(func() { dialRDP = original })
	called := false
	dialRDP = func(context.Context, string, string) (net.Conn, error) {
		called = true
		return nil, errors.New("must not dial")
	}

	check := CheckRDPTarget(RDPCheckRequest{
		Target:       "10.77.0.9",
		TargetDevice: "office-pc",
		TunnelStatus: "offline",
	})
	if called || check.Status != Fail || !strings.Contains(check.Detail, "目标设备离线") || strings.Contains(check.Detail, "发现的问题：网络未连接。") {
		t.Fatalf("check = %+v, called = %v", check, called)
	}
	if !strings.Contains(check.Detail, "目标设备状态：offline") || strings.Contains(check.Detail, "目标设备状态：未知") {
		t.Fatalf("legacy detail = %q, want the effective offline target state", check.Detail)
	}
}

func TestRunOneClickShowsEffectiveLegacyDeviceStatusInRDPDetail(t *testing.T) {
	original := dialRDP
	t.Cleanup(func() { dialRDP = original })
	dialRDP = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("injected unreachable target")
	}

	report := RunOneClick(OneClickRequest{
		Target:       "10.77.0.9",
		TargetDevice: "office-pc",
		DeviceStatus: "online",
		IncludeRDP:   true,
	})
	check := findDiagnosticCheck(report.Checks, "远程桌面可达性")
	if check == nil || check.Status != Fail {
		t.Fatalf("checks = %+v, want failed legacy RDP check", report.Checks)
	}
	if !strings.Contains(check.Detail, "目标设备状态：online") || strings.Contains(check.Detail, "目标设备状态：未知") {
		t.Fatalf("legacy one-click detail = %q, want the effective online target state", check.Detail)
	}
}

func TestCheckRDPTargetExplicitStateStillOverridesLegacyDetail(t *testing.T) {
	original := dialRDP
	t.Cleanup(func() { dialRDP = original })
	local, remote := net.Pipe()
	t.Cleanup(func() { _ = remote.Close() })
	dialRDP = func(context.Context, string, string) (net.Conn, error) {
		return local, nil
	}

	check := CheckRDPTarget(RDPCheckRequest{
		Target:       "10.77.0.9",
		TargetDevice: "office-pc",
		NetworkState: "connected",
		TargetStatus: "online",
		TunnelStatus: "offline",
	})
	if check.Status != OK || !strings.Contains(check.Detail, "本机网络状态：connected") || !strings.Contains(check.Detail, "目标设备状态：online") {
		t.Fatalf("check = %+v, want explicit states to retain priority", check)
	}
}

func TestOneClickReportPrefersLocalNetworkStateOverProjectedTargetOffline(t *testing.T) {
	report := BuildOneClickReport(OneClickRequest{
		NetworkState: "reconnecting",
		TargetStatus: "offline",
	})
	if findOneClickFinding(report, "network_not_connected") == nil {
		t.Fatalf("findings = %+v, want network_not_connected", report.Findings)
	}
	if findOneClickFinding(report, "target_offline") != nil {
		t.Fatalf("findings = %+v, must not blame target while local network is reconnecting", report.Findings)
	}
}

func TestOneClickReportAllowsConnectedOnlineTargetDespiteStalePathState(t *testing.T) {
	req := OneClickRequest{
		Target:       "10.77.0.9",
		NetworkState: "connected",
		TargetStatus: "online",
		IncludeRDP:   true,
		ConnectionStatus: p2p.ConnectionStatus{
			PathState: p2p.PathStateOffline,
		},
	}
	if !shouldRunRDPCheck(req) {
		t.Fatal("connected online target must continue to the RDP probe")
	}
	if finding := findOneClickFinding(BuildOneClickReport(req), "target_offline"); finding != nil {
		t.Fatalf("finding = %+v, explicit online target must not be reported offline", finding)
	}
}

func TestOneClickReportKeepsLegacyOnlineTargetProbeableDespiteStalePathState(t *testing.T) {
	req := OneClickRequest{
		Target:       "10.77.0.9",
		DeviceStatus: "online",
		IncludeRDP:   true,
		ConnectionStatus: p2p.ConnectionStatus{
			PathState: p2p.PathStateOffline,
		},
	}
	if !shouldRunRDPCheck(req) {
		t.Fatal("legacy online target must continue to the RDP probe")
	}
}

func TestOneClickReportExplainsRequiredScenarios(t *testing.T) {
	tests := []struct {
		name string
		req  OneClickRequest
		code string
	}{
		{
			name: "service not running",
			req: OneClickRequest{
				BaseReport: Report{Summary: Summary{ServiceStatus: winservice.ServiceStatus{Installed: true, State: "stopped"}}},
			},
			code: "service_not_running",
		},
		{
			name: "virtual adapter unavailable",
			req: OneClickRequest{
				BaseReport: Report{Checks: []Check{{Name: "虚拟网卡权限", Status: Fail, Detail: "需要提升权限"}}},
			},
			code: "virtual_adapter_unavailable",
		},
		{
			name: "firewall or network blocked",
			req: OneClickRequest{
				BaseReport: Report{Checks: []Check{{Name: "Hub TCP 可达性", Status: Fail, Detail: "防火墙阻断"}}},
			},
			code: "network_blocked",
		},
		{
			name: "target offline",
			req: OneClickRequest{
				DeviceStatus: "offline",
				ConnectionStatus: p2p.ConnectionStatus{
					PathState: p2p.PathStateOffline,
				},
			},
			code: "target_offline",
		},
		{
			name: "rdp unavailable",
			req: OneClickRequest{
				DeviceStatus: "online",
				ConnectionStatus: p2p.ConnectionStatus{
					PathState: p2p.PathStateRDPUnreachable,
				},
				RDPCheck: &Check{Name: "远程桌面可达性", Status: Fail, Detail: "无法连接"},
			},
			code: "rdp_unavailable",
		},
		{
			name: "connection failed",
			req: OneClickRequest{
				DeviceStatus: "online",
				ConnectionStatus: p2p.ConnectionStatus{
					PathState: p2p.PathStateFailed,
				},
			},
			code: "connection_failed",
		},
		{
			name: "relay path anomaly",
			req: OneClickRequest{
				DeviceStatus: "online",
				ConnectionStatus: p2p.ConnectionStatus{
					PathType:     p2p.PathTypeRelay,
					PathState:    p2p.PathStateFallbackRelay,
					QualityScore: 42,
					LatencyMS:    180,
				},
			},
			code: "relay_path_anomaly",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := BuildOneClickReport(tt.req)
			finding := findOneClickFinding(report, tt.code)
			if finding == nil {
				t.Fatalf("findings = %+v, want code %q", report.Findings, tt.code)
			}
			for label, text := range map[string]string{
				"title":          finding.Title,
				"problem":        finding.Problem,
				"impact":         finding.Impact,
				"recommendation": finding.Recommendation,
				"action":         finding.Action,
			} {
				if strings.TrimSpace(text) == "" {
					t.Fatalf("%s is empty for %+v", label, finding)
				}
			}
			assertPlainDiagnosticText(t, *finding)
		})
	}
}

func findOneClickFinding(report OneClickReport, code string) *Finding {
	for i := range report.Findings {
		if report.Findings[i].Code == code {
			return &report.Findings[i]
		}
	}
	return nil
}

func findDiagnosticCheck(checks []Check, name string) *Check {
	for i := range checks {
		if checks[i].Name == name {
			return &checks[i]
		}
	}
	return nil
}

func assertPlainDiagnosticText(t *testing.T, finding Finding) {
	t.Helper()
	text := strings.Join([]string{
		finding.Title,
		finding.Problem,
		finding.Impact,
		finding.Recommendation,
		finding.Action,
	}, "\n")
	for _, forbidden := range []string{"NAT", "CSR", "CA", "证书路径", "路由表", "服务名", "JSON", "MeshlinkAgent"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("finding text exposes %q: %q", forbidden, text)
		}
	}
}
