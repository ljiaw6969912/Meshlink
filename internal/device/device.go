package device

import (
	"context"
	"fmt"
	"log/slog"

	"meshlink/internal/config"
)

const (
	TypeNull   = "null"
	TypeTUN    = "tun"
	TypeWintun = "wintun"
)

type Device interface {
	Name() string
	MTU() int
	ReadPacket(context.Context) ([]byte, error)
	WritePacket([]byte) error
	Close() error
}

func Open(cfg config.DeviceConfig, mtu int, logger *slog.Logger) (Device, error) {
	switch cfg.Type {
	case "", TypeNull:
		logger.Info("using null packet device; TLS control/data path can run, OS routing is disabled")
		return NewNull(cfg.Name, mtu), nil
	case TypeTUN, TypeWintun:
		return OpenTUN(cfg.Name, mtu, logger)
	default:
		return nil, fmt.Errorf("unknown device type %q", cfg.Type)
	}
}
