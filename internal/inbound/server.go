// Package inbound is the off-mesh inbound mail edge (research D2, contracts §B):
// a SEPARATE listener — not the mesh listeners and not gateway-proxied — that
// the mail relay posts raw RFC 822 messages to. This file holds the listener:
// TLS (or an explicit development opt-out), strict timeouts, a hard body cap,
// GET /healthz for the relay, and POST /inbound/mail delegated to the mail
// handler (authentication, routing, dedup, threading and ticket creation live
// in the handler). Every refusal is the same generic body so the edge never
// enumerates mailboxes or explains why a delivery was refused.
package inbound

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Paths served by the edge.
const (
	PathMail    = "/inbound/mail"
	PathHealthz = "/healthz"
)

// ServerConfig configures the edge listener.
type ServerConfig struct {
	Addr         string
	CertFile     string
	KeyFile      string
	Insecure     bool  // development only: plaintext HTTP
	MaxBodyBytes int64 // hard cap on a posted message
}

// Server is the inbound edge listener.
type Server struct {
	cfg     ServerConfig
	mail    http.Handler
	log     *slog.Logger
	handler http.Handler
}

// Refusal writes the single generic refusal every non-accepted delivery gets
// (no reason, no mailbox enumeration).
func Refusal(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"outcome":"refused"}` + "\n"))
}

// NewServer builds the edge; mail handles POST /inbound/mail (nil answers 503
// so the relay retries until the handler is wired).
func NewServer(cfg ServerConfig, mail http.Handler, log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Server{cfg: cfg, mail: mail, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+PathHealthz, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(`{"status":"ok"}` + "\n"))
	})
	mux.HandleFunc("POST "+PathMail, func(w http.ResponseWriter, r *http.Request) {
		if s.mail == nil {
			Refusal(w, http.StatusServiceUnavailable)
			return
		}
		if cfg.MaxBodyBytes > 0 {
			// The handler authenticates first and then refuses (and audits) an
			// oversized delivery; the reader enforces the cap either way.
			r.Body = http.MaxBytesReader(w, r.Body, cfg.MaxBodyBytes)
		}
		s.mail.ServeHTTP(w, r)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { Refusal(w, http.StatusNotFound) })
	s.handler = mux
	return s
}

// Handler returns the edge's HTTP handler (tests).
func (s *Server) Handler() http.Handler { return s.handler }

// httpServer builds the bounded http.Server.
func (s *Server) httpServer() *http.Server {
	return &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
	}
}

// TLSConfig loads the edge certificate (TLS 1.2+).
func (s *Server) TLSConfig() (*tls.Config, error) {
	if s.cfg.Insecure {
		return nil, nil
	}
	cert, err := tls.LoadX509KeyPair(s.cfg.CertFile, s.cfg.KeyFile)
	if err != nil {
		return nil, errors.New("inbound: load TLS certificate: " + err.Error())
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}, nil
}

// Serve runs the edge on lis until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, lis net.Listener) error {
	tlsCfg, err := s.TLSConfig()
	if err != nil {
		_ = lis.Close()
		return err
	}
	if tlsCfg != nil {
		lis = tls.NewListener(lis, tlsCfg)
	} else {
		s.log.Warn("inbound edge: serving WITHOUT TLS (inbound.insecure_dev)")
	}
	srv := s.httpServer()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second) // ctx is already done: shut down with a fresh deadline
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Run listens on the configured address and serves until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	lis, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return err
	}
	return s.Serve(ctx, lis)
}
