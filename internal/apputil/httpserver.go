// Package apputil contains small shared helpers for repo binaries.
package apputil

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// StartHTTPServer starts an HTTP server and shuts it down when the context ends.
func StartHTTPServer(ctx context.Context, addr string, handler http.Handler, errCh chan<- error) {
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	go func() {
		err := server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
}
