package webserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

func (s *Server) serve(ctx context.Context, listen func() error) error {
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.srv.BaseContext = func(net.Listener) context.Context { return requestCtx }
	shutdown := make(chan error, 1)
	go func() {
		<-requestCtx.Done()
		s.logger.Info("shutting down HTTP server")
		// Native unit cleanup has its own five-second budget. Leave time to
		// publish its canceled envelope before the serving process returns.
		shutdownCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer stop()
		err := s.srv.Shutdown(shutdownCtx)
		if err != nil {
			err = errors.Join(err, s.srv.Close())
		}
		shutdown <- err
	}()
	serveErr := listen()
	cancel()
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	if err := <-shutdown; err != nil {
		serveErr = errors.Join(serveErr, fmt.Errorf("HTTP shutdown: %w", err))
	}
	if serveErr != nil {
		return fmt.Errorf("HTTP server error: %w", serveErr)
	}
	return nil
}
