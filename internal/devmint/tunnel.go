package devmint

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"regexp"
	"time"
)

// StartTunnel launches a cloudflared tunnel exposing localhost:port and returns
// the public HTTPS URL, a cleanup function, and any startup error.
func StartTunnel(ctx context.Context, port int, logger *log.Logger) (string, func(), error) {
	if _, err := exec.LookPath("cloudflared"); err != nil {
		return "", nil, fmt.Errorf("cloudflared not found in PATH — install from https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/")
	}

	cmd := exec.CommandContext(ctx, "cloudflared", "tunnel",
		"--url", fmt.Sprintf("http://localhost:%d", port),
		"--no-autoupdate",
	)

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", nil, fmt.Errorf("creating stderr pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("starting cloudflared: %w", err)
	}

	go io.Copy(io.Discard, stdout) //nolint:errcheck

	// cleanup only kills the process; cmd.Wait() is owned by the exitCh goroutine below.
	cleanup := func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
	}

	tunnelURLPattern := regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

	urlCh := make(chan string, 1)
	exitCh := make(chan error, 1)

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			logger.Printf("[TUNNEL] %s", line)
			if match := tunnelURLPattern.FindString(line); match != "" {
				select {
				case urlCh <- match:
				default:
				}
			}
		}
	}()

	go func() {
		exitCh <- cmd.Wait()
	}()

	select {
	case url := <-urlCh:
		return url, cleanup, nil
	case err := <-exitCh:
		if err != nil {
			return "", nil, fmt.Errorf("cloudflared exited: %w", err)
		}
		return "", nil, fmt.Errorf("cloudflared exited unexpectedly without emitting a tunnel URL")
	case <-time.After(30 * time.Second):
		cleanup()
		return "", nil, fmt.Errorf("timed out waiting for cloudflared tunnel URL")
	case <-ctx.Done():
		cleanup()
		return "", nil, ctx.Err()
	}
}
