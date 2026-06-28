package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	meshupdate "meshlink/internal/update"
	"meshlink/internal/version"
)

const defaultServiceName = "MeshlinkUpdateServer"

func main() {
	var listen string
	var releaseDir string
	var serviceAction string
	var serviceName string
	flag.StringVar(&listen, "listen", meshupdate.DefaultListen, "HTTP listen address")
	flag.StringVar(&releaseDir, "dir", defaultReleaseDir(), "release directory")
	flag.StringVar(&serviceAction, "service", "", "Windows service action: install, uninstall, start, stop, run")
	flag.StringVar(&serviceName, "service-name", defaultServiceName, "Windows service name")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()

	if *showVersion {
		fmt.Println(version.Display())
		return
	}

	if serviceAction != "" {
		if err := runServiceAction(serviceAction, serviceName, listen, releaseDir); err != nil {
			fmt.Fprintf(os.Stderr, "service %s: %v\n", serviceAction, err)
			os.Exit(1)
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := meshupdate.RunServer(ctx, listen, releaseDir, logger); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func defaultReleaseDir() string {
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if strings.EqualFold(filepath.Base(dir), "bin") {
			return filepath.Join(filepath.Dir(dir), "release")
		}
		return filepath.Join(dir, "release")
	}
	return "release"
}
