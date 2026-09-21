// mesh-coordinator is the headless Linux server and local mesh participant.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"meshlink/internal/config"
	"meshlink/internal/onboarding"
	"meshlink/internal/runner"
	"meshlink/internal/version"
)

type options struct {
	data, server, listen string
	maxUses              int
	maxUsesSet, newCode  bool
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("mesh-coordinator", flag.ContinueOnError)
	fs.SetOutput(errOut)
	var o options
	var showVersion, devices bool
	fs.StringVar(&o.data, "data", "./data", "persistent network data directory")
	fs.StringVar(&o.server, "server", "", "public IPv4 or DNS name[:port]; required on first start")
	fs.StringVar(&o.listen, "listen", "", "local IPv4:port; defaults to 0.0.0.0:3222 on first start")
	fs.IntVar(&o.maxUses, "max-uses", 100, "invitation enrollment limit; -1 means unlimited")
	fs.BoolVar(&o.newCode, "new-code", false, "replace the invitation on this start")
	fs.BoolVar(&showVersion, "version", false, "print version and exit")
	fs.BoolVar(&devices, "devices", false, "print current device list as JSON and exit")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "max-uses" {
			o.maxUsesSet = true
		}
	})
	if showVersion {
		_, err := fmt.Fprintln(out, version.Display())
		return err
	}
	absolute, err := filepath.Abs(o.data)
	if err != nil {
		return err
	}
	o.data = absolute
	if devices {
		list, err := (onboarding.Manager{BaseDir: o.data}).Devices("mesh-coordinator")
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(list)
	}
	if err := platformPreflight(); err != nil {
		return err
	}
	lock, err := acquireDataLock(o.data)
	if err != nil {
		return err
	}
	defer lock.Close()
	result, err := prepareServer(o)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Meshlink Linux %s\n服务器：%s\n监听：%s（TCP 和 UDP）\n本机组网 IP：%s\n数据目录：%s\n接入链接：%s\n验证码：%s\n正在启动协调服务和本机节点，按 Ctrl+C 停止。\n", version.Display(), result.Invite.Server, result.Listen, result.VirtualIP, o.data, result.Invite.Link, result.Invite.Code)
	return runner.Run(ctx, result.ConfigPath, runner.ConsoleLogger(out), runner.WithServiceName("mesh-coordinator"))
}

func prepareServer(o options) (onboarding.StartServerResult, error) {
	fail := func(err error) (onboarding.StartServerResult, error) { return onboarding.StartServerResult{}, err }
	if strings.TrimSpace(o.data) == "" {
		return fail(fmt.Errorf("data directory is required"))
	}
	if o.maxUses == 0 {
		o.maxUses = 100
	}
	if o.maxUses < -1 {
		return fail(fmt.Errorf("max-uses must be positive or -1"))
	}
	name := ""
	cfg, err := config.Load(filepath.Join(o.data, "configs", "active.json"))
	if err == nil {
		if cfg.Mode != "hub" {
			return fail(fmt.Errorf("数据目录已有客户端配置，请使用独立的服务器数据目录"))
		}
		name = cfg.NodeID
		// This launcher manages the standard installation layout. Do not
		// silently replace identities from a manually customized installation.
		for _, pair := range [][2]string{{cfg.CAFile, "../certs/ca.pem"}, {cfg.CertFile, "../certs/" + name + ".pem"}, {cfg.KeyFile, "../certs/" + name + "-key.pem"}} {
			if filepath.ToSlash(filepath.Clean(pair[0])) != pair[1] {
				return fail(fmt.Errorf("现有配置使用自定义证书路径，已保留全部数据；请使用原 mesh-agent -config 方式启动"))
			}
		}
		if o.server == "" {
			o.server = cfg.ServerPublicEndpoint
		}
		if o.listen == "" {
			o.listen = cfg.Listen
		}
	} else if !os.IsNotExist(err) {
		return fail(fmt.Errorf("保留原配置，请先修复读取错误：%w", err))
	}
	if o.listen == "" {
		o.listen = "0.0.0.0:3222"
	}
	listenHost, port, err := parseEndpoint(o.listen, true)
	if err != nil {
		return fail(err)
	}
	if o.server == "" {
		return fail(fmt.Errorf("首次启动请指定公网地址，例如 -server example.com:3222"))
	}
	if !strings.Contains(o.server, ":") {
		o.server = net.JoinHostPort(o.server, strconv.Itoa(port))
	}
	if _, _, err := parseEndpoint(o.server, false); err != nil {
		return fail(err)
	}
	m := onboarding.Manager{BaseDir: o.data, LocalIPv4: func() (string, error) { return listenHost, nil }}
	if !o.maxUsesSet {
		previous, found, err := m.LatestInvite()
		if err != nil {
			return fail(err)
		}
		if found && previous.LongLived {
			o.maxUses = previous.MaxUses
		}
	}
	result, err := m.StartServerMode(onboarding.StartServerRequest{NodeName: name, ServerAddress: o.server, ListenPort: port, LongLived: true, MaxUses: o.maxUses})
	if err != nil {
		return fail(err)
	}
	if o.newCode {
		result.Invite, err = m.CreateInvite(onboarding.CreateInviteRequest{Server: o.server, LongLived: true, MaxUses: o.maxUses, ReplaceExisting: true})
		if err != nil {
			return fail(err)
		}
	}
	return result, nil
}

func parseEndpoint(address string, listen bool) (string, int, error) {
	host, portString, err := net.SplitHostPort(address)
	if err != nil {
		return "", 0, fmt.Errorf("地址需为 IPv4 或域名加端口：%q", address)
	}
	port, err := strconv.Atoi(portString)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("端口应在 1–65535：%q", address)
	}
	ip := net.ParseIP(host)
	if listen {
		if ip == nil || ip.To4() == nil {
			return "", 0, fmt.Errorf("监听地址必须是本地 IPv4：%q", address)
		}
	} else {
		if host == "" || strings.ContainsAny(host, "/\\ \t\r\n") || (ip != nil && (ip.To4() == nil || ip.IsUnspecified() || ip.IsMulticast())) {
			return "", 0, fmt.Errorf("公网地址无效：%q", address)
		}
	}
	return host, port, nil
}

func acquireDataLock(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, ".coordinator.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(file); err != nil {
		file.Close()
		return nil, fmt.Errorf("数据目录已被其他实例使用或无法加锁：%w", err)
	}
	return file, nil
}
