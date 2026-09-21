package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"path/filepath"
	"sync"

	"meshlink/internal/config"
	"meshlink/internal/device"
	"meshlink/internal/netsetup"
	"meshlink/internal/networkstate"
	"meshlink/internal/proto"
)

type Agent struct {
	localMAC            func() string
	cfg                 *config.Config
	log                 *slog.Logger
	dev                 device.Device
	hello               proto.Hello
	routes              []netip.Prefix
	status              *statusStore
	baseDir             string
	runMode             func(context.Context) error
	peerRuntimeMu       sync.RWMutex
	peerRuntime         *peerRuntime
	peerTimings         peerSessionTimings
	deviceCloseOnce     sync.Once
	deviceCloseErr      error
	serverNode          *Agent
	localServerNode     bool
	serverPublicAddress netip.AddrPort
}

func (a *Agent) closeDevice() error {
	a.deviceCloseOnce.Do(func() {
		if a.dev != nil {
			a.deviceCloseErr = a.dev.Close()
		}
	})
	return a.deviceCloseErr
}

type Option func(*options)

type options struct {
	localMAC   func() string
	statusPath string
	baseDir    string
	device     device.Device
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

func WithDevice(dev device.Device) Option {
	return func(opts *options) {
		opts.device = dev
	}
}

func New(cfg *config.Config, logger *slog.Logger, opts ...Option) (*Agent, error) {
	var settings options
	for _, opt := range opts {
		opt(&settings)
	}
	var dev device.Device
	var hello proto.Hello
	var routes []netip.Prefix
	if cfg.Mode == "spoke" {
		dev = settings.device
		var err error
		if dev == nil {
			dev, err = device.Open(cfg.Device, cfg.MTU, logger)
			if err != nil {
				return nil, err
			}
		}
		if err := netsetup.Apply(dev.Name(), cfg.VirtualIP, cfg.Device.Type, cfg.Setup, logger); err != nil {
			_ = dev.Close()
			return nil, err
		}

		hello = proto.Hello{
			NodeID:    cfg.NodeID,
			VirtualIP: cfg.VirtualIP,
			MTU:       cfg.MTU,
		}
		routes = make([]netip.Prefix, 0, len(cfg.Routes))
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
	}

	a := &Agent{
		localMAC: settings.localMAC,
		cfg:      cfg,
		log:      logger.With("node", cfg.NodeID, "mode", cfg.Mode),
		dev:      dev,
		hello:    hello,
		routes:   routes,
		status:   newStatusStore(settings.statusPath, nodeStatusFromConfig(cfg, cfg.CertFile)),
		baseDir:  settings.baseDir,
	}
	if cfg.Mode == "hub" && cfg.ServerNodeConfig != "" {
		child, err := a.newServerNode(settings)
		if err != nil {
			return nil, err
		}
		a.serverNode = child
	}
	return a, nil
}

func BaseDirFromConfigPath(configPath string) string {
	configDir := filepath.Dir(configPath)
	if filepath.Base(configDir) == "configs" {
		return filepath.Dir(configDir)
	}
	return configDir
}

func (a *Agent) Run(ctx context.Context) (err error) {
	if a.serverNode != nil {
		defer a.serverNode.closeDevice()
		defer a.serverNode.status.terminate()
	}
	if a.dev != nil {
		a.log.Info("agent starting", "device", a.dev.Name(), "mtu", a.dev.MTU())
		defer a.closeDevice()
	} else {
		a.log.Info("coordinator starting")
	}
	a.status.setState("starting")
	defer func() {
		if statusErr := a.status.terminate(); statusErr != nil {
			err = errors.Join(err, fmt.Errorf("write terminal agent status: %w", statusErr))
		}
	}()
	if a.runMode != nil {
		a.status.setState("running")
		return a.runMode(ctx)
	}

	switch a.cfg.Mode {
	case "hub":
		return a.runHub(ctx)
	case "spoke":
		a.status.setNetworkState(networkstate.Connecting)
		return a.runSpoke(ctx)
	default:
		return fmt.Errorf("unknown mode %q", a.cfg.Mode)
	}
}
