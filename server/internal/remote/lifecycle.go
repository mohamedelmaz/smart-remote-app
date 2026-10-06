package remote

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// Start binds the listener and serves until Shutdown is called.
//
// Binding is separated from serving so the caller can discover the actual
// port when 0 was requested, which the tests and the --port=0 case rely on.
func (s *Server) Start() error {
	addr := fmt.Sprintf(":%d", s.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("remote: listen on %s: %w", addr, err)
	}
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
		s.port = tcp.Port
	}

	s.httpSrv = &http.Server{
		Handler: s.Handler(),
		// No WriteTimeout: the MJPEG stream and WebSocket connections are
		// long-lived by design, and a global write timeout would kill them
		// mid-session. ReadHeaderTimeout still protects against Slowloris.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          nil,
	}

	s.logger.Printf("listening on http://%s (dashboard, API and websocket)",
		ln.Addr().String())

	go func() {
		if err := s.httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Printf("http server stopped: %v", err)
		}
	}()
	return nil
}

// SanitizeLabel exposes DNS label sanitising to the entry point.
func SanitizeLabel(s string) string { return sanitizeLabel(s) }

// HostLabel returns a DNS-safe hostname for the mDNS SRV and A records.
func HostLabel() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "smart-remote.local"
	}
	name := sanitizeLabel(host)
	if name == "device" {
		return "smart-remote.local"
	}
	return name + ".local"
}

// Listen starts the HTTP server and blocks until the context is cancelled.
//
// It is the convenience entry point used by main; tests drive Start and
// Shutdown separately so they can bind an ephemeral port.
func (s *Server) Listen(ctx context.Context) error {
	if err := s.Start(); err != nil {
		return err
	}
	s.WaitForShutdown(ctx)
	return nil
}

// Addr returns the bound address, useful when the port was auto-assigned.
func (s *Server) Addr() string {
	return fmt.Sprintf(":%d", s.port)
}

// Port returns the bound port.
func (s *Server) Port() int { return s.port }

// Shutdown stops the HTTP server, mDNS and the audio device.
//
// The context bounds the graceful drain so a stuck screen stream cannot
// prevent the process from exiting.
func (s *Server) Shutdown(ctx context.Context) {
	s.once.Do(func() {
		close(s.shutdown)
		if s.mdns != nil {
			s.mdns.Stop()
		}
		if s.audio != nil {
			if err := s.audio.Close(); err != nil {
				s.logger.Printf("audio close: %v", err)
			}
		}
		if s.httpSrv != nil {
			if err := s.httpSrv.Shutdown(ctx); err != nil {
				s.logger.Printf("http shutdown: %v", err)
			}
		}
		// Release the viewer locks so a later start is not refused because
		// of a stale holder from the previous run - including the webcam
		// lock, which used to be forgotten here entirely.
		s.viewer.Reset()
		s.webcamViewer.Reset()
		s.logger.Printf("shutdown complete")
	})
}

// WaitForShutdown blocks until ctx is done, then shuts down gracefully.
func (s *Server) WaitForShutdown(ctx context.Context) {
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.Shutdown(shutdownCtx)
}
