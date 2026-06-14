package cli

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/fullsend-ai/fullsend/internal/fetchsvc"
)

// startFetchService starts an HTTP server that proxies runtime skill fetch
// requests from agents inside the sandbox to the fetchsvc handler. It returns
// the listener address, a bearer token for authentication, and a shutdown
// function that should be deferred by the caller.
func startFetchService(_ context.Context, cfg fetchsvc.ServiceConfig) (addr string, token string, shutdown func(), err error) {
	token, err = generateToken()
	if err != nil {
		return "", "", nil, fmt.Errorf("generating fetch service token: %w", err)
	}

	svc := fetchsvc.New(cfg)
	handler := withBearerAuth(token, svc)

	mux := http.NewServeMux()
	mux.Handle("/fetch", handler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// ADR-0046: bind to 0.0.0.0 so the sandbox container can reach the host.
	// Loopback binding will be possible once NVIDIA/OpenShell#1633 ships.
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return "", "", nil, fmt.Errorf("listening for fetch service: %w", err)
	}

	server := &http.Server{
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  60 * time.Second,
		ErrorLog:     log.New(log.Writer(), "fetchsvc: ", log.LstdFlags),
	}
	go func() {
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			server.ErrorLog.Printf("server error: %v", err)
		}
	}()

	shutdownFn := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	}

	return ln.Addr().String(), token, shutdownFn, nil
}

// withBearerAuth wraps an http.Handler with bearer token authentication.
// Uses timing-safe comparison to prevent token timing attacks.
func withBearerAuth(token string, next http.Handler) http.Handler {
	tokenBytes := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if len(auth) < len(prefix) || auth[:len(prefix)] != prefix {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		provided := []byte(auth[len(prefix):])
		if subtle.ConstantTimeCompare(provided, tokenBytes) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// generateToken produces a 32-byte hex-encoded random token.
func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}
