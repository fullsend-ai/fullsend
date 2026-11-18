package wranglerprep

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/dispatch/cf/workersrc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeInstaller returns an installer that counts calls and creates the
// executable the preparer expects.
func fakeInstaller(calls *atomic.Int32) WranglerInstallFunc {
	return func(_ context.Context, dir string) error {
		calls.Add(1)
		bin := filepath.Join(dir, "node_modules", ".bin")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(bin, "wrangler"), []byte("#!/bin/sh\n"), 0o755)
	}
}

func TestWranglerPreparer_ConcurrentCallersInstallOnce(t *testing.T) {
	var calls atomic.Int32
	p := NewWranglerPreparer(t.TempDir(), fakeInstaller(&calls))

	const n = 32
	paths := make([]string, n)
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // simulate N per-org mint deployments starting together
			paths[i], errs[i] = p.Prepare(context.Background())
		}()
	}
	close(start)
	wg.Wait()

	assert.EqualValues(t, 1, calls.Load(), "exactly one npm install")
	for i := 0; i < n; i++ {
		require.NoError(t, errs[i])
		assert.Equal(t, paths[0], paths[i], "all callers see the same path")
	}
	assert.True(t, strings.HasSuffix(paths[0], filepath.Join("node_modules", ".bin", "wrangler")))
	_, err := os.Stat(paths[0])
	assert.NoError(t, err)
}

func TestWranglerPreparer_InstallFailureIsCachedAndCleansUp(t *testing.T) {
	base := t.TempDir()
	var calls atomic.Int32
	p := NewWranglerPreparer(base, func(context.Context, string) error {
		calls.Add(1)
		return errors.New("npm exploded")
	})

	for i := 0; i < 3; i++ {
		path, err := p.Prepare(context.Background())
		require.ErrorContains(t, err, "npm exploded")
		assert.Empty(t, path)
	}
	assert.EqualValues(t, 1, calls.Load(), "failure is not retried per caller")
	entries, err := os.ReadDir(base)
	require.NoError(t, err)
	assert.Empty(t, entries, "failed install dir removed")
}

func TestWranglerPreparer_MissingBinaryAfterInstall(t *testing.T) {
	base := t.TempDir()
	p := NewWranglerPreparer(base, func(context.Context, string) error { return nil })
	_, err := p.Prepare(context.Background())
	require.ErrorContains(t, err, "executable missing after install")
	entries, _ := os.ReadDir(base)
	assert.Empty(t, entries)
}

func TestWranglerPreparer_BaseDirUnusable(t *testing.T) {
	var calls atomic.Int32
	p := NewWranglerPreparer(filepath.Join(t.TempDir(), "missing"), fakeInstaller(&calls))
	_, err := p.Prepare(context.Background())
	require.ErrorContains(t, err, "creating wrangler install dir")
	assert.Zero(t, calls.Load())
}

func TestNewWranglerPreparer_Defaults(t *testing.T) {
	p := NewWranglerPreparer("", nil)
	assert.NotNil(t, p.install)
}

func TestPrepareWrangler_UsesDefaultPreparer(t *testing.T) {
	var calls atomic.Int32
	orig := defaultWranglerPreparer
	defaultWranglerPreparer = NewWranglerPreparer(t.TempDir(), fakeInstaller(&calls))
	t.Cleanup(func() { defaultWranglerPreparer = orig })

	a, err := PrepareWrangler(context.Background())
	require.NoError(t, err)
	b, err := PrepareWrangler(context.Background())
	require.NoError(t, err)
	assert.Equal(t, a, b)
	assert.EqualValues(t, 1, calls.Load())
}

func TestPrepareWranglerEnv(t *testing.T) {
	ctx := context.Background()

	t.Run("exports prepared path", func(t *testing.T) {
		t.Setenv(WranglerBinEnv, "")
		require.NoError(t, prepareWranglerEnv(ctx, func(context.Context) (string, error) { return "/p/wrangler", nil }))
		assert.Equal(t, "/p/wrangler", os.Getenv(WranglerBinEnv))
	})

	t.Run("respects preset value", func(t *testing.T) {
		t.Setenv(WranglerBinEnv, "/preset")
		require.NoError(t, prepareWranglerEnv(ctx, func(context.Context) (string, error) {
			t.Fatal("must not prepare when env already set")
			return "", nil
		}))
		assert.Equal(t, "/preset", os.Getenv(WranglerBinEnv))
	})

	t.Run("failure leaves env unset", func(t *testing.T) {
		t.Setenv(WranglerBinEnv, "")
		err := prepareWranglerEnv(ctx, func(context.Context) (string, error) { return "", errors.New("boom") })
		require.ErrorContains(t, err, "preparing wrangler: boom")
		assert.Empty(t, os.Getenv(WranglerBinEnv))
	})

	t.Run("public wrapper short-circuits on preset", func(t *testing.T) {
		t.Setenv(WranglerBinEnv, "/preset")
		require.NoError(t, PrepareWranglerEnv(ctx))
	})
}

func TestNpmInstallEnv_WithholdsCredentials(t *testing.T) {
	got := npmInstallEnv([]string{
		"PATH=/usr/bin", "HOME=/home/x", "HTTPS_PROXY=http://p", "NPM_CONFIG_REGISTRY=https://r",
		"npm_config_cache=/c", "CLOUDFLARE_API_TOKEN=secret", "GH_TOKEN=secret", "APP_PRIVATE_KEY=secret",
	})
	assert.Equal(t, []string{"PATH=/usr/bin", "HOME=/home/x", "HTTPS_PROXY=http://p", "NPM_CONFIG_REGISTRY=https://r", "npm_config_cache=/c"}, got)
}

func TestNpmCI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake")
	}
	binDir := t.TempDir()
	outFile := filepath.Join(t.TempDir(), "out")
	// The install child gets a minimal environment, so the fake signals
	// failure through a file rather than a variable.
	failFile := filepath.Join(t.TempDir(), "fail")
	script := "#!/bin/sh\necho \"$@\" > " + outFile + "\npwd >> " + outFile +
		"\nls >> " + outFile + "\nif [ -e " + failFile + " ]; then echo nope; exit 1; fi\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "npm"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := t.TempDir()
	require.NoError(t, npmCI(context.Background(), dir))
	got, err := os.ReadFile(outFile)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(got)), "\n")
	require.Len(t, lines, 4)
	assert.Equal(t, "ci --no-audit --no-fund --ignore-scripts", lines[0])
	gotDir, err := filepath.EvalSymlinks(lines[1])
	require.NoError(t, err)
	wantDir, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	assert.Equal(t, wantDir, gotDir, "npm ci runs in the install dir")
	assert.Equal(t, []string{"package-lock.json", "package.json"}, lines[2:], "the Worker's manifest and lock are written first")
	for name, want := range map[string][]byte{"package.json": workersrc.PackageJSON, "package-lock.json": workersrc.PackageLock} {
		written, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		assert.Equal(t, want, written)
	}

	require.NoError(t, os.WriteFile(failFile, nil, 0o600))
	err = npmCI(context.Background(), t.TempDir())
	require.ErrorContains(t, err, "npm ci")
	assert.Contains(t, err.Error(), "nope")
}

func TestNpmCI_UnwritableDir(t *testing.T) {
	err := npmCI(context.Background(), filepath.Join(t.TempDir(), "missing"))
	require.ErrorContains(t, err, "writing package.json")
}
