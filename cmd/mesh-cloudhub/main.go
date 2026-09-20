package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"meshlink/internal/cloudhub"
	"meshlink/internal/licensing"
	"meshlink/internal/relay"
	"meshlink/internal/version"
)

const defaultListen = "127.0.0.1:18080"
const defaultRelayListen = ""

func main() {
	var listen string
	var relayListen string
	var privateFlags privateLicenseFlags
	showVersion := flag.Bool("version", false, "print version")
	flag.StringVar(&listen, "listen", defaultListen, "HTTP listen address")
	flag.StringVar(&relayListen, "relay-listen", defaultRelayListen, "Relay data TCP listen address; empty disables the data listener")
	flag.StringVar(&privateFlags.DeploymentID, "private-deployment-id", "", "private deployment ID bound by the signed license")
	flag.StringVar(&privateFlags.OrganizationID, "private-organization-id", "", "private organization ID bound by the signed license")
	flag.StringVar(&privateFlags.LicenseFile, "private-license-file", "", "path to the persisted signed private license")
	flag.StringVar(&privateFlags.TrustedKeysFile, "private-trusted-keys-file", "", "path to trusted Ed25519 public keys")
	flag.Parse()

	if *showVersion {
		fmt.Println(version.Display())
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	privateManager, err := loadPrivateLicenseManager(privateFlags)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var serviceOptions []cloudhub.Option
	if privateManager != nil {
		serviceOptions = append(serviceOptions, cloudhub.WithPrivateLicenseManager(privateManager))
	}
	service := cloudhub.NewService(cloudhub.NewMemoryStore(), serviceOptions...)
	var relayRuntime *relay.Server
	var relayListener net.Listener
	var handlerOpts []cloudhub.ServerOption
	if relayListen != "" {
		var err error
		relayListener, err = net.Listen("tcp", relayListen)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		relayRuntime = relay.NewServer(service,
			relay.WithEndpoint(relayListener.Addr().String()),
			relay.WithErrorCallback(func(err error) {
				logger.Error("relay runtime error", "error", err)
			}),
		)
		revokeRelaySession := func(_ context.Context, session cloudhub.RelaySession) error {
			err := relayRuntime.RevokeRelaySession(session.ID)
			if errors.Is(err, relay.ErrNotFound) {
				return nil
			}
			return err
		}
		service.SetRelaySessionRevokedHook(revokeRelaySession)
		handlerOpts = append(handlerOpts,
			cloudhub.WithRelayEndpoint(relayListener.Addr().String()),
			cloudhub.WithRelayStatusProvider(func() cloudhub.RelayStatus {
				return cloudhub.RelayStatus{Enabled: true, Listen: relayRuntime.Endpoint()}
			}),
			cloudhub.WithRelaySessionCreatedHook(func(_ context.Context, result cloudhub.RelaySessionResult) error {
				return relayRuntime.AuthorizeRelaySession(result)
			}),
			cloudhub.WithRelaySessionClosedHook(revokeRelaySession),
		)
	} else {
		handlerOpts = append(handlerOpts, cloudhub.WithRelayStatusProvider(func() cloudhub.RelayStatus {
			return cloudhub.RelayStatus{Enabled: false}
		}))
	}
	server := &http.Server{
		Addr:              listen,
		Handler:           newHandlerForService(service, handlerOpts...),
		ReadHeaderTimeout: 5 * time.Second,
	}

	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()
	if relayRuntime != nil {
		go func() {
			logger.Info("starting mesh cloud hub relay listener", "listen", relayListener.Addr().String())
			if err := relayRuntime.Serve(ctx, relayListener); err != nil {
				logger.Error("relay listener stopped", "error", err)
				cancel()
			}
		}()
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	logger.Info("starting mesh cloud hub", "listen", listen)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newHandler(opts ...cloudhub.ServerOption) http.Handler {
	return newHandlerForService(cloudhub.NewService(cloudhub.NewMemoryStore()), opts...)
}

func newHandlerForService(service *cloudhub.Service, opts ...cloudhub.ServerOption) http.Handler {
	return cloudhub.NewServer(service, opts...)
}

type privateLicenseFlags struct {
	DeploymentID    string
	OrganizationID  string
	LicenseFile     string
	TrustedKeysFile string
}

func loadPrivateLicenseManager(flags privateLicenseFlags) (*licensing.Manager, error) {
	values := []string{flags.DeploymentID, flags.OrganizationID, flags.LicenseFile, flags.TrustedKeysFile}
	set := 0
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			set++
		}
	}
	if set == 0 {
		return nil, nil
	}
	if set != len(values) {
		return nil, fmt.Errorf("private deployment configuration requires deployment ID, organization ID, license file, and trusted public key file")
	}
	keys, err := licensing.LoadTrustedKeysFile(strings.TrimSpace(flags.TrustedKeysFile))
	if err != nil {
		return nil, fmt.Errorf("load private trusted public keys: %w", err)
	}
	manager, err := licensing.NewManager(licensing.ManagerConfig{
		Path: strings.TrimSpace(flags.LicenseFile), TrustedKeys: keys,
		Binding: licensing.Binding{OrganizationID: strings.TrimSpace(flags.OrganizationID), DeploymentID: strings.TrimSpace(flags.DeploymentID)},
	})
	if err != nil {
		return nil, fmt.Errorf("load signed private license: %w", err)
	}
	return manager, nil
}
