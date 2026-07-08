package deployssh

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"time"
)

const ServiceName = "meshlink-agent"

type Stage string

const (
	StageValidateBundle   Stage = "validate_bundle"
	StageCheckSystem      Stage = "check_system"
	StageCheckSystemd     Stage = "check_systemd"
	StageCheckPermissions Stage = "check_permissions"
	StageCheckPort        Stage = "check_port"
	StageCreateDirs       Stage = "create_dirs"
	StageBackup           Stage = "backup"
	StageUpload           Stage = "upload"
	StageInstallService   Stage = "install_service"
	StageRestartService   Stage = "restart_service"
	StageHealthCheck      Stage = "health_check"
	StageRollback         Stage = "rollback"
)

type Layout struct {
	BaseDir         string `json:"base_dir"`
	BinDir          string `json:"bin_dir"`
	ConfigRoot      string `json:"config_root"`
	ConfigDir       string `json:"config_dir"`
	CertDir         string `json:"cert_dir"`
	InviteDir       string `json:"invite_dir"`
	LogDir          string `json:"log_dir"`
	BackupDir       string `json:"backup_dir"`
	AgentBinaryPath string `json:"agent_binary_path"`
	ConfigPath      string `json:"config_path"`
	CAPath          string `json:"ca_path"`
	CAKeyPath       string `json:"ca_key_path"`
	InviteStorePath string `json:"invite_store_path"`
	ServicePath     string `json:"service_path"`
}

func DefaultLayout() Layout {
	layout := Layout{
		BaseDir:     "/opt/meshlink",
		ConfigRoot:  "/etc/meshlink",
		LogDir:      "/var/log/meshlink",
		ServicePath: "/etc/systemd/system/" + ServiceName + ".service",
	}
	return layout.withDefaults()
}

func (l Layout) withDefaults() Layout {
	if l.BaseDir == "" {
		l.BaseDir = "/opt/meshlink"
	}
	if l.BinDir == "" {
		l.BinDir = path.Join(l.BaseDir, "bin")
	}
	if l.ConfigRoot == "" {
		l.ConfigRoot = "/etc/meshlink"
	}
	if l.ConfigDir == "" {
		l.ConfigDir = path.Join(l.ConfigRoot, "configs")
	}
	if l.CertDir == "" {
		l.CertDir = path.Join(l.ConfigRoot, "certs")
	}
	if l.InviteDir == "" {
		l.InviteDir = path.Join(l.ConfigRoot, "invites")
	}
	if l.LogDir == "" {
		l.LogDir = "/var/log/meshlink"
	}
	if l.BackupDir == "" {
		l.BackupDir = path.Join(l.BaseDir, "backups")
	}
	if l.AgentBinaryPath == "" {
		l.AgentBinaryPath = path.Join(l.BinDir, "mesh-agent")
	}
	if l.ConfigPath == "" {
		l.ConfigPath = path.Join(l.ConfigDir, "active.json")
	}
	if l.CAPath == "" {
		l.CAPath = path.Join(l.CertDir, "ca.pem")
	}
	if l.CAKeyPath == "" {
		l.CAKeyPath = path.Join(l.CertDir, "ca-key.pem")
	}
	if l.InviteStorePath == "" {
		l.InviteStorePath = path.Join(l.InviteDir, "invites.json")
	}
	if l.ServicePath == "" {
		l.ServicePath = "/etc/systemd/system/" + ServiceName + ".service"
	}
	return l
}

func (l Layout) HubCertPath(nodeName string) string {
	return path.Join(l.CertDir, nodeName+".pem")
}

func (l Layout) HubKeyPath(nodeName string) string {
	return path.Join(l.CertDir, nodeName+"-key.pem")
}

type BundleFile struct {
	RemotePath string      `json:"remote_path"`
	Content    []byte      `json:"-"`
	Mode       fs.FileMode `json:"mode"`
}

type Bundle struct {
	PublicAddress string       `json:"public_address"`
	ListenPort    int          `json:"listen_port"`
	Files         []BundleFile `json:"files"`
}

type ProvisionRequest struct {
	Layout   Layout `json:"layout"`
	Bundle   Bundle `json:"bundle"`
	BackupID string `json:"backup_id,omitempty"`
}

