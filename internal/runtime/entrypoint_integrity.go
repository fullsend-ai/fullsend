package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/security"
)

const entrypointIntegrityExit = 97

// EntrypointIntegrity holds runner-derived digests for runtime files uploaded
// into a Claude entrypoint sandbox. Expected values remain in the outer
// process; they are never read from a sandbox manifest.
type EntrypointIntegrity struct {
	files      map[string]string
	exactDirs  map[string][]string
	helperPath string
}

// NewEntrypointIntegrity records digests from the exact bytes passed to the
// upload path. exactDirs binds a directory to its complete expected filename
// set, catching missing files, symlinks, and unexpected entries.
func NewEntrypointIntegrity(files map[string][]byte, exactDirs map[string][]string, helperPath string) *EntrypointIntegrity {
	copyFiles := make(map[string]string, len(files))
	for path, content := range files {
		sum := sha256.Sum256(content)
		copyFiles[path] = hex.EncodeToString(sum[:])
	}
	copyDirs := make(map[string][]string, len(exactDirs))
	for dir, names := range exactDirs {
		copyDirs[dir] = append([]string(nil), names...)
		sort.Strings(copyDirs[dir])
	}
	return &EntrypointIntegrity{files: copyFiles, exactDirs: copyDirs, helperPath: helperPath}
}

// GuardCommand returns a POSIX-shell check. includeHelper is false for the
// in-sandbox child helper because self-authentication cannot be trusted; the
// outer runner checks the helper independently before and after execution.
func (i *EntrypointIntegrity) GuardCommand(includeHelper bool) string {
	if i == nil {
		return "false"
	}
	paths := make([]string, 0, len(i.files))
	for path := range i.files {
		if !includeHelper && path == i.helperPath {
			continue
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	checks := make([]string, 0, len(paths)*3+len(i.exactDirs)*2)
	for _, path := range paths {
		checks = append(checks,
			"test -f "+shellQuote(path),
			"test ! -L "+shellQuote(path),
			codexSHACheck(path, i.files[path]),
		)
	}
	dirs := make([]string, 0, len(i.exactDirs))
	for dir := range i.exactDirs {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		names := i.exactDirs[dir]
		checks = append(checks,
			fmt.Sprintf(`[ "$(command -p find %s -mindepth 1 -maxdepth 1 -print | command -p wc -l)" -eq %d ]`, shellQuote(dir), len(names)),
			fmt.Sprintf(`[ -z "$(command -p find %s -mindepth 1 ! -type f -print)" ]`, shellQuote(dir)),
		)
	}
	return fmt.Sprintf(`{ %s || { echo 'fullsend: Claude entrypoint integrity check failed; refusing to continue' >&2; exit %d; }; }`, strings.Join(checks, " && "), entrypointIntegrityExit)
}

// Check verifies the current sandbox files from the outer runner. OpenShell
// failures, nonzero checks, and missing output all fail closed.
func (i *EntrypointIntegrity) Check(ctx context.Context, sandboxName string) error {
	if i == nil {
		return fmt.Errorf("Claude entrypoint integrity state is unavailable")
	}
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	stdout, stderr, exitCode, err := sandbox.ExecContext(checkCtx, sandboxName, i.GuardCommand(true), 20*time.Second)
	if err != nil {
		return fmt.Errorf("checking Claude entrypoint integrity: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("checking Claude entrypoint integrity: exit %d (%s%s)", exitCode, strings.TrimSpace(stdout), strings.TrimSpace(stderr))
	}
	return nil
}

// EntrypointSecurityEnv captures the runner-owned hook settings and the
// harness-provided hook values while the runner's workspace .env is pristine.
// The Claude helper re-exports these after inheriting the environment loaded
// by Fullsend's entrypoint launcher, so writable-file edits cannot silently
// disable the configured checks.
func EntrypointSecurityEnv(sandboxName string, hooks security.SandboxHookConfig) (map[string]string, error) {
	// Pin effective values even when the harness omits them. RunEntrypoint
	// sources the writable workspace .env, so leaving a default out of the
	// helper's re-export list would let that file (or its inherited environment)
	// silently change the security policy.
	failOn := hooks.TirithFailOn()
	if failOn == "" {
		failOn = "high"
	}
	required := "0"
	if hooks.TirithRequired() {
		required = "1"
	}
	allowlist := hooks.SSRFEgressAllowlist()
	if entry := hooks.ForgeEgressEntry(); entry != "" {
		if allowlist == "" {
			allowlist = entry
		} else {
			allowlist += "," + entry
		}
	}
	env := map[string]string{
		"TIRITH_FAIL_ON":            failOn,
		"TIRITH_REQUIRED":           required,
		"FULLSEND_EGRESS_ALLOWLIST": allowlist,
	}
	for _, pair := range codexSecurityEnv(hooks) {
		env[pair.Key] = pair.Value
	}
	harnessEnv, err := codexReadHarnessSecurityEnv(sandboxName)
	if err != nil {
		return nil, err
	}
	for _, pair := range harnessEnv {
		env[pair.Key] = pair.Value
	}
	return env, nil
}
