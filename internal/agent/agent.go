package agent

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"path/filepath"

	"meshlink/internal/config"
	"meshlink/internal/device"
	"meshlink/internal/netsetup"
	"meshlink/internal/proto"
)

type Agent struct {
	cfg     *config.Config
	log     *slog.Logger
	dev     device.Device
	hello   proto.Hello
	routes  []netip.Prefix
	status  *statusStore
	baseDir string
}

type Option func(*options)

type options struct {
	statusPath string
	baseDir    string
}

func WithStatusPath(path string) Option {
	return func(opts *options) {
		opts.statusPath = path
	}
}

func WithBaseDir(path string) Option {
	return func(opts *options) {
		opts.baseDir = path
	}
}

func New(cfg *config.Config, logger *slog.Logger, opts ...Option) (*Agent, error) {
	var settings options
	for _, opt := range opts {
		opt(&settings)
	}
	dev, err := device.Open(cfg.Device, cfg.MTU, logger)
	if err != nil {
		return nil, err
	}
	if err := netsetup.Apply(dev.Name(), cfg.VirtualIP, cfg.Device.Type, cfg.Setup, logger); err != nil {
		_ = dev.Close()
		return nil, err
	}

	hello := proto.Hello{
		NodeID:    cfg.NodeID,
		VirtualIP: cfg.VirtualIP,
		MTU:       cfg.MTU,
	}
	routes := make([]netip.Prefix, 0, len(cfg.Routes))
	for _, route := range cfg.Routes {
		prefix, err := netip.ParsePrefix(route.CIDR)
		if err != nil {
			return nil, err
		}
		if !prefix.Addr().Is4() {
			return nil, fmt.Errorf("IPv6 routes are not supported: %s", route.CIDR)
		}
		routes = append(routes, prefix)
		hello.Routes = append(hello.Routes, prefix.String())
	}

	return &Agent{
		cfg:     cfg,
		log:     logger.With("node", cfg.NodeID, "mode", cfg.Mode),
		dev:     dev,
		hello:   hello,
		routes:  routes,
		status:  newStatusStore(settings.statusPath, nodeStatusFromConfig(cfg, cfg.CertFile)),
		baseDir: settings.baseDir,
	}, nil
}

func BaseDirFromConfigPath(configPath string) string {
	configDir := filepath.Dir(configPath)
	if filepath.Base(configDir) == "configs" {
		return filepath.Dir(configDir)
	}
	return configDir
}

func (a *Agent) Run(ctx context.Context) error {
	a.log.Info("agent starting", "device", a.dev.Name(), "mtu", a.dev.MTU())
	a.status.setState("running")
	defer a.dev.Close()
	defer a.status.setState("stopped")

	switch a.cfg.Mode {
	case "hub":
		return a.runHub(ctx)
	case "spoke":
		return a.runSpoke(ctx)
	default:
		return fmt.Errorf("unknown mode %q", a.cfg.Mode)
	}
}
