package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"meshlink/internal/runner"
	"meshlink/internal/winservice"
)

func main() {
	var configPath string
	var serviceAction string
	var serviceName string
	flag.StringVar(&configPath, "config", "", "path to agent JSON config")
	flag.StringVar(&serviceAction, "service", "", "Windows service action: install, uninstall, start, stop, run")
	flag.StringVar(&serviceName, "service-name", winservice.DefaultName, "Windows service name")
	flag.Parse()

	if serviceAction != "" {
		if err := runServiceAction(serviceAction, serviceName, configPath); err != nil {
			fmt.Fprintf(os.Stderr, "service %s: %v\n", serviceAction, err)
			os.Exit(1)
		}
		return
	}

	if configPath == "" {
		fmt.Fprintln(os.Stderr, "missing -config")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runner.Run(ctx, configPath, runner.ConsoleLogger(os.Stdout)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runServiceAction(action, name, configPath string) error {
	switch action {
	case "install":
		return winservice.Install(name, configPath)
	case "uninstall":
		return winservice.Uninstall(name)
	case "start":
		return winservice.Start(name)
	case "stop":
		return winservice.Stop(name)
	case "run":
		if configPath == "" {
			return fmt.Errorf("missing -config")
		}
		return winservice.Run(name, configPath)
	default:
		return fmt.Errorf("unknown service action %q", action)
	}
}
