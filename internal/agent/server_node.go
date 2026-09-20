package agent

import (
	"context"
	"fmt"
	"net"
	"path/filepath"

	"meshlink/internal/config"
	"meshlink/internal/p2p"
)

func (a *Agent) newServerNode(settings options) (*Agent, error) {
	path := a.cfg.ServerNodeConfig
	if !filepath.IsAbs(path) {
		path = filepath.Join(a.baseDir, "configs", path)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, fmt.Errorf("load server mesh node: %w", err)
	}
	if cfg.Mode != "spoke" {
		return nil, fmt.Errorf("server mesh node must be a spoke")
	}
	for _, value := range []*string{&cfg.CAFile, &cfg.CertFile, &cfg.KeyFile} {
		if !filepath.IsAbs(*value) {
			*value = filepath.Join(filepath.Dir(path), *value)
		}
	}
	statusPath := ""
	if settings.statusPath != "" {
		statusPath = settings.statusPath + ".server-node.json"
	}
	child, err := New(cfg, a.log, WithBaseDir(a.baseDir), WithStatusPath(statusPath), WithDevice(settings.device))
	if err != nil {
		return nil, fmt.Errorf("create server mesh node: %w", err)
	}
	child.localServerNode = true
	return child, nil
}

type probeSocket interface {
	read(context.Context, []byte) (int, *net.UDPAddr, error)
	write([]byte, *net.UDPAddr) (int, error)
	LocalAddr() net.Addr
	Close() error
}

type udpProbeSocket struct{ *net.UDPConn }

func (s udpProbeSocket) read(_ context.Context, b []byte) (int, *net.UDPAddr, error) {
	return s.ReadFromUDP(b)
}
func (s udpProbeSocket) write(b []byte, a *net.UDPAddr) (int, error) { return s.WriteToUDP(b, a) }

// quic-go owns the sole UDP reader and separates QUIC from authenticated probes.
// The host never runs a rendezvous Refresh on this transport, so no competing
// non-QUIC reader can consume a remote client's probe response.
type sharedProbeSocket struct{ service *p2p.CandidateService }

func (s sharedProbeSocket) read(ctx context.Context, b []byte) (int, *net.UDPAddr, error) {
	n, addr, err := s.service.Transport().ReadNonQUICPacket(ctx, b)
	if err != nil {
		return 0, nil, err
	}
	source, ok := addr.(*net.UDPAddr)
	if !ok {
		return 0, nil, fmt.Errorf("non-UDP probe source")
	}
	return n, source, nil
}
func (s sharedProbeSocket) write(b []byte, a *net.UDPAddr) (int, error) {
	return s.service.Transport().WriteTo(b, a)
}
func (s sharedProbeSocket) LocalAddr() net.Addr { return s.service.LocalAddr() }
func (s sharedProbeSocket) Close() error        { return s.service.Transport().Close() }
