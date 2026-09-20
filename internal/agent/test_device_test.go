package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"
)

// channelTUNDevice is a packet-level test device: packets injected into readQ
// are consumed by the production TUN reader, and production deliveries appear
// on writeQ. It never marks a peer direct or substitutes for a network path.
type channelTUNDevice struct {
	name      string
	mtu       int
	readQ     chan []byte
	writeQ    chan []byte
	closed    chan struct{}
	closeOnce sync.Once
	onInject  func([]byte)
}

func newChannelTUNDevice(name string, mtu, capacity int, onInject func([]byte)) *channelTUNDevice {
	return &channelTUNDevice{
		name:     name,
		mtu:      mtu,
		readQ:    make(chan []byte, capacity),
		writeQ:   make(chan []byte, capacity),
		closed:   make(chan struct{}),
		onInject: onInject,
	}
}

func (d *channelTUNDevice) Name() string { return d.name }
func (d *channelTUNDevice) MTU() int     { return d.mtu }

func (d *channelTUNDevice) ReadPacket(ctx context.Context) ([]byte, error) {
	select {
	case packet := <-d.readQ:
		return append([]byte(nil), packet...), nil
	case <-d.closed:
		return nil, net.ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (d *channelTUNDevice) WritePacket(packet []byte) error {
	copyPacket := append([]byte(nil), packet...)
	select {
	case d.writeQ <- copyPacket:
		return nil
	case <-d.closed:
		return net.ErrClosed
	}
}

func (d *channelTUNDevice) Close() error {
	d.closeOnce.Do(func() { close(d.closed) })
	return nil
}

func (d *channelTUNDevice) inject(ctx context.Context, packet []byte) error {
	copyPacket := append([]byte(nil), packet...)
	if d.onInject != nil {
		d.onInject(copyPacket)
	}
	select {
	case d.readQ <- copyPacket:
		return nil
	case <-d.closed:
		return net.ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *channelTUNDevice) receive(ctx context.Context) ([]byte, error) {
	select {
	case packet := <-d.writeQ:
		return packet, nil
	case <-d.closed:
		return nil, net.ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (d *channelTUNDevice) drain() [][]byte {
	var packets [][]byte
	for {
		select {
		case packet := <-d.writeQ:
			packets = append(packets, packet)
		default:
			return packets
		}
	}
}

func assertChannelPacket(t *testing.T, d *channelTUNDevice, want []byte, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	got, err := d.receive(ctx)
	if err != nil {
		t.Fatalf("receive packet from %s: %v", d.name, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("packet delivered to %s = %x, want %x", d.name, got, want)
	}
}

func assertChannelQuiet(t *testing.T, d *channelTUNDevice, duration time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	packet, err := d.receive(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("device %s unexpectedly delivered %x (err=%v)", d.name, packet, err)
	}
}

func assertChannelNoPacketOrClosed(t *testing.T, d *channelTUNDevice, duration time.Duration) {
	t.Helper()
	select {
	case packet := <-d.writeQ:
		t.Fatalf("device %s unexpectedly delivered %x", d.name, packet)
	default:
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case packet := <-d.writeQ:
		t.Fatalf("device %s unexpectedly delivered %x", d.name, packet)
	case <-d.closed:
		return
	case <-timer.C:
		return
	}
}

func receiveExactPacket(ctx context.Context, d *channelTUNDevice, want []byte) error {
	for {
		packet, err := d.receive(ctx)
		if err != nil {
			return err
		}
		if bytes.Equal(packet, want) {
			return nil
		}
		return fmt.Errorf("device %s delivered %x, want %x", d.name, packet, want)
	}
}
