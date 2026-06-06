package devmint

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain handles the fake-cloudflared subprocess pattern. When the test
// binary is invoked with FAKE_CLOUDFLARED set, it acts as cloudflared and
// exits. This lets CI-safe tests spawn themselves as a fake binary.
func TestMain(m *testing.M) {
	switch os.Getenv("FAKE_CLOUDFLARED") {
	case "emit-url":
		fmt.Fprintln(os.Stderr, "https://test-tunnel-123.trycloudflare.com")
		// Block until killed — StartTunnel's cleanup will kill us.
		select {}
	case "exit-error":
		os.Exit(1)
	case "sleep":
		select {}
	}
	os.Exit(m.Run())
}

// fakeCloudflaredBinary returns the path to the current test binary so it can
// be used as a fake cloudflared, and returns the PATH that finds it.
func fakeCloudflaredBinary(t *testing.T) (dir string) {
	t.Helper()
	// The test binary is the current executable.
	exe, err := os.Executable()
	require.NoError(t, err)

	dir = t.TempDir()
	// Symlink (or copy) the test binary as "cloudflared".
	link := dir + "/cloudflared"
	require.NoError(t, os.Symlink(exe, link))
	return dir
}

func TestStartTunnel_CloudflaredNotInPath(t *testing.T) {
	t.Setenv("PATH", "")
	_, _, err := StartTunnel(context.Background(), 8321, log.New(io.Discard, "", 0))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cloudflared not found in PATH")
}

func TestStartTunnel_URLExtracted(t *testing.T) {
	dir := fakeCloudflaredBinary(t)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CLOUDFLARED", "emit-url")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	url, cleanup, err := StartTunnel(ctx, 8321, log.New(io.Discard, "", 0))
	require.NoError(t, err)
	assert.Equal(t, "https://test-tunnel-123.trycloudflare.com", url)
	cleanup()
}

func TestStartTunnel_EarlyExit(t *testing.T) {
	dir := fakeCloudflaredBinary(t)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CLOUDFLARED", "exit-error")

	_, _, err := StartTunnel(context.Background(), 8321, log.New(io.Discard, "", 0))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cloudflared exited")
}

func TestStartTunnel_ContextCancelled(t *testing.T) {
	if _, err := exec.LookPath("cloudflared"); err != nil {
		// Use fake binary when real cloudflared is not installed.
		dir := fakeCloudflaredBinary(t)
		t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
		t.Setenv("FAKE_CLOUDFLARED", "sleep")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, _, err := StartTunnel(ctx, 8321, log.New(io.Discard, "", 0))
	require.Error(t, err)
}
