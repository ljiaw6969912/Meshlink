package diagnose

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"meshlink/internal/config"
	"meshlink/internal/winservice"
)

type Status string

const (
	OK   Status = "ok"
	Warn Status = "warn"
	Fail Status = "fail"
)

type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
}

type Summary struct {
	ConfigPath    string                   `json:"config_path,omitempty"`
	NodeID        string                   `json:"node_id,omitempty"`
	Mode          string                   `json:"mode,omitempty"`
	VirtualIP     string                   `json:"virtual_ip,omitempty"`
	Device        string                   `json:"device,omitempty"`
	SetupEnabled  bool                     `json:"setup_enabled"`
	ServiceStatus winservice.ServiceStatus `json:"service_status"`
	Elevated      bool                     `json:"elevated"`
}

type Report struct {
	Summary Summary `json:"summary"`
	Checks  []Check `json:"checks"`
}

func Run(configPath, serviceName string) Report {
	var report Report
	report.Summary.ConfigPath = configPath
	report.Summary.Elevated = elevated()
	report.add("进程权限", boolStatus(report.Summary.Elevated), elevatedDetail(report.Summary.Elevated))

	status, err := winservice.Status(serviceName)
	if err != nil {
		report.add("Windows 服务", Warn, err.Error())
	} else {
		report.Summary.ServiceStatus = status
		if status.Installed {
			report.add("Windows 服务", OK, "已安装："+serviceStateCN(status.State))
		} else {
			report.add("Windows 服务", Warn, "未安装")
		}
	}

	if strings.TrimSpace(configPath) == "" {
		report.add("配置", Fail, "需要配置文件路径")
		return report
	}

	absConfig, err := filepath.Abs(configPath)
	if err != nil {
		report.add("配置路径", Fail, err.Error())
		return report
	}
	report.Summary.ConfigPath = absConfig
	if _, err := os.Stat(absConfig); err != nil {
		report.add("配置文件", Fail, err.Error())
		return report
	}
	report.add("配置文件", OK, absConfig)

	cfg, err := config.Load(absConfig)
	if err != nil {
		report.add("配置校验", Fail, err.Error())
		return report
	}
	report.Summary.NodeID = cfg.NodeID
	report.Summary.Mode = cfg.Mode
	report.Summary.VirtualIP = cfg.VirtualIP
	report.Summary.Device = cfg.Device.Type
	report.Summary.SetupEnabled = cfg.Setup.Enabled
	report.add("配置校验", OK, "节点 "+cfg.NodeID+" 的 "+cfg.Mode+" 配置有效")

	report.checkFile("CA 证书", cfg.CAFile)
	report.checkFile("节点证书", cfg.CertFile)
	report.checkFile("私钥文件", cfg.KeyFile)

	if cfg.Device.Type == "tun" || cfg.Device.Type == "wintun" {
		if !report.Summary.Elevated {
			report.add("虚拟网卡权限", Fail, "真实 TUN/Wintun 需要管理员/root 权限")
		} else {
			report.add("虚拟网卡权限", OK, "当前进程已有管理员/root 权限")
		}
	}

	switch cfg.Mode {
	case "spoke":
		report.checkTCPConnect(cfg.Connect)
	case "hub":
		report.checkListen(cfg.Listen, status)
	}

	if cfg.Setup.NAT.Enabled {
		if cfg.Setup.Forwarding {
			report.add("NAT 配置", OK, "NAT 网段 "+cfg.Setup.NAT.InternalPrefix)
		} else {
			report.add("NAT 配置", Warn, "NAT 已开启，但转发未开启")
		}
	}

	return report
}

func CheckRDP(target string) Check {
	target = strings.TrimSpace(target)
	if target == "" {
		return Check{Name: "RDP 目标", Status: Fail, Detail: "需要目标地址"}
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		host = target
		port = "3389"
	}
	if host == "" {
		return Check{Name: "RDP 目标", Status: Fail, Detail: "目标地址无效"}
	}
	addr := net.JoinHostPort(host, port)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp4", addr)
	if err != nil {
		return Check{Name: "RDP TCP " + addr, Status: Fail, Detail: "无法连接；请确认远程桌面已开启、目标地址正确、隧道和防火墙可达"}
	}
	_ = conn.Close()
	return Check{Name: "RDP TCP " + addr, Status: OK, Detail: "可达"}
}

func (r *Report) checkFile(name, path string) {
	if strings.TrimSpace(path) == "" {
		r.add(name, Fail, "路径为空")
		return
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			r.add(name, Fail, "文件不存在："+path)
			return
		}
		r.add(name, Fail, "无法读取："+path)
		return
	}
	r.add(name, OK, path)
}

func (r *Report) checkTCPConnect(addr string) {
	if strings.TrimSpace(addr) == "" {
		r.add("Hub TCP 可达性", Fail, "连接地址为空")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp4", addr)
	if err != nil {
		r.add("Hub TCP 可达性", Fail, "目标："+addr+"\r\n错误："+err.Error()+"\r\n请检查 spoke 的 connect、hub 监听端口、Windows 防火墙、路由器防火墙和 IPv4 端口转发")
		return
	}
	_ = conn.Close()
	r.add("Hub TCP 可达性", OK, "已连接到 "+addr)
}

func (r *Report) checkListen(addr string, service winservice.ServiceStatus) {
	if strings.TrimSpace(addr) == "" {
		r.add("Hub 监听地址", Fail, "监听地址为空")
		return
	}
	ln, err := net.Listen("tcp4", addr)
	if err == nil {
		_ = ln.Close()
		r.add("Hub 监听地址", OK, "地址可用："+addr)
		return
	}
	if service.Installed && service.State == "running" {
		r.add("Hub 监听地址", OK, "服务运行中，地址已被占用："+addr)
		return
	}
	r.add("Hub 监听地址", Warn, fmt.Sprintf("无法绑定 %s；请检查端口是否被占用或权限是否足够", addr))
}

func (r *Report) add(name string, status Status, detail string) {
	r.Checks = append(r.Checks, Check{Name: name, Status: status, Detail: detail})
}

func boolStatus(ok bool) Status {
	if ok {
		return OK
	}
	return Warn
}

func elevatedDetail(ok bool) string {
	if ok {
		return "已提升权限"
	}
	return "未提升权限"
}

func serviceStateCN(state string) string {
	switch state {
	case "running":
		return "运行中"
	case "stopped":
		return "已停止"
	case "start pending":
		return "正在启动"
	case "stop pending":
		return "正在停止"
	case "paused":
		return "已暂停"
	default:
		return state
	}
}
