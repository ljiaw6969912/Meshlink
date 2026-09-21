package main

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func staging(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range allowed {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture "+name), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestArchiveModesContentAndOverwrite(t *testing.T) {
	source := staging(t)
	output := filepath.Join(t.TempDir(), "linux.tar.gz")
	for range 2 {
		if err := pack(source, output); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	count := 0
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		name := allowed[count]
		wantMode := int64(0644)
		if name == "bin/mesh-coordinator" {
			wantMode = 0755
		}
		if header.Name != "meshlink-linux/"+name || header.Mode != wantMode || header.Typeflag != tar.TypeReg {
			t.Fatalf("unexpected archive header: %+v", header)
		}
		data, err := io.ReadAll(tr)
		if err != nil || string(data) != "fixture "+name {
			t.Fatalf("bad payload for %s: %q, %v", name, data, err)
		}
		count++
	}
	if count != 7 {
		t.Fatalf("archive contains %d files; want 7", count)
	}
}

func TestRejectRuntimeDataAndPreserveExistingArchive(t *testing.T) {
	source := staging(t)
	output := filepath.Join(t.TempDir(), "linux.tar.gz")
	if err := os.WriteFile(output, []byte("previous package"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(source, "configs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := pack(source, output); err == nil {
		t.Fatal("accepted a runtime configuration directory")
	}
	data, err := os.ReadFile(output)
	if err != nil || string(data) != "previous package" {
		t.Fatalf("existing archive changed on failure: %q, %v", data, err)
	}
}
