// linux-package builds a configuration-free Linux archive with explicit Unix modes.
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var allowed = []string{
	"bin/mesh-coordinator", "docs/linux-coordinator.md",
	"systemd/meshlink-coordinator.service", "LICENSE",
	"THIRD_PARTY_NOTICES.md", "VERSION", "build-metadata.json",
}

func main() {
	source := flag.String("source", "", "staging directory containing the allowlisted files")
	output := flag.String("output", "", "output .tar.gz path")
	flag.Parse()
	if *source == "" || *output == "" {
		fmt.Fprintln(os.Stderr, "-source and -output are required")
		os.Exit(2)
	}
	if err := pack(*source, *output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func fileMode(name string) int64 {
	if name == "bin/mesh-coordinator" {
		return 0755
	}
	return 0644
}

func pack(source, output string) error {
	contents := make(map[string][]byte, len(allowed))
	for _, name := range allowed {
		path := filepath.Join(source, filepath.FromSlash(name))
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("expected regular file: %s", name)
		}
		contents[name], err = os.ReadFile(path)
		if err != nil {
			return err
		}
	}
	// Refuse unexpected content even though only allowlisted files are archived.
	if err := filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		if entry.IsDir() && (name == "." || name == "bin" || name == "docs" || name == "systemd") {
			return nil
		}
		if _, ok := contents[name]; !ok || !entry.Type().IsRegular() {
			return fmt.Errorf("unexpected staging content: %s", name)
		}
		return nil
	}); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(output), ".linux-package-*.tar.gz")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, name := range allowed {
		data := contents[name]
		header := &tar.Header{Name: "meshlink-linux/" + name, Mode: fileMode(name), Size: int64(len(data)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := verify(temporary, contents); err != nil {
		return err
	}
	return os.Rename(temporary, output)
}

func verify(archive string, contents map[string][]byte) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for _, name := range allowed {
		header, err := tr.Next()
		if err != nil {
			return err
		}
		if header.Name != "meshlink-linux/"+name || header.Typeflag != tar.TypeReg || header.Mode != fileMode(name) || header.Size != int64(len(contents[name])) {
			return fmt.Errorf("archive header mismatch: %s", name)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return err
		}
		if sha256.Sum256(data) != sha256.Sum256(contents[name]) {
			return fmt.Errorf("archive hash mismatch: %s", name)
		}
	}
	if _, err := tr.Next(); err != io.EOF {
		return fmt.Errorf("unexpected archive trailer: %v", err)
	}
	// Consume the gzip trailer so checksum errors cannot be hidden by tar EOF.
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return err
	}
	return nil
}
