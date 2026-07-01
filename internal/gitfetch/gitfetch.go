// Package gitfetch fetches directory trees from git repositories using
// sparse checkout, providing a forge-agnostic alternative to REST API
// directory listing.
package gitfetch

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

const (
	MaxFiles    = 1000
	maxTotalMiB = 50
	maxTotal    = maxTotalMiB * 1024 * 1024
)

// TreeFetchFunc fetches all files under path in a repository at ref.
// Returns map[relativePath]content. Token is optional — when empty the
// fetch is unauthenticated (sufficient for public repos).
type TreeFetchFunc func(ctx context.Context, cloneURL, path, ref, token string) (map[string][]byte, error)

// FetchTree fetches all files under path in a repository at ref using
// git sparse checkout. It creates a temporary shallow clone with only
// tree objects, configures sparse checkout for the target path, then
// reads the materialized files from disk.
func FetchTree(ctx context.Context, cloneURL, subpath, ref, token string) (map[string][]byte, error) {
	if cloneURL == "" {
		return nil, fmt.Errorf("gitfetch: clone URL is required")
	}
	u, err := url.Parse(cloneURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "file") {
		return nil, fmt.Errorf("gitfetch: unsupported URL scheme %q", cloneURL)
	}
	if ref == "" {
		return nil, fmt.Errorf("gitfetch: ref is required")
	}
	if err := validatePath(subpath); err != nil {
		return nil, fmt.Errorf("gitfetch: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "fullsend-gitfetch-*")
	if err != nil {
		return nil, fmt.Errorf("gitfetch: creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := runGit(ctx, tmpDir, "init"); err != nil {
		return nil, redactToken(fmt.Errorf("gitfetch: git init: %w", err), token)
	}
	if err := runGit(ctx, tmpDir, "remote", "add", "origin", cloneURL); err != nil {
		return nil, redactToken(fmt.Errorf("gitfetch: git remote add: %w", err), token)
	}
	var authEnv []string
	if token != "" {
		if err := validateToken(token); err != nil {
			return nil, fmt.Errorf("gitfetch: %w", err)
		}
		authEnv = []string{
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Bearer " + token,
		}
	}

	if subpath != "" {
		if err := runGitWithEnv(ctx, tmpDir, authEnv, "sparse-checkout", "init", "--cone"); err != nil {
			return nil, redactToken(fmt.Errorf("gitfetch: git sparse-checkout init: %w", err), token)
		}
		if err := runGitWithEnv(ctx, tmpDir, authEnv, "sparse-checkout", "set", subpath); err != nil {
			return nil, redactToken(fmt.Errorf("gitfetch: git sparse-checkout set: %w", err), token)
		}
	}

	fetchErr := runGitWithEnv(ctx, tmpDir, authEnv, "fetch", "--depth", "1", "--filter=blob:none", "origin", "--", ref)
	if fetchErr != nil && token != "" && isAuthError(fetchErr) {
		// The token may be scoped to a different repo. Retry without
		// authentication so public repos remain accessible.
		fetchErr = runGitWithEnv(ctx, tmpDir, nil, "fetch", "--depth", "1", "--filter=blob:none", "origin", "--", ref)
		if fetchErr == nil {
			// Unauthenticated fetch succeeded; clear authEnv so
			// subsequent checkout also runs without the token.
			authEnv = nil
		}
	}
	if fetchErr != nil {
		return nil, wrapTransient(redactToken(fmt.Errorf("gitfetch: git fetch: %w", fetchErr), token))
	}
	if err := runGitWithEnv(ctx, tmpDir, authEnv, "checkout", "FETCH_HEAD"); err != nil {
		return nil, wrapTransient(redactToken(fmt.Errorf("gitfetch: git checkout: %w", err), token))
	}

	walkRoot := tmpDir
	if subpath != "" {
		walkRoot = filepath.Join(tmpDir, filepath.FromSlash(subpath))
	}

	absTmp, _ := filepath.Abs(tmpDir)
	absWalk, _ := filepath.Abs(walkRoot)
	if absWalk != absTmp && !strings.HasPrefix(absWalk, absTmp+string(os.PathSeparator)) {
		return nil, fmt.Errorf("gitfetch: path %q escapes repository root", subpath)
	}

	info, err := os.Stat(walkRoot)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("gitfetch: path %q not found in repository at ref %s", subpath, ref)
	}

	files := make(map[string][]byte)
	var totalBytes int64
	walkErr := filepath.WalkDir(walkRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks are not supported: %s", p)
		}
		if d.Type()&^os.ModeDir != 0 {
			return fmt.Errorf("unsupported file type at %s", p)
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}

		rel, err := filepath.Rel(walkRoot, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		if len(files) >= MaxFiles {
			return fmt.Errorf("directory listing exceeded maximum of %d files", MaxFiles)
		}

		data, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("reading %s: %w", rel, err)
		}

		totalBytes += int64(len(data))
		if totalBytes > maxTotal {
			return fmt.Errorf("total content size exceeds %d MiB limit", maxTotalMiB)
		}

		files[rel] = data
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("gitfetch: %w", walkErr)
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("gitfetch: path %q contains no files at ref %s", subpath, ref)
	}

	return files, nil
}

func validatePath(p string) error {
	if p == "" {
		return nil
	}
	if filepath.IsAbs(p) {
		return fmt.Errorf("path must be relative, got %q", p)
	}
	cleaned := path.Clean(p)
	for _, segment := range strings.Split(cleaned, "/") {
		if segment == ".." {
			return fmt.Errorf("path must not contain '..': %q", p)
		}
	}
	return nil
}

func runGit(ctx context.Context, dir string, args ...string) error {
	return runGitWithEnv(ctx, dir, nil, args...)
}

func runGitWithEnv(ctx context.Context, dir string, extraEnv []string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Env = append(cmd.Env, extraEnv...)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

// TransientError wraps errors caused by temporary network conditions
// (DNS failures, connection refused, etc.) detected from git stderr.
type TransientError struct {
	Err error
}

func (e *TransientError) Error() string { return e.Err.Error() }
func (e *TransientError) Unwrap() error { return e.Err }

// authErrorPatterns are stderr substrings that indicate the git fetch
// failed due to authentication/authorization (e.g. a token scoped to a
// different repo). When detected, FetchTree retries without the token
// so that public repos remain accessible.
var authErrorPatterns = []string{
	"could not read username",
	"authentication failed",
	"invalid credentials",
	"authorization failed",
	"the requested url returned error: 401",
	"the requested url returned error: 403",
}

// isAuthError returns true when the error message indicates a git
// authentication or authorization failure.
func isAuthError(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, p := range authErrorPatterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

var transientPatterns = []string{
	"connection refused",
	"connection reset",
	"no such host",
	"network is unreachable",
	"temporary failure",
	"i/o timeout",
	"deadline exceeded",
	"context canceled",
}

func wrapTransient(err error) error {
	msg := strings.ToLower(err.Error())
	for _, p := range transientPatterns {
		if strings.Contains(msg, p) {
			return &TransientError{Err: err}
		}
	}
	return err
}

func validateToken(token string) error {
	for _, b := range []byte(token) {
		if b < 0x20 && b != '\t' {
			return fmt.Errorf("token contains invalid characters")
		}
	}
	return nil
}

func redactToken(err error, token string) error {
	if token == "" || err == nil {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), token, "***")
	if encoded := url.QueryEscape(token); encoded != token {
		msg = strings.ReplaceAll(msg, encoded, "***")
	}
	return fmt.Errorf("%s", msg)
}
