//go:build windows

package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/lxn/walk"
	"meshlink/internal/config"
	"meshlink/internal/tlsutil"
	meshupdate "meshlink/internal/update"
	"meshlink/internal/version"
	"meshlink/internal/winservice"
)

func serverUpdateURL(cfg *config.Config) (string, error) {
	endpoint := cfg.Connect
	if cfg.Mode == "hub" {
		endpoint = cfg.ServerPublicEndpoint
	}
	if endpoint == "" {
		endpoint = cfg.Transport.Connect
	}
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil || strings.TrimSpace(host) == "" || port == "" {
		return "", fmt.Errorf("当前配置没有可用的服务器地址")
	}
	return "https://" + net.JoinHostPort(host, port) + "/updates", nil
}

func preferredUpdateURL(baseDir, configPath string) string {
	cfg, err := config.Load(configPath)
	if err != nil {
		return rememberedUpdateURL(baseDir)
	}
	server, err := serverUpdateURL(cfg)
	if err != nil {
		return rememberedUpdateURL(baseDir)
	}
	settings := loadDesktopSettings(baseDir)
	// An override belongs to this server, not to every network on this PC.
	if settings.LastUpdateServer == server && settings.LastUpdateURL != "" {
		if normalized, err := meshupdate.NormalizeBaseURL(settings.LastUpdateURL); err == nil {
			return normalized
		}
	}
	return server
}

func rememberServerUpdateURL(baseDir, configPath, updateURL string) error {
	if err := rememberUpdateURL(baseDir, updateURL); err != nil {
		return err
	}
	settings := loadDesktopSettings(baseDir)
	settings.LastUpdateServer = ""
	if cfg, err := config.Load(configPath); err == nil {
		settings.LastUpdateServer, _ = serverUpdateURL(cfg)
	}
	return saveDesktopSettings(baseDir, settings)
}

func updateHTTPClient(configPath, updateURL string) (*http.Client, error) {
	transport := &http.Transport{DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second, DisableKeepAlives: true}
	client := &http.Client{Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	cfg, err := config.Load(configPath)
	if err != nil {
		return client, nil
	}
	server, err := serverUpdateURL(cfg)
	if err != nil {
		return client, nil
	}
	want, _ := url.Parse(server)
	actual, err := url.Parse(updateURL)
	if err != nil {
		return nil, err
	}
	// Only present network credentials to the configured coordinator.
	if actual.Scheme != "https" || !strings.EqualFold(actual.Host, want.Host) {
		return client, nil
	}
	resolve := func(path string) string {
		if filepath.IsAbs(path) {
			return path
		}
		return filepath.Join(filepath.Dir(configPath), path)
	}
	serverName := cfg.ServerName
	if serverName == "" {
		serverName = want.Hostname()
	}
	secure, err := tlsutil.ClientConfig(resolve(cfg.CAFile), resolve(cfg.CertFile), resolve(cfg.KeyFile), serverName)
	if err != nil {
		return nil, fmt.Errorf("读取当前网络更新凭据：%w", err)
	}
	transport.TLSClientConfig = secure
	if cfg.Mode == "hub" {
		// A server updating itself must not depend on its router's NAT loopback.
		listenHost, port, err := net.SplitHostPort(cfg.Listen)
		if err != nil {
			return nil, err
		}
		if listenHost == "" || listenHost == "0.0.0.0" {
			listenHost = "127.0.0.1"
		}
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp4", net.JoinHostPort(listenHost, port))
		}
	}
	return client, nil
}

func (a *desktopApp) setUpdateBusy(busy bool) {
	a.updateBusy = busy
	for _, button := range []*walk.PushButton{a.updateCheckButton, a.updateApplyButton} {
		if button != nil {
			button.SetEnabled(!busy)
		}
	}
	for _, input := range []*walk.LineEdit{a.updateHost, a.updatePort} {
		if input != nil {
			input.SetEnabled(!busy)
		}
	}
}

