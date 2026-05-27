package device

import (
	"context"
)

type Null struct {
	name string
	mtu  int
}

func NewNull(name string, mtu int) *Null {
	if name == "" {
		name = "null0"
	}
	return &Null{name: name, mtu: mtu}
}

func (d *Null) Name() string { return d.name }

func (d *Null) MTU() int { return d.mtu }

func (d *Null) ReadPacket(ctx context.Context) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (d *Null) WritePacket([]byte) error { return nil }

func (d *Null) Close() error { return nil }
