package diagnose

import (
	"strconv"
	"strings"

	"meshlink/internal/p2p"
)

type OneClickRequest struct {
	ConfigPath       string               `json:"config_path,omitempty"`
	ServiceName      string               `json:"service_name,omitempty"`
	Target           string               `json:"target,omitempty"`
	TargetDevice     string               `json:"target_device,omitempty"`
	NetworkState     string               `json:"network_state,omitempty"`
	TargetStatus     string               `json:"target_status,omitempty"`
	TunnelStatus     string               `json:"tunnel_status,omitempty"`
	Port             int                  `json:"port,omitempty"`
	DeviceStatus     string               `json:"device_status,omitempty"`
	ConnectionStatus p2p.ConnectionStatus `json:"connection_status,omitempty"`
	IncludeRDP       bool                 `json:"include_rdp,omitempty"`

	BaseReport Report `json:"-"`
	RDPCheck   *Check `json:"-"`
}

type OneClickReport struct {
	Status   Status          `json:"status"`
	Summary  OneClickSummary `json:"summary"`
	Findings []Finding       `json:"findings"`
	Checks   []Check         `json:"checks,omitempty"`
}

type OneClickSummary struct {
	Status       Status `json:"status"`
	Headline     string `json:"headline"`
	TargetDevice string `json:"target_device,omitempty"`
}

type Finding struct {
	Code           string `json:"code"`
	Severity       Status `json:"severity"`
	Title          string `json:"title"`
	Problem        string `json:"problem"`
	Impact         string `json:"impact"`
	Recommendation string `json:"recommendation"`
	Action         string `json:"action"`
}

func RunOneClick(req OneClickRequest) OneClickReport {
	req.BaseReport = Run(req.ConfigPath, req.ServiceName)
	if shouldRunRDPCheck(req) {
		check := CheckRDPTarget(RDPCheckRequest{
			Target:       req.Target,
			TargetDevice: req.TargetDevice,
			NetworkState: req.NetworkState,
			TargetStatus: oneClickTargetStatus(req),
			TunnelStatus: req.TunnelStatus,
			Port:         req.Port,
		})
		req.RDPCheck = &check
	}
	return BuildOneClickReport(req)
}

func BuildOneClickReport(req OneClickRequest) OneClickReport {
	report := OneClickReport{
		Status: OK,
		Checks: append([]Check(nil), req.BaseReport.Checks...),
	}
	if req.RDPCheck != nil {
		report.Checks = append(report.Checks, *req.RDPCheck)
	}

	addServiceFindings(&report, req)
	addBaseCheckFindings(&report, req.BaseReport.Checks)
	addConnectionFindings(&report, req)
	addRDPFindings(&report, req)

	report.Status = oneClickStatus(report.Findings)
	report.Summary = OneClickSummary{
		Status:       report.Status,
		Headline:     oneClickHeadline(report.Status, len(report.Findings)),
		TargetDevice: strings.TrimSpace(req.TargetDevice),
	}
	return report
}

func addServiceFindings(report *OneClickReport, req OneClickRequest) {
	status := req.BaseReport.Summary.ServiceStatus
	if !status.Installed {
		return
	}
	if strings.EqualFold(status.State, "running") {
		return
	}
	report.addFinding(Finding{
		Code:           "service_not_running",
		Severity:       Fail,
		Title:          "后台连接服务未运行",
		Problem:        "本机后台连接服务未运行。",
		Impact:         "设备列表和远程桌面连接可能不会更新。",
		Recommendation: "请在控制台启动后台连接服务；如果启动失败，再查看详细诊断。",
		Action:         "启动后台连接服务后刷新设备列表，再重新诊断。",
	})
}

func addBaseCheckFindings(report *OneClickReport, checks []Check) {
	for _, check := range checks {
		if check.Status == OK {
			continue
		}
		name := strings.TrimSpace(check.Name)
		switch {
		case strings.Contains(name, "Windows 服务"):
			report.addFinding(Finding{
				Code:           "service_not_running",
				Severity:       Fail,
				Title:          "后台连接服务未运行",
				Problem:        "本机后台连接服务不可用。",
				Impact:         "设备列表和远程桌面连接可能不会更新。",
				Recommendation: "请在控制台启动后台连接服务；如果启动失败，再查看详细诊断。",
				Action:         "启动后台连接服务后刷新设备列表，再重新诊断。",
			})
		case strings.Contains(name, "虚拟网卡"):
			report.addFinding(Finding{
				Code:           "virtual_adapter_unavailable",
				Severity:       Fail,
				Title:          "虚拟网卡暂不可用",
				Problem:        "本机虚拟网卡当前不可用。",
				Impact:         "本机无法获得组网地址，远程设备也可能无法访问本机。",
				Recommendation: "请以管理员身份运行控制台，或重新安装虚拟网卡驱动。",
				Action:         "重新以管理员身份打开控制台后，再运行一键诊断。",
			})
		case strings.Contains(name, "TCP 可达性") || strings.Contains(name, "监听地址"):
			report.addFinding(Finding{
				Code:           "network_blocked",
				Severity:       Fail,
				Title:          "防火墙或网络阻断",
				Problem:        "本机到对端的连接入口不可达。",
				Impact:         "设备可能在线，但远程桌面连接无法稳定建立。",
				Recommendation: "请检查两端网络，并确认防火墙允许 Meshlink 通信端口。",
				Action:         "调整网络或防火墙后刷新设备列表，再重新诊断。",
			})
		}
	}
}

