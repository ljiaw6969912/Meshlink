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

	reconnectDelay := spokeReconnectMinDelay
	for {
		started := time.Now()
		err := a.connectOnce(ctx, tlsCfg, devicePackets)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case devErr := <-deviceErr:
			return devErr
		default:
		}
		if time.Since(started) >= spokeStableResetAfter {
			reconnectDelay = spokeReconnectMinDelay
		}
		wait := jitterDelay(reconnectDelay)
		a.log.Warn("spoke connection ended, reconnecting", "err", err, "delay", wait)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		reconnectDelay = nextReconnectDelay(reconnectDelay)
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
	connCtx, cancelConn := context.WithCancel(ctx)
	defer cancelConn()

	errCh := make(chan error, 3)
	sendErr := func(err error) {
		select {
		case errCh <- err:
		case <-connCtx.Done():
		}
	}

	var writeMu sync.Mutex
	write := func(typ byte, payload []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		if err := conn.SetWriteDeadline(time.Now().Add(peerWriteTimeout)); err != nil {
			return err
		}
		err := proto.Write(conn, typ, payload)
		_ = conn.SetWriteDeadline(time.Time{})
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
	if err := write(proto.TypeHello, hello); err != nil {
		return err
	}

	go func() {
		for {
			select {
			case <-connCtx.Done():
				sendErr(connCtx.Err())
				return
			case packet := <-devicePackets:
				if err := write(proto.TypePacket, packet); err != nil {
					sendErr(err)
					return
				}
			}
		}
	}()

	go func() {
		for {
			frame, err := readFrameWithTimeout(conn, peerReadTimeout)
			if err != nil {
				sendErr(err)
				return
			}
			a.status.touchPeer(peerID)
			switch frame.Type {
			case proto.TypePacket:
				if err := a.dev.WritePacket(frame.Payload); err != nil {
					sendErr(err)
					return
				}
			case proto.TypePing:
				if err := write(proto.TypePong, nil); err != nil {
					sendErr(err)
					return
				}
			case proto.TypePong:
			case proto.TypeRoster:
				roster, err := proto.ParseRoster(frame.Payload)
				if err != nil {
					sendErr(err)
					return
				}
				a.status.applyRoster(roster)
			default:
				sendErr(fmt.Errorf("unsupported frame type from hub: %d", frame.Type))
				return
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(peerHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-connCtx.Done():
				return
			case <-ticker.C:
				if err := write(proto.TypePing, nil); err != nil {
					sendErr(err)
					return
				}
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		cancelConn()
		if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
			return err
		}
		return err
	}
}

func nextReconnectDelay(delay time.Duration) time.Duration {
	if delay <= 0 {
		return spokeReconnectMinDelay
	}
	next := delay * 2
	if next > spokeReconnectMaxDelay {
		return spokeReconnectMaxDelay
	}
	return next
}

func jitterDelay(delay time.Duration) time.Duration {
	if delay <= 0 {
		return spokeReconnectMinDelay
	}
	window := delay / 5
	if window <= 0 {
		return delay
	}
	offset := time.Duration(time.Now().UnixNano()%int64(window*2+1)) - window
	result := delay + offset
	if result < spokeReconnectMinDelay {
		return spokeReconnectMinDelay
	}
	if result > spokeReconnectMaxDelay {
		return spokeReconnectMaxDelay
	}
	return result
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
