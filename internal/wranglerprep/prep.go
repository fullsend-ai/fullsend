// Package wranglerprep installs the pinned Wrangler once and tells child
// processes where it is. It has no dependency on the Cloudflare
// provisioner, so the behaviour-test driver (which must not import the
// mint core) can use it.
package wranglerprep

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fullsend-ai/fullsend/internal/dispatch/cf/workersrc"
)

// WranglerBinEnv names the environment variable that points at a prepared
// Wrangler executable. When set, every Wrangler invocation in the
// Cloudflare provisioner runs that executable directly instead of
// `npx wrangler`. Concurrent `fullsend mint deploy` processes otherwise
// race on the shared npx cache.
const WranglerBinEnv = "FULLSEND_WRANGLER_BIN"

// WranglerInstallFunc installs the Worker's locked dependencies, Wrangler
// among them, under dir so that dir/node_modules/.bin/wrangler exists.
type WranglerInstallFunc func(ctx context.Context, dir string) error

// npmEnvAllowed lists the environment variables npm needs to find itself,
// its configuration, its registry (including through a proxy) and the CA
// bundle. Names with an npm_config_ prefix are allowed too (case
// insensitively). Everything else, such as deployment credentials, GitHub
// App keys and actor tokens in the runner environment, is withheld.
var npmEnvAllowed = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LANG": true, "LC_ALL": true,
	"TMPDIR": true, "TEMP": true, "TMP": true, "USERPROFILE": true,
	"APPDATA": true, "LOCALAPPDATA": true, "SYSTEMROOT": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true,
	"http_proxy": true, "https_proxy": true, "no_proxy": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "NODE_EXTRA_CA_CERTS": true,
}

// npmInstallEnv returns the minimal environment for the npm install
// child: the allowed variables from environ ("KEY=value" entries).
func npmInstallEnv(environ []string) []string {
	var out []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if npmEnvAllowed[name] || strings.HasPrefix(strings.ToLower(name), "npm_config_") {
			out = append(out, kv)
		}
	}
	return out
}

// npmCI is the production WranglerInstallFunc. It writes the Worker's
// package.json and package-lock.json into dir and runs `npm ci` there, so
// the install is exactly what the lock file resolves. It disables
// lifecycle scripts and withholds the runner's credentials from npm and
// from any package code it fetches.
func npmCI(ctx context.Context, dir string) error {
	if err := os.WriteFile(filepath.Join(dir, "package.json"), workersrc.PackageJSON, 0o600); err != nil {
		return fmt.Errorf("writing package.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), workersrc.PackageLock, 0o600); err != nil {
		return fmt.Errorf("writing package-lock.json: %w", err)
	}
	cmd := exec.CommandContext(ctx, "npm", "ci", "--no-audit", "--no-fund", "--ignore-scripts")
	cmd.Dir = dir
	cmd.Env = npmInstallEnv(os.Environ())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("npm ci: %w\n%s", err, out)
	}
	return nil
}

// WranglerPreparer installs Wrangler at most once per instance, no matter
// how many goroutines call Prepare concurrently. All callers receive the
// same result. It uses no file locks, so it only serialises callers within
// one process (the BT runner), which then shares the path with children.
type WranglerPreparer struct {
	once    sync.Once
	path    string
	err     error
	baseDir string
	install WranglerInstallFunc
}

// NewWranglerPreparer returns a preparer that installs the Worker's locked
// dependencies under a fresh directory below baseDir (the system temp dir
// if empty) using install (`npm ci` if nil).
func NewWranglerPreparer(baseDir string, install WranglerInstallFunc) *WranglerPreparer {
	if install == nil {
		install = npmCI
	}
	return &WranglerPreparer{baseDir: baseDir, install: install}
}

// Prepare installs Wrangler once and returns the executable path
// (<dir>/node_modules/.bin/wrangler). Failures are cached and returned to
// every caller.
func (p *WranglerPreparer) Prepare(ctx context.Context) (string, error) {
	p.once.Do(func() { p.path, p.err = p.prepare(ctx) })
	return p.path, p.err
}

func (p *WranglerPreparer) prepare(ctx context.Context) (string, error) {
	dir, err := os.MkdirTemp(p.baseDir, "fullsend-wrangler-")
	if err != nil {
		return "", fmt.Errorf("creating wrangler install dir: %w", err)
	}
	if err := p.install(ctx, dir); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	bin := filepath.Join(dir, "node_modules", ".bin", "wrangler")
	if _, err := os.Stat(bin); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("wrangler executable missing after install: %w", err)
	}
	return bin, nil
}

var defaultWranglerPreparer = NewWranglerPreparer("", nil)

// PrepareWrangler installs the pinned Wrangler once per process and
// returns its executable path. Export the result as WranglerBinEnv so
// child `fullsend` processes skip npx.
func PrepareWrangler(ctx context.Context) (string, error) {
	return defaultWranglerPreparer.Prepare(ctx)
}

// PrepareWranglerEnv runs PrepareWrangler and exports WranglerBinEnv in the
// current process environment so child CLI processes inherit it. An
// already-set WranglerBinEnv is respected and no install is performed.
func PrepareWranglerEnv(ctx context.Context) error {
	return prepareWranglerEnv(ctx, PrepareWrangler)
}

func prepareWranglerEnv(ctx context.Context, prepare func(context.Context) (string, error)) error {
	if os.Getenv(WranglerBinEnv) != "" {
		return nil
	}
	bin, err := prepare(ctx)
	if err != nil {
		return fmt.Errorf("preparing wrangler: %w", err)
	}
	return os.Setenv(WranglerBinEnv, bin)
}