func addConnectionFindings(report *OneClickReport, req OneClickRequest) {
	conn := req.ConnectionStatus

	if oneClickNetworkUnavailable(req) {
		report.addFinding(networkNotConnectedFinding())
		return
	}
	if oneClickTargetOffline(req) {
		report.addFinding(Finding{
			Code:           "target_offline",
			Severity:       Fail,
			Title:          "目标设备离线",
			Problem:        "目标设备当前不在线。",
			Impact:         "本机现在无法打开这台设备的远程桌面。",
			Recommendation: "请确认目标设备已开机，并保持 Meshlink 正在运行。",
			Action:         "目标设备上线后刷新设备列表，再重新诊断。",
		})
	}
	if conn.PathState == p2p.PathStateRDPUnreachable {
		report.addFinding(rdpUnavailableFinding())
	}
	if conn.PathState == p2p.PathStateFailed || conn.PathState == p2p.PathStateClosed {
		report.addFinding(Finding{
			Code:           "connection_failed",
			Severity:       Fail,
			Title:          "设备连接失败",
			Problem:        "当前组网连接没有建立成功或已经断开。",
			Impact:         "远程桌面无法稳定打开。",
			Recommendation: "请刷新设备列表；如果仍失败，重启两端 Meshlink 并检查网络。",
			Action:         "刷新设备列表后重试远程桌面。",
		})
	}
	if relayPathAnomaly(conn) {
		report.addFinding(Finding{
			Code:           "relay_path_anomaly",
			Severity:       Warn,
			Title:          "中继连接质量异常",
			Problem:        "当前连接走中继且质量较低。",
			Impact:         "远程桌面可能卡顿或断开。",
			Recommendation: "请优先检查两端网络质量和防火墙设置，稍后刷新设备状态。",
			Action:         "刷新设备列表，再重新尝试远程桌面。",
		})
	}
}

func addRDPFindings(report *OneClickReport, req OneClickRequest) {
	if req.RDPCheck == nil || req.RDPCheck.Status == OK {
		return
	}
	if hasFinding(report.Findings, "network_not_connected") || hasFinding(report.Findings, "target_offline") {
		return
	}
	report.addFinding(rdpUnavailableFinding())
}

func networkNotConnectedFinding() Finding {
	return Finding{
		Code:           "network_not_connected",
		Severity:       Fail,
		Title:          "网络未连接",
		Problem:        "本机 Meshlink 网络当前未连接。",
		Impact:         "本机现在无法访问目标设备的远程桌面。",
		Recommendation: "请先重新连接 Meshlink 网络，确认本机显示已连接后再试。",
		Action:         "重新连接网络并刷新设备列表，再重新诊断。",
	}
}

func rdpUnavailableFinding() Finding {
	return Finding{
		Code:           "rdp_unavailable",
		Severity:       Fail,
		Title:          "远程桌面不可达",
		Problem:        "目标设备的远程桌面没有响应。",
		Impact:         "本机暂时无法打开这台设备的远程桌面。",
		Recommendation: "请在目标 Windows 设备开启远程桌面，并允许远程桌面通过防火墙。",
		Action:         "处理后点击“一键诊断”复查。",
	}
}

func shouldRunRDPCheck(req OneClickRequest) bool {
	if !req.IncludeRDP || strings.TrimSpace(req.Target) == "" {
		return false
	}
	if oneClickNetworkUnavailable(req) || oneClickTargetOffline(req) {
		return false
	}
	return true
}

func oneClickNetworkUnavailable(req OneClickRequest) bool {
	if strings.TrimSpace(req.NetworkState) != "" {
		return rdpNetworkUnavailable(req.NetworkState)
	}
	return legacyRDPLocalNetworkUnavailable(req.TunnelStatus)
}

func oneClickTargetStatus(req OneClickRequest) string {
	if strings.TrimSpace(req.TargetStatus) != "" {
		return req.TargetStatus
	}
	return req.DeviceStatus
}

func oneClickTargetOffline(req OneClickRequest) bool {
	if strings.TrimSpace(req.TargetStatus) != "" {
		return rdpTargetOffline(req.TargetStatus)
	}
	if strings.TrimSpace(req.DeviceStatus) != "" {
		return rdpTargetOffline(req.DeviceStatus)
	}
	return rdpTargetOffline(req.TunnelStatus) || req.ConnectionStatus.PathState == p2p.PathStateOffline
}

func relayPathAnomaly(conn p2p.ConnectionStatus) bool {
	if conn.PathType != p2p.PathTypeRelay && conn.PathState != p2p.PathStateFallbackRelay {
		return false
	}
	if conn.QualityScore > 0 && conn.QualityScore < 70 {
		return true
	}
	if conn.LatencyMS >= 150 {
		return true
	}
	if conn.SwitchCount > 0 {
		return true
	}
	return strings.TrimSpace(conn.LastError) != ""
}

func (r *OneClickReport) addFinding(finding Finding) {
	if finding.Severity == "" {
		finding.Severity = Warn
	}
	if hasFinding(r.Findings, finding.Code) {
		return
	}
	r.Findings = append(r.Findings, finding)
}

func hasFinding(findings []Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func oneClickStatus(findings []Finding) Status {
	status := OK
	for _, finding := range findings {
		if finding.Severity == Fail {
			return Fail
		}
		if finding.Severity == Warn {
			status = Warn
		}
	}
	return status
}

func oneClickHeadline(status Status, count int) string {
	if count == 0 || status == OK {
		return "未发现需要处理的问题。"
	}
	if status == Fail {
		return "发现 " + strconv.Itoa(count) + " 个需要处理的问题。"
	}
	return "发现 " + strconv.Itoa(count) + " 个需要关注的提醒。"
}
