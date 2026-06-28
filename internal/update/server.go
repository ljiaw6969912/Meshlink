package update

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
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

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/"+ManifestFile, http.StatusFound)
			return
		}
		http.FileServer(http.Dir(releaseDir)).ServeHTTP(w, r)
	})

	server := &http.Server{
		Addr:              listen,
		Handler:           logRequests(mux, logger),
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

func logRequests(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		logger.Info("update request", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr, "elapsed", time.Since(start))
	})
}
