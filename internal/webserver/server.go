// Package webserver provides an HTTP server that serves the embedded SPA
// dashboard and exposes REST API endpoints.
package webserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"runtime"
	"time"
)

// Config holds the HTTP server configuration.
type Config struct {
	Port       int
	ResultsDir string
	NoBrowser  bool
	Logger     *slog.Logger
}

// Server wraps the HTTP server with configuration.
type Server struct {
	cfg    Config
	srv    *http.Server
	logger *slog.Logger
}

// New creates a new HTTP server with the given configuration.
func New(cfg Config) (*Server, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Port == 0 {
		cfg.Port = 3000
	}
	if cfg.ResultsDir == "" {
		cfg.ResultsDir = "."
	}

	mux := http.NewServeMux()
	s := &Server{
		cfg:    cfg,
		logger: cfg.Logger,
		srv: &http.Server{
			Addr:              fmt.Sprintf("127.0.0.1:%d", cfg.Port),
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
		},
	}

	if err := registerRoutes(mux, cfg); err != nil {
		return nil, err
	}
	return s, nil
}

// ListenAndServe starts the HTTP server and optionally opens a browser.
func (s *Server) ListenAndServe(ctx context.Context) error {
	url := fmt.Sprintf("http://localhost:%d", s.cfg.Port)
	s.logger.Info("HTTP server starting", "address", s.srv.Addr, "url", url)
	fmt.Printf("waza dashboard: %s\n", url)

	if !s.cfg.NoBrowser {
		// Open browser in background after a short delay.
		go func() {
			time.Sleep(500 * time.Millisecond)
			if err := openBrowser(url); err != nil {
				s.logger.Debug("failed to open browser", "error", err)
			}
		}()
	}

	// Graceful shutdown on context cancellation.
	go func() {
		<-ctx.Done()
		s.logger.Info("shutting down HTTP server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.srv.Shutdown(shutdownCtx); err != nil {
			s.logger.Error("HTTP server shutdown error", "error", err)
		}
	}()

	if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("HTTP server error: %w", err)
	}
	return nil
}

// Handler returns the underlying http.Handler (useful for testing).
func (s *Server) Handler() http.Handler {
	return s.srv.Handler
}

// openBrowser opens the given URL in the default browser.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return cmd.Start()
}