type RemoteClient interface {
	Run(ctx context.Context, command string) (RunResult, error)
	Upload(ctx context.Context, remotePath string, content []byte, mode fs.FileMode) error
}

type RunResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

type HealthCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type RollbackResult struct {
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type ProvisionResult struct {
	ServiceName   string         `json:"service_name"`
	ServiceStatus string         `json:"service_status"`
	Layout        Layout         `json:"layout"`
	HostTrust     HostTrust      `json:"host_trust,omitempty"`
	Health        []HealthCheck  `json:"health"`
	Rollback      RollbackResult `json:"rollback,omitempty"`
}

type ProvisionError struct {
	Stage    Stage
	Err      error
	Rollback RollbackResult
}

func (e *ProvisionError) Error() string {
	if e == nil {
		return ""
	}
	if e.Rollback.Status != "" {
		return fmt.Sprintf("%s：%v；回滚结果：%s", e.Stage, e.Err, e.Rollback.Detail)
	}
	return fmt.Sprintf("%s：%v", e.Stage, e.Err)
}

func (e *ProvisionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func Provision(ctx context.Context, client RemoteClient, req ProvisionRequest) (ProvisionResult, error) {
	if client == nil {
		return ProvisionResult{}, &ProvisionError{Stage: StageValidateBundle, Err: fmt.Errorf("SSH client is required")}
	}
	layout := req.Layout.withDefaults()
	if req.BackupID == "" {
		req.BackupID = time.Now().UTC().Format("20060102T150405Z")
	}
	if err := validateBundle(layout, req.Bundle); err != nil {
		return ProvisionResult{}, &ProvisionError{Stage: StageValidateBundle, Err: err}
	}
	result := ProvisionResult{ServiceName: ServiceName, Layout: layout}
	if err := runRequired(ctx, client, StageCheckSystem, "远程系统类型不是 Linux", `test "$(uname -s)" = "Linux"`); err != nil {
		return result, err
	}
	if err := runRequired(ctx, client, StageCheckSystemd, "远程 systemd 不可用，请确认云服务器使用 systemd Linux 发行版", `command -v systemctl >/dev/null 2>&1 && test -d /run/systemd/system`); err != nil {
		return result, err
	}
	if err := runRequired(ctx, client, StageCheckPermissions, "远程目录权限不足，请使用 root 用户或配置免密 sudo", permissionCommand()); err != nil {
		return result, err
	}
	if err := runRequired(ctx, client, StageCheckPort, fmt.Sprintf("监听端口 %d 已被占用，请更换端口或停止占用进程", req.Bundle.ListenPort), listenPortCommand(req.Bundle.ListenPort)); err != nil {
		return result, err
	}
	if err := runRequired(ctx, client, StageCreateDirs, "创建远程目录失败", privileged(createDirsCommand(layout))); err != nil {
		return result, err
	}
	backupPath := path.Join(layout.BackupDir, req.BackupID)
	if err := runRequired(ctx, client, StageBackup, "创建远程回滚备份失败", privileged(backupCommand(layout, backupPath))); err != nil {
		return result, err
	}
	for _, file := range append(req.Bundle.Files, BundleFile{
		RemotePath: layout.ServicePath,
		Content:    []byte(SystemdServiceUnit(layout)),
		Mode:       0o644,
	}) {
		if err := client.Upload(ctx, file.RemotePath, file.Content, file.Mode); err != nil {
			rollback := rollback(ctx, client, layout, backupPath)
			return result, &ProvisionError{Stage: StageUpload, Err: fmt.Errorf("上传 %s 失败：%w", file.RemotePath, err), Rollback: rollback}
		}
	}
	if err := runRequired(ctx, client, StageInstallService, "安装 systemd 服务失败", privileged(`systemctl daemon-reload && systemctl enable `+ServiceName)); err != nil {
		rollback := rollback(ctx, client, layout, backupPath)
		return result, withRollback(err, rollback)
	}
	if err := runRequired(ctx, client, StageRestartService, "启动或重启 meshlink-agent 服务失败", privileged(`systemctl restart `+ServiceName)); err != nil {
		_ = collectDiagnostics(ctx, client, layout)
		rollback := rollback(ctx, client, layout, backupPath)
		return result, withRollback(err, rollback)
	}
	health, status, err := Health(ctx, client, layout, req.Bundle.ListenPort)
	result.Health = health
	result.ServiceStatus = status
	if err != nil {
		rollback := rollback(ctx, client, layout, backupPath)
		result.Rollback = rollback
		return result, &ProvisionError{Stage: StageHealthCheck, Err: err, Rollback: rollback}
	}
	return result, nil
}

func validateBundle(layout Layout, bundle Bundle) error {
	if bundle.ListenPort <= 0 || bundle.ListenPort > 65535 {
		return fmt.Errorf("Meshlink 监听端口必须在 1-65535 之间")
	}
	required := map[string]string{
		layout.AgentBinaryPath: "Linux mesh-agent 产物不存在，请先运行 Linux agent 构建脚本",
		layout.ConfigPath:      "远程 Hub 配置不存在",
		layout.CAPath:          "CA 证书不存在",
		layout.CAKeyPath:       "CA 私钥不存在",
		layout.InviteStorePath: "接入邀请文件不存在",
	}
	for _, file := range bundle.Files {
		if len(file.Content) == 0 {
			continue
		}
		delete(required, file.RemotePath)
	}
	for _, msg := range required {
		return errors.New(msg)
	}
	return nil
}

func Health(ctx context.Context, client RemoteClient, layout Layout, listenPort int) ([]HealthCheck, string, error) {
	checks := make([]HealthCheck, 0, 7)
	status := "unknown"
	run := func(name, command string) (RunResult, error) {
		out, err := client.Run(ctx, command)
		if err != nil {
			checks = append(checks, HealthCheck{Name: name, Status: "fail", Detail: strings.TrimSpace(out.Stderr + " " + err.Error())})
			return out, err
		}
		checks = append(checks, HealthCheck{Name: name, Status: "ok", Detail: strings.TrimSpace(out.Stdout)})
		return out, nil
	}
	_, _ = run("SSH 可达", "true")
	_, _ = run("远程系统类型", `test "$(uname -s)" = "Linux"`)
	_, _ = run("远程目录权限", permissionCommand())
	_, _ = run("systemd", `command -v systemctl >/dev/null 2>&1 && test -d /run/systemd/system`)
	_, _ = run("mesh-agent 已安装", `test -x `+shellQuote(layout.AgentBinaryPath))
	_, _ = run("远程版本", shellQuote(layout.AgentBinaryPath)+" -version")
	if out, err := run("远程服务状态", `systemctl is-active `+ServiceName); err == nil {
		if strings.TrimSpace(out.Stdout) == "active" {
			status = "running"
		} else {
			status = strings.TrimSpace(out.Stdout)
		}
	}
	_, _ = run("监听端口", portListeningCommand(listenPort))
	_, err := run("/enroll/health", fmt.Sprintf("curl -fsSk https://127.0.0.1:%d/enroll/health", listenPort))
	if err != nil {
		return checks, status, fmt.Errorf("/enroll/health 不可访问，请检查服务日志和云服务器安全组")
	}
	return checks, status, nil
}

func runRequired(ctx context.Context, client RemoteClient, stage Stage, message, command string) error {
	out, err := client.Run(ctx, command)
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(out.Stderr)
	if detail == "" {
		detail = err.Error()
	}
	return &ProvisionError{Stage: stage, Err: fmt.Errorf("%s：%s", message, detail)}
}

func withRollback(err error, rollback RollbackResult) error {
	var perr *ProvisionError
	if ok := errors.As(err, &perr); ok {
		perr.Rollback = rollback
		return perr
	}
	return &ProvisionError{Stage: StageRollback, Err: err, Rollback: rollback}
}

func rollback(ctx context.Context, client RemoteClient, layout Layout, backupPath string) RollbackResult {
	cmd := privileged(rollbackCommand(layout, backupPath))
	if _, err := client.Run(ctx, cmd); err != nil {
		return RollbackResult{Status: "failed", Detail: "回滚失败：" + err.Error()}
	}
	return RollbackResult{Status: "restored", Detail: "已恢复上一版二进制、配置和 systemd 服务"}
}

func collectDiagnostics(ctx context.Context, client RemoteClient, layout Layout) error {
	cmd := privileged(fmt.Sprintf(`mkdir -p %s
systemctl status %s --no-pager > %s/deploy-last-status.log 2>&1 || true
journalctl -u %s -n 120 --no-pager > %s/deploy-last-journal.log 2>&1 || true`,
		shellQuote(layout.LogDir), ServiceName, shellQuote(layout.LogDir), ServiceName, shellQuote(layout.LogDir)))
	_, err := client.Run(ctx, cmd)
	return err
}

func SystemdServiceUnit(layout Layout) string {
	layout = layout.withDefaults()
	return `[Unit]
Description=Meshlink self-hosted relay hub
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=` + layout.AgentBinaryPath + ` -config ` + layout.ConfigPath + `
WorkingDirectory=` + layout.ConfigRoot + `
Restart=on-failure
RestartSec=3
LimitNOFILE=1048576
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
`
}

func permissionCommand() string {
	return `# test-directory-permission
if [ "$(id -u)" -eq 0 ]; then exit 0; fi
if command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1; then exit 0; fi
exit 70`
}

func listenPortCommand(port int) string {
	p := strconv.Itoa(port)
	return `# check-listen-port
if systemctl is-active --quiet ` + ServiceName + ` 2>/dev/null; then exit 0; fi
if command -v ss >/dev/null 2>&1; then
  ss -ltn | awk '{print $4}' | grep -E '(^|:)` + p + `$' >/dev/null && exit 73 || exit 0
fi
if command -v netstat >/dev/null 2>&1; then
  netstat -ltn | awk '{print $4}' | grep -E '(^|:)` + p + `$' >/dev/null && exit 73 || exit 0
fi
exit 0`
}

func portListeningCommand(port int) string {
	p := strconv.Itoa(port)
	return `if command -v ss >/dev/null 2>&1; then
  ss -ltn | awk '{print $4}' | grep -E '(^|:)` + p + `$' >/dev/null
elif command -v netstat >/dev/null 2>&1; then
  netstat -ltn | awk '{print $4}' | grep -E '(^|:)` + p + `$' >/dev/null
else
  true
fi`
}

func createDirsCommand(layout Layout) string {
	return fmt.Sprintf("mkdir -p %s %s %s %s %s %s",
		layout.BinDir,
		layout.ConfigDir,
		layout.CertDir,
		layout.InviteDir,
		layout.LogDir,
		layout.BackupDir,
	)
}

func backupCommand(layout Layout, backupPath string) string {
	return fmt.Sprintf(`# backup-create
mkdir -p %[1]s
rm -rf %[2]s
mkdir -p %[2]s
if [ -e %[3]s ]; then cp -a %[3]s %[2]s/bin; fi
if [ -e %[4]s ]; then cp -a %[4]s %[2]s/config-root; fi
if [ -e %[5]s ]; then cp -a %[5]s %[2]s/service; fi`,
		shellQuote(layout.BackupDir),
		shellQuote(backupPath),
		shellQuote(layout.BinDir),
		shellQuote(layout.ConfigRoot),
		shellQuote(layout.ServicePath),
	)
}

func rollbackCommand(layout Layout, backupPath string) string {
	return fmt.Sprintf(`# rollback-restore
if [ -d %[1]s/bin ]; then rm -rf %[2]s && cp -a %[1]s/bin %[2]s; fi
if [ -d %[1]s/config-root ]; then rm -rf %[3]s && cp -a %[1]s/config-root %[3]s; fi
if [ -f %[1]s/service ]; then cp -a %[1]s/service %[4]s; fi
systemctl daemon-reload || true
systemctl restart %[5]s || true`,
		shellQuote(backupPath),
		shellQuote(layout.BinDir),
		shellQuote(layout.ConfigRoot),
		shellQuote(layout.ServicePath),
		ServiceName,
	)
}

func privileged(command string) string {
	return `if [ "$(id -u)" -eq 0 ]; then sh -c ` + shellQuote(command) + `; else sudo -n sh -c ` + shellQuote(command) + `; fi`
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
