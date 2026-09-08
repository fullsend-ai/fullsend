package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/fullsend-ai/fullsend/internal/cli"
)

// exitCoder allows commands to signal specific process exit codes.
// Errors implementing this interface cause the CLI to exit with the
// returned code instead of the default 1.
type exitCoder interface {
	ExitCode() int
}

// signalContext returns a context that is cancelled on the first
// SIGINT/SIGTERM, plus a cleanup function that stops signal forwarding.
// Subsequent signals are absorbed to prevent the default terminate handler
// from killing the process before cleanup (metrics, telemetry) completes.
//
// GitHub Actions sends SIGINT, waits ~7.5 s, then SIGTERM; without absorbing
// the second signal the default handler terminates the process before the
// metrics/telemetry flush path finishes (#6936).
//
// signal.NotifyContext stops listening after the first signal, which
// re-enables the default "terminate" handler for subsequent deliveries.
// Keeping our own channel registered prevents that.
func signalContext() (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh // First signal: cancel the context.
		cancel()
		for range sigCh { // Subsequent signals: absorbed.
		}
	}()
	return ctx, func() { signal.Stop(sigCh) }
}

func main() {
	ctx, cleanup := signalContext()
	defer cleanup()

	if err := cli.Execute(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		var ec exitCoder
		if errors.As(err, &ec) {
			os.Exit(ec.ExitCode())
		}
		os.Exit(1)
	}
}
