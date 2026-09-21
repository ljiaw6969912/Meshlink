package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func RunServer(ctx context.Context, listen, releaseDir string, logger *slog.Logger) error {
	if listen == "" {
		listen = DefaultListen
	}
	if releaseDir == "" {
		releaseDir = "release"
	}
	if logger == nil {
		logger = slog.Default()
	}
	releaseDir, err := filepath.Abs(releaseDir)
	if err != nil {
		return err
	}
	if info, err := os.Stat(releaseDir); err != nil {
		return fmt.Errorf("release dir is not available: %w", err)
	} else if !info.IsDir() {
		return fmt.Errorf("release path is not a directory: %s", releaseDir)
	}

	server := &http.Server{
		Addr:              listen,
		Handler:           logRequests(releaseHandler(releaseDir), logger),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		logger.Info("update server listening", "addr", listen, "release_dir", releaseDir)
		errCh <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return ctx.Err()
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// NewServerHandler also supports mounting behind the coordinator's authenticated
// TLS listener. The caller strips its route prefix before forwarding requests.
func NewServerHandler(releaseDir string) (http.Handler, error) {
	absolute, err := filepath.Abs(releaseDir)
	if err != nil {
		return nil, err
	}
	return releaseHandler(absolute), nil
}

func ResolveReleaseDirectory(baseDir string) string {
	return filepath.Dir(filepath.Clean(baseDir))
}

// The release directory also contains live identities. Never expose it through
// a file server: only the validated manifest and its single archive are public.
func releaseHandler(releaseDir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path == "/healthz" {
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte("ok\n"))
			}
			return
		}
		// Reject arbitrary paths even while the publication is unavailable.
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name != ManifestFile && !(strings.HasPrefix(name, "meshlink-") && strings.HasSuffix(name, ".zip") && !strings.ContainsAny(name, "/\\")) {
			http.NotFound(w, r)
			return
		}
		raw, err := os.ReadFile(filepath.Join(releaseDir, ManifestFile))
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		var manifest Manifest
		if err != nil || json.Unmarshal(raw, &manifest) != nil || ValidateManifest(manifest) != nil {
			http.Error(w, "release unavailable", http.StatusServiceUnavailable)
			return
		}
		if name != ManifestFile && name != manifest.Package.File {
			http.NotFound(w, r)
			return
		}
		archive, err := os.Open(filepath.Join(releaseDir, "meshlink-无配置.zip"))
		if err != nil {
			http.Error(w, "release unavailable", http.StatusServiceUnavailable)
			return
		}
		defer archive.Close()
		info, err := archive.Stat()
		hash := sha256.New()
		if err != nil || !info.Mode().IsRegular() || info.Size() != manifest.Package.Size {
			http.Error(w, "release unavailable", http.StatusServiceUnavailable)
			return
		}
		if _, err = io.Copy(hash, archive); err != nil || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), manifest.Package.SHA256) {
			http.Error(w, "release publication in progress", http.StatusServiceUnavailable)
			return
		}
		if name == ManifestFile {
			w.Header().Set("Content-Type", "application/json")
			if r.Method == http.MethodGet {
				_, _ = w.Write(raw)
			}
			return
		}
		if _, err := archive.Seek(0, io.SeekStart); err != nil {
			http.Error(w, "release unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		http.ServeContent(w, r, manifest.Package.File, info.ModTime(), archive)
	})
}

func logRequests(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		logger.Info("update request", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr, "elapsed", time.Since(start))
	})
}
