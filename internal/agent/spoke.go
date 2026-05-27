package agent

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"meshlink/internal/proto"
	"meshlink/internal/tlsutil"
)

func (a *Agent) runSpoke(ctx context.Context) error {
	tlsCfg, err := tlsutil.ClientConfig(a.cfg.CAFile, a.cfg.CertFile, a.cfg.KeyFile, a.cfg.ServerName)
	if err != nil {
		return err
	}

	devicePackets := make(chan []byte, 256)
	deviceErr := make(chan error, 1)
	go func() {
		deviceErr <- a.readDevicePackets(ctx, devicePackets)
	}()

	for {
		err := a.connectOnce(ctx, tlsCfg, devicePackets)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case devErr := <-deviceErr:
			return devErr
		default:
		}
		a.log.Warn("spoke connection ended, reconnecting soon", "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

func (a *Agent) connectOnce(ctx context.Context, tlsCfg *tls.Config, devicePackets <-chan []byte) error {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	raw, err := dialer.DialContext(ctx, "tcp4", a.cfg.Connect)
	if err != nil {
		return err
	}
	defer raw.Close()

	conn := tls.Client(raw, tlsCfg)
	if err := conn.HandshakeContext(ctx); err != nil {
		return err
	}
	commonName, fingerprint := certInfoFromState(conn.ConnectionState())
	peerID := commonName
	if peerID == "" {
		peerID = a.cfg.ServerName
	}
	if peerID == "" {
		peerID = "hub"
	}
	a.status.upsertPeer(PeerStatus{
		NodeID:      peerID,
		RemoteAddr:  raw.RemoteAddr().String(),
		Fingerprint: fingerprint,
		CommonName:  commonName,
		ConnectedAt: time.Now(),
	})
	defer a.status.removePeer(peerID)
	a.log.Info("connected to hub", "addr", a.cfg.Connect)

	hello, err := a.hello.Marshal()
	if err != nil {
		return err
	}
	if err := proto.Write(conn, proto.TypeHello, hello); err != nil {
		return err
	}

	errCh := make(chan error, 2)
	var writeMu sync.Mutex
	write := func(typ byte, payload []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return proto.Write(conn, typ, payload)
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			case packet := <-devicePackets:
				if err := write(proto.TypePacket, packet); err != nil {
					errCh <- err
					return
				}
			}
		}
	}()

	go func() {
		for {
			frame, err := proto.Read(conn)
			if err != nil {
				errCh <- err
				return
			}
			a.status.touchPeer(peerID)
			switch frame.Type {
			case proto.TypePacket:
				if err := a.dev.WritePacket(frame.Payload); err != nil {
					errCh <- err
					return
				}
			case proto.TypePing:
				if err := write(proto.TypePong, nil); err != nil {
					errCh <- err
					return
				}
			case proto.TypePong:
			case proto.TypeRoster:
				roster, err := proto.ParseRoster(frame.Payload)
				if err != nil {
					errCh <- err
					return
				}
				a.status.applyRoster(roster)
			default:
				errCh <- fmt.Errorf("unsupported frame type from hub: %d", frame.Type)
				return
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
			return err
		}
		return err
	}
}

func (a *Agent) readDevicePackets(ctx context.Context, out chan<- []byte) error {
	for {
		packet, err := a.dev.ReadPacket(ctx)
		if err != nil {
			return err
		}
		select {
		case out <- packet:
		default:
			a.log.Warn("dropping packet because spoke send queue is full")
		}
	}
}
