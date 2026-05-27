package device

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"

	wgtun "golang.zx2c4.com/wireguard/tun"
)

const maxPacketSize = 65535

type TUN struct {
	dev     wgtun.Device
	name    string
	mtu     int
	readMu  sync.Mutex
	writeMu sync.Mutex
}

func OpenTUN(name string, mtu int, logger *slog.Logger) (*TUN, error) {
	if name == "" {
		name = "mesh0"
	}
	dev, err := wgtun.CreateTUN(name, mtu)
	if err != nil {
		return nil, err
	}
	actualName, err := dev.Name()
	if err != nil {
		_ = dev.Close()
		return nil, err
	}
	actualMTU, err := dev.MTU()
	if err != nil {
		_ = dev.Close()
		return nil, err
	}
	logger.Info("opened packet device", "name", actualName, "mtu", actualMTU, "batch_size", dev.BatchSize())
	return &TUN{dev: dev, name: actualName, mtu: actualMTU}, nil
}

func (d *TUN) Name() string { return d.name }

func (d *TUN) MTU() int { return d.mtu }

func (d *TUN) ReadPacket(ctx context.Context) ([]byte, error) {
	d.readMu.Lock()
	defer d.readMu.Unlock()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		buf := make([]byte, maxPacketSize)
		bufs := [][]byte{buf}
		sizes := []int{0}
		n, err := d.dev.Read(bufs, sizes, 0)
		if err != nil {
			return nil, err
		}
		if n == 0 || sizes[0] == 0 {
			continue
		}
		packet := make([]byte, sizes[0])
		copy(packet, buf[:sizes[0]])
		return packet, nil
	}
}

func (d *TUN) WritePacket(packet []byte) error {
	if len(packet) == 0 {
		return nil
	}
	if len(packet) > maxPacketSize {
		return fmt.Errorf("packet too large: %d", len(packet))
	}

	d.writeMu.Lock()
	defer d.writeMu.Unlock()

	n, err := d.dev.Write([][]byte{packet}, 0)
	if err != nil {
		if err == io.ErrClosedPipe {
			return err
		}
		return err
	}
	if n != 1 {
		return fmt.Errorf("short packet write: wrote %d packets", n)
	}
	return nil
}

func (d *TUN) Close() error {
	return d.dev.Close()
}
