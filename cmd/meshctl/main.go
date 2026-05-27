package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"meshlink/internal/certutil"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "init-ca":
		err = initCA(os.Args[2:])
	case "issue":
		err = issue(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `meshctl commands:
  init-ca -out certs [-name mesh-ca] [-days 3650]
  issue -out certs -name home -ca certs/ca.pem -ca-key certs/ca-key.pem [-dns host.example.com] [-ip 192.0.2.10] [-days 825]`)
}

func initCA(args []string) error {
	fs := flag.NewFlagSet("init-ca", flag.ContinueOnError)
	out := fs.String("out", "certs", "output directory")
	name := fs.String("name", "mesh-ca", "certificate common name")
	days := fs.Int("days", 3650, "validity in days")
	if err := fs.Parse(args); err != nil {
		return err
	}

	result, err := certutil.InitCA(certutil.CAOptions{
		OutDir: *out,
		Name:   *name,
		Days:   *days,
	})
	if err != nil {
		return err
	}
	fmt.Printf("created CA files: %v\n", result.Files)
	return nil
}

func issue(args []string) error {
	fs := flag.NewFlagSet("issue", flag.ContinueOnError)
	out := fs.String("out", "certs", "output directory")
	name := fs.String("name", "", "node name")
	caPath := fs.String("ca", filepath.Join("certs", "ca.pem"), "CA certificate")
	caKeyPath := fs.String("ca-key", filepath.Join("certs", "ca-key.pem"), "CA private key")
	dnsNames := fs.String("dns", "", "comma-separated DNS SANs")
	ipAddrs := fs.String("ip", "", "comma-separated IP SANs")
	days := fs.Int("days", 825, "validity in days")
	if err := fs.Parse(args); err != nil {
		return err
	}

	result, err := certutil.Issue(certutil.IssueOptions{
		OutDir:    *out,
		Name:      *name,
		CAPath:    *caPath,
		CAKeyPath: *caKeyPath,
		DNSNames:  certutil.SplitCSV(*dnsNames),
		IPAddrs:   certutil.SplitCSV(*ipAddrs),
		Days:      *days,
	})
	if err != nil {
		return err
	}
	fmt.Printf("issued node certificate files: %v\n", result.Files)
	return nil
}
