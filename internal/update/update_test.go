package update

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"0.1.1", "0.1.0", 1},
		{"0.1.0", "0.1.0", 0},
		{"0.1.0", "0.1.1", -1},
		{"1.0.0", "0.9.9", 1},
		{"1.0.0", "1.0.0-dev", 1},
		{"1.0.0-dev", "1.0.0", -1},
	}
	for _, tt := range tests {
		if got := CompareVersions(tt.a, tt.b); got != tt.want {
			t.Fatalf("CompareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	got, err := NormalizeBaseURL("10.77.0.1:1263/")
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://10.77.0.1:1263" {
		t.Fatalf("got %q", got)
	}
}

func TestCheckRejectsManifestPackageVersionMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/manifest.json" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
  "product": "Meshlink",
  "version": "0.1.1",
  "generated_at": "2026-07-07T00:00:00Z",
  "package": {
    "file": "meshlink-0.1.2.zip",
    "sha256": "abc",
    "size": 123
  }
}`))
	}))
	defer server.Close()

	_, err := Check(t.Context(), server.URL, "0.1.1")
	if err == nil {
		t.Fatal("expected manifest/package version mismatch error")
	}
	if !strings.Contains(err.Error(), "manifest version") {
		t.Fatalf("error = %q, want manifest version mismatch", err.Error())
	}
}