func (a *desktopApp) runRemoteUpdate(install bool) {
	if a.updateBusy || a.updateState == nil {
		return
	}
	baseURL, err := a.currentUpdateBaseURL()
	if err != nil {
		a.fail("更新地址无效", err)
		return
	}
	configPath, baseDir, serviceName := a.currentConfigPath(), appBaseDir(), a.currentServiceName()
	client, err := updateHTTPClient(configPath, baseURL)
	if err != nil {
		a.fail("准备更新连接失败", err)
		return
	}
	if err := rememberServerUpdateURL(baseDir, configPath, baseURL); err != nil {
		a.fail("保存更新地址失败", err)
		return
	}
	a.updateToken++
	token := a.updateToken
	a.setUpdateBusy(true)
	a.updateState.SetText("更新状态：正在检查服务器最新版本…")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	a.updateCancel = cancel
	deliver := func(fn func()) {
		a.mw.Synchronize(func() {
			if a.updateToken != token || a.updateState == nil || a.mw.IsDisposed() {
				return
			}
			fn()
		})
	}
	go func() {
		defer client.CloseIdleConnections()
		failed := func(stage string, err error) {
			deliver(func() {
				cancel()
				a.setUpdateBusy(false)
				a.updateState.SetText("更新状态：" + stage)
				a.showUpdateInfo(err.Error())
			})
		}
		checkCtx, checkCancel := context.WithTimeout(ctx, 15*time.Second)
		result, err := meshupdate.CheckWithClient(checkCtx, client, baseURL, version.Version)
		checkCancel()
		if err != nil {
			failed("检查失败", err)
			return
		}
		if !install || !result.UpdateAvailable {
			deliver(func() {
				cancel()
				a.setUpdateBusy(false)
				a.latestUpdate = nil
				if result.UpdateAvailable {
					a.latestUpdate = &result.Manifest
					a.updateState.SetText("更新状态：发现新版本 " + result.Manifest.Version)
				} else {
					a.updateState.SetText("更新状态：已是最新版本")
				}
				a.showUpdateInfo(formatUpdateResult(result))
			})
			return
		}
		deliver(func() {
			a.updateState.SetText("更新状态：正在下载并校验 " + result.Manifest.Version + "…")
		})
		packagePath, err := meshupdate.DownloadPackageWithClient(ctx, client, baseURL, result.Manifest, filepath.Join(baseDir, "updates", "downloads"))
		if err != nil {
			failed("下载失败", err)
			return
		}
		scriptPath, err := meshupdate.WriteApplyScript(meshupdate.ApplyOptions{
			BaseDir: baseDir, PackagePath: packagePath, PackageSHA256: result.Manifest.Package.SHA256,
			Version: result.Manifest.Version, ServiceName: serviceName, WaitPID: os.Getpid(),
		})
		if err != nil {
			failed("准备失败", err)
			return
		}
		deliver(func() {
			defer cancel()
			if ctx.Err() != nil {
				a.setUpdateBusy(false)
				a.updateState.SetText("更新状态：已取消或超时")
				a.showUpdateInfo(ctx.Err().Error())
				return
			}
			cmd := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", scriptPath)
			cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
			if err := cmd.Start(); err != nil {
				a.setUpdateBusy(false)
				a.updateState.SetText("更新状态：启动失败")
				a.showUpdateInfo(err.Error())
				return
			}
			_ = cmd.Process.Release()
			if a.updateDialog != nil {
				a.updateDialog.Accept()
			}
			a.mw.Close()
		})
	}()
}

func verifyApplyResult(result meshupdate.ApplyResult, currentVersion string, status func(string) (winservice.ServiceStatus, error)) error {
	if result.Status != "success" {
		return fmt.Errorf("%s", fallbackText(result.Message, "更新未完成，请查看更新日志"))
	}
	if result.Version != currentVersion {
		return fmt.Errorf("目标版本为 %s，当前运行版本为 %s，未确认更新成功", result.Version, currentVersion)
	}
	for _, name := range result.Services {
		svc, err := status(name)
		if err != nil || !svc.Installed || svc.State != "running" {
			return fmt.Errorf("新版已安装，但服务 %s 尚未恢复运行", name)
		}
	}
	return nil
}

func (a *desktopApp) reportUpdateResult() {
	result, err := meshupdate.ReadApplyResult(appBaseDir())
	if os.IsNotExist(err) {
		return
	}
	if err == nil {
		err = verifyApplyResult(result, version.Version, winservice.Status)
		if _, consumeErr := meshupdate.ConsumeApplyResult(appBaseDir()); err == nil {
			err = consumeErr
		}
	}
	if err != nil {
		a.fail("更新未成功", err)
		return
	}
	walk.MsgBox(a.mw, "更新成功", "Meshlink 已更新至 "+result.Version+"。\r\n程序和随附脚本已更新，后台服务及桌面已重新启动。", walk.MsgBoxIconInformation)
}
