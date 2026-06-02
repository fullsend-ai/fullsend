package devmint

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"
)

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

	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("starting cloudflared: %w", err)
	}

	cleanup := func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}

	urlCh := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			logger.Printf("[TUNNEL] %s", line)
			if idx := strings.Index(line, "https://"); idx >= 0 {
				candidate := line[idx:]
				if end := strings.IndexAny(candidate, " \t\n\r|"); end > 0 {
					candidate = candidate[:end]
				}
				if strings.Contains(candidate, "trycloudflare.com") {
					select {
					case urlCh <- candidate:
					default:
					}
				}
			}
		}
	}()

	select {
	case url := <-urlCh:
		return url, cleanup, nil
	case <-time.After(30 * time.Second):
		cleanup()
		return "", nil, fmt.Errorf("timed out waiting for cloudflared tunnel URL")
	case <-ctx.Done():
		cleanup()
		return "", nil, ctx.Err()
	}
}
