package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureAvailable_OpenshellNotInPath(t *testing.T) {
	// Save and clear PATH to ensure openshell is not found.
	t.Setenv("PATH", "")

	err := EnsureAvailable()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "openshell not found in PATH")
}

func TestConstants(t *testing.T) {
	assert.Equal(t, "/sandbox/workspace", SandboxWorkspace)
	assert.Equal(t, "/sandbox/claude-config", SandboxClaudeConfig)
	assert.Equal(t, "/sandbox/pi-config", SandboxPiConfig)
	assert.Equal(t, "/sandbox/codex-config", SandboxCodexConfig)
}

func TestBuildProviderArgs_BareKeyCredentials(t *testing.T) {
	t.Setenv("MY_SECRET", "super-secret-value")

	credentials := map[string]string{
		"API_KEY": "${MY_SECRET}",
	}
	config := map[string]string{
		"BASE_URL": "https://api.example.com",
	}

	args, extraEnv, secrets := buildProviderArgs("test-provider", "anthropic", credentials, config, false)

	// Args must use bare-key form: --credential API_KEY (no =value).
	assert.Contains(t, args, "--credential")
	for _, arg := range args {
		if strings.HasPrefix(arg, "API_KEY") {
			assert.Equal(t, "API_KEY", arg, "credential arg must be bare key, not KEY=VALUE")
		}
	}

	// Secret value must NOT appear anywhere in args.
	for _, arg := range args {
		assert.NotContains(t, arg, "super-secret-value",
			"secret value must not appear in CLI args")
	}

	// Secret value must be in extraEnv for the child process.
	require.Len(t, extraEnv, 1)
	assert.Equal(t, "API_KEY=super-secret-value", extraEnv[0])

	// Secrets list captures expanded values for redaction.
	require.Len(t, secrets, 1)
	assert.Equal(t, "super-secret-value", secrets[0])

	// Config values are not secrets — they appear as KEY=VALUE in args.
	found := false
	for _, arg := range args {
		if arg == "BASE_URL=https://api.example.com" {
			found = true
		}
	}
	assert.True(t, found, "config should appear as KEY=VALUE in args")
}

func TestBuildProviderArgs_KeyRemapping(t *testing.T) {
	// Credential key name differs from the host env var name.
	t.Setenv("HOST_VAR_NAME", "the-secret")

	credentials := map[string]string{
		"PROVIDER_KEY": "${HOST_VAR_NAME}",
	}

	args, extraEnv, _ := buildProviderArgs("p", "custom", credentials, nil, false)

	// Bare key uses the credential key name, not the host var name.
	for _, arg := range args {
		assert.NotContains(t, arg, "the-secret")
	}

	// The child env maps the credential key to the expanded value.
	require.Len(t, extraEnv, 1)
	assert.Equal(t, "PROVIDER_KEY=the-secret", extraEnv[0])
}

func TestBuildProviderArgs_EmptyCredential(t *testing.T) {
	t.Setenv("EMPTY_VAR", "")

	credentials := map[string]string{
		"KEY": "${EMPTY_VAR}",
	}

	args, extraEnv, secrets := buildProviderArgs("p", "custom", credentials, nil, false)

	// Empty values use inline KEY= form, not bare-key + env.
	assert.Empty(t, extraEnv)
	assert.Contains(t, args, "KEY=")

	// Empty string is not added to secrets (nothing to redact).
	assert.Empty(t, secrets)
}

func TestCollectLogs_OpenshellNotInPath(t *testing.T) {
	t.Setenv("PATH", "")

	_, err := CollectLogs("nonexistent-sandbox", "sandbox")
	assert.Error(t, err)
}

func TestCollectLogs_InvalidSource(t *testing.T) {
	// When openshell is not in PATH, any source should fail.
	t.Setenv("PATH", "")

	_, err := CollectLogs("test-sandbox", "invalid-source")
	assert.Error(t, err)
}

func TestEnableProvidersV2_OpenshellNotInPath(t *testing.T) {
	t.Setenv("PATH", "")
	err := EnableProvidersV2()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "providers_v2")
}

func TestEnableProvidersV2(t *testing.T) {
	tests := []struct {
		name    string
		script  string
		wantErr bool
	}{
		{
			name:   "success",
			script: "#!/bin/sh\nexit 0\n",
		},
		{
			name: "unknown setting key",
			// Captured from a real OpenShell CLI after providers_v2_enabled
			// was removed. Box-drawing and the × marker must not prevent a match.
			script: "#!/bin/sh\n" +
				"echo \"Error:   \u00d7 unknown setting key 'providers_v2_enabled'. Allowed keys:\" >&2\n" +
				"echo \"  \u2502 ocsf_json_enabled, ocsf_schema_version, agent_policy_proposals_enabled,\" >&2\n" +
				"echo \"  \u2502 proposal_approval_mode\" >&2\n" +
				"exit 1\n",
		},
		{
			name: "unknown setting key mixed case",
			script: "#!/bin/sh\n" +
				"echo \"Unknown Setting Key 'PROVIDERS_V2_ENABLED'\" >&2\n" +
				"exit 1\n",
		},
		{
			name: "permission denied",
			script: "#!/bin/sh\n" +
				"echo \"permission denied\" >&2\n" +
				"exit 1\n",
			wantErr: true,
		},
		{
			name: "unknown other key",
			script: "#!/bin/sh\n" +
				"echo \"unknown setting key 'ocsf_json_enabled'\" >&2\n" +
				"exit 1\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(tt.script), 0o755))
			t.Setenv("PATH", dir)

			err := EnableProvidersV2()
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "providers_v2")
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestIsUnknownProvidersV2Setting(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name: "real openshell unknown-key error",
			output: "Error:   \u00d7 unknown setting key 'providers_v2_enabled'. Allowed keys:\n" +
				"  \u2502 ocsf_json_enabled, ocsf_schema_version",
			want: true,
		},
		{
			name:   "mixed case",
			output: "Unknown Setting Key 'PROVIDERS_V2_ENABLED'",
			want:   true,
		},
		{
			name:   "empty",
			output: "",
		},
		{
			name:   "unknown key without providers_v2_enabled",
			output: "unknown setting key 'ocsf_json_enabled'",
		},
		{
			name:   "key name without unknown-setting phrasing",
			output: "failed to set providers_v2_enabled: permission denied",
		},
		{
			name:   "unrelated error",
			output: "permission denied",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isUnknownProvidersV2Setting(tt.output))
		})
	}
}

func TestExec_OpenshellNotInPath(t *testing.T) {
	t.Setenv("PATH", "")

	_, _, _, err := Exec("test-sandbox", "echo hello", 10*time.Second)
	assert.Error(t, err)
}

func TestExecContext_CancelledContext(t *testing.T) {
	t.Setenv("PATH", "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, _, err := ExecContext(ctx, "test-sandbox", "echo hello", 10*time.Second)
	assert.Error(t, err)
}

func TestExecStreamReader_OpenshellNotInPath(t *testing.T) {
	t.Setenv("PATH", "")

	_, _, _, err := ExecStreamReader(context.Background(), "test-sandbox", "echo hello", 10*time.Second, os.Stderr)
	assert.Error(t, err)
}

func TestOsRootContainment(t *testing.T) {
	dir := t.TempDir()

	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	defer root.Close()

	f, err := root.Create("safe.txt")
	require.NoError(t, err)
	f.Close()

	_, err = root.Create("../../../etc/passwd")
	assert.Error(t, err)

	_, err = root.Create("../../home/runner/.bashrc")
	assert.Error(t, err)

	_, err = root.Create("subdir/../../etc/shadow")
	assert.Error(t, err)
}

func TestSanitizeDownload_RemovesAbsoluteSymlinks(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "real.txt"), []byte("ok"), 0o644))
	require.NoError(t, os.Symlink("/nonexistent/target", filepath.Join(dir, "danger")))

	err := sanitizeDownload(dir)
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(dir, "real.txt"))
	assert.NoError(t, err)

	_, err = os.Lstat(filepath.Join(dir, "danger"))
	assert.True(t, os.IsNotExist(err), "absolute symlink should have been removed")
}

func TestSanitizeDownload_KeepsRelativeSymlinksInsideRepo(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "target.txt"), []byte("ok"), 0o644))
	// Relative symlink: sub/link -> ../target.txt (stays inside dir)
	require.NoError(t, os.Symlink("../target.txt", filepath.Join(dir, "sub", "link")))

	err := sanitizeDownload(dir)
	require.NoError(t, err)

	_, err = os.Lstat(filepath.Join(dir, "sub", "link"))
	assert.NoError(t, err, "relative in-repo symlink should be preserved")
}

func TestSanitizeDownload_RemovesSymlinkChainEscape(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "real"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	// dirlink -> ../real: relative, inside repo — sanitizeDownload keeps it.
	require.NoError(t, os.Symlink("../real", filepath.Join(dir, "sub", "dirlink")))
	// escape -> "sub/dirlink/../../etc/passwd":
	//   filepath.Clean sees: dir/sub/dirlink/../../etc/passwd → dir/etc/passwd (inside, passes textual check)
	//   EvalSymlinks follows: sub/dirlink → dir/real → ../../etc/passwd → outside dir (escapes)
	require.NoError(t, os.Symlink("sub/dirlink/../../etc/passwd", filepath.Join(dir, "escape")))

	err := sanitizeDownload(dir)
	require.NoError(t, err)

	_, err = os.Lstat(filepath.Join(dir, "sub", "dirlink"))
	assert.NoError(t, err, "in-repo dirlink should be preserved")

	_, err = os.Lstat(filepath.Join(dir, "escape"))
	assert.True(t, os.IsNotExist(err), "symlink-chain escape should be removed")
}

func TestSanitizeDownload_RemovesRelativeSymlinksEscapingRepo(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	// Relative symlink that traverses above dir root.
	require.NoError(t, os.Symlink("../../etc/passwd", filepath.Join(dir, "sub", "escape")))

	err := sanitizeDownload(dir)
	require.NoError(t, err)

	_, err = os.Lstat(filepath.Join(dir, "sub", "escape"))
	assert.True(t, os.IsNotExist(err), "escaping relative symlink should have been removed")
}

func TestSanitizeDownload_RemovesDirSymlinkIndirection(t *testing.T) {
	repo := t.TempDir()

	// Place a secret file outside the repo root.
	secret := filepath.Join(filepath.Dir(repo), "secret")
	require.NoError(t, os.WriteFile(secret, []byte("leaked"), 0o644))
	t.Cleanup(func() { os.Remove(secret) })

	// d/x is a directory symlink to "." — relative, inside repo, so kept.
	// e targets "d/x/../../secret" which textually cleans to repo/secret (inside),
	// but on the filesystem d/x resolves to d/, so ../../secret escapes.
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "d"), 0o755))
	require.NoError(t, os.Symlink(".", filepath.Join(repo, "d", "x")))
	require.NoError(t, os.Symlink("d/x/../../secret", filepath.Join(repo, "e")))

	require.NoError(t, sanitizeDownload(repo))

	_, err := os.Lstat(filepath.Join(repo, "e"))
	assert.True(t, os.IsNotExist(err), "dir-symlink indirection escape should have been removed")
}

func TestSanitizeDownload_RemovesGitHooks(t *testing.T) {
	dir := t.TempDir()

	// Create .git/hooks/ with a script.
	hooksDir := filepath.Join(dir, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte("#!/bin/sh\nmalicious"), 0o755))

	// Create a safe file under .git/.
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("[core]"), 0o644))

	err := sanitizeDownload(dir)
	require.NoError(t, err)

	// .git/hooks/ should be removed entirely.
	_, err = os.Stat(hooksDir)
	assert.True(t, os.IsNotExist(err), ".git/hooks/ should have been removed")

	// .git/config should survive.
	_, err = os.Stat(filepath.Join(dir, ".git", "config"))
	assert.NoError(t, err)
}

func TestSanitizeDownload_NestedSymlinks(t *testing.T) {
	dir := t.TempDir()

	// Create nested structure with symlinks at various depths.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a", "b", "real.txt"), []byte("ok"), 0o644))
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(dir, "a", "b", "link")))
	require.NoError(t, os.Symlink("/etc/shadow", filepath.Join(dir, "a", "top-link")))

	err := sanitizeDownload(dir)
	require.NoError(t, err)

	// Real file survives.
	_, err = os.Stat(filepath.Join(dir, "a", "b", "real.txt"))
	assert.NoError(t, err)

	// Both symlinks removed.
	_, err = os.Lstat(filepath.Join(dir, "a", "b", "link"))
	assert.True(t, os.IsNotExist(err))
	_, err = os.Lstat(filepath.Join(dir, "a", "top-link"))
	assert.True(t, os.IsNotExist(err))
}

func TestSanitizeDownload_RemovesSubmoduleGitHooks(t *testing.T) {
	dir := t.TempDir()

	// Create submodule .git/hooks/ with a script.
	subHooks := filepath.Join(dir, "vendor", "dep", ".git", "hooks")
	require.NoError(t, os.MkdirAll(subHooks, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(subHooks, "post-checkout"), []byte("#!/bin/sh\nmalicious"), 0o755))

	// Create a safe file in the submodule .git/.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "vendor", "dep", ".git", "config"), []byte("[core]"), 0o644))

	err := sanitizeDownload(dir)
	require.NoError(t, err)

	// Submodule .git/hooks/ should be removed.
	_, err = os.Stat(subHooks)
	assert.True(t, os.IsNotExist(err), "submodule .git/hooks/ should have been removed")

	// Submodule .git/config should survive.
	_, err = os.Stat(filepath.Join(dir, "vendor", "dep", ".git", "config"))
	assert.NoError(t, err)
}

func TestSanitizeDownload_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	err := sanitizeDownload(dir)
	assert.NoError(t, err)
}

const (
	testOID    = "0123456789abcdef0123456789abcdef01234567"
	testOID256 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// TestSanitizeDownload_RemovesAncestryMetadata removes replacement refs,
// grafts, and commit-graph caches while keeping the rest of .git intact.
func TestSanitizeDownload_RemovesAncestryMetadata(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".git/HEAD", "ref: refs/heads/main\n")
	writeFile(t, root, ".git/config", "[core]\n")
	writeFile(t, root, ".git/index", "index bytes\n")
	writeFile(t, root, ".git/objects/ab/object", "object\n")
	writeFile(t, root, ".git/objects/pack/pack-abc.idx", "pack\n")
	writeFile(t, root, ".git/refs/heads/main", testOID+"\n")
	writeFile(t, root, ".git/refs/remotes/origin/main", testOID+"\n")
	writeFile(t, root, ".git/refs/tags/v1", testOID+"\n")
	writeFile(t, root, ".git/refs/stash", testOID+"\n")
	writeFile(t, root, ".git/refs/notes/commits", testOID+"\n")
	writeFile(t, root, ".git/refs/bisect/good", testOID+"\n")
	writeFile(t, root, ".git/refs/custom/x", testOID+"\n")
	writeFile(t, root, ".git/refs/replace/"+testOID, testOID+"\n")
	writeFile(t, root, ".git/info/exclude", "# exclude\n")
	writeFile(t, root, ".git/info/refs", "cache\n")
	writeFile(t, root, ".git/info/grafts", testOID+" "+testOID+"\n")
	writeFile(t, root, ".git/objects/info/packs", "cache\n")
	writeFile(t, root, ".git/objects/info/commit-graph", "graph\n")
	writeFile(t, root, ".git/objects/info/commit-graphs/chain", "graph chain\n")
	writeFile(t, root, ".git/modules/dep/config", "[core]\n")
	writeFile(t, root, ".git/worktrees/wt/HEAD", "ref: refs/heads/wt\n")

	require.NoError(t, sanitizeDownload(root))

	for _, rel := range []string{
		".git/HEAD", ".git/config", ".git/index",
		".git/objects/ab/object", ".git/objects/pack/pack-abc.idx",
		".git/refs/heads/main", ".git/refs/remotes/origin/main",
		".git/refs/tags/v1", ".git/refs/stash", ".git/refs/notes/commits",
		".git/refs/bisect/good", ".git/refs/custom/x",
		".git/info/exclude", ".git/info/refs", ".git/objects/info/packs",
		".git/modules/dep/config", ".git/worktrees/wt/HEAD",
	} {
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		assert.NoError(t, err, "%s must be retained", rel)
	}
	for _, rel := range []string{
		".git/refs/replace", ".git/info/grafts",
		".git/objects/info/commit-graph", ".git/objects/info/commit-graphs",
	} {
		_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
		assert.True(t, os.IsNotExist(err), "%s must be removed", rel)
	}
}

// TestSanitizeDownload_PreservesLiteralBackslashNames checks that a Unix file
// whose name contains backslashes is not treated as a slash-separated metadata
// path.
func TestSanitizeDownload_PreservesLiteralBackslashNames(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("backslash is a path separator on Windows")
	}
	root := t.TempDir()
	name := `.git/objects\info\commit-graph`
	writeFile(t, root, name, "graph\n")

	require.NoError(t, sanitizeDownload(root))

	_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(name)))
	require.NoError(t, err, "literal backslash name must survive sanitization")
}

// TestSanitizeDownload_RejectsSymlinkedGitMetadata fails closed when the root
// .git or a container holding filtered metadata is a symlink.
func TestSanitizeDownload_RejectsSymlinkedGitMetadata(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, root string)
	}{
		{
			name: "root git",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, "real-git"), 0o755))
				require.NoError(t, os.Symlink("real-git", filepath.Join(root, ".git")))
			},
		},
		{
			name: "refs",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
				require.NoError(t, os.MkdirAll(filepath.Join(root, "real-refs"), 0o755))
				require.NoError(t, os.Symlink("../real-refs", filepath.Join(root, ".git", "refs")))
			},
		},
		{
			name: "info",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
				require.NoError(t, os.MkdirAll(filepath.Join(root, "real-info"), 0o755))
				require.NoError(t, os.Symlink("../real-info", filepath.Join(root, ".git", "info")))
			},
		},
		{
			name: "objects",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
				require.NoError(t, os.MkdirAll(filepath.Join(root, "real-objects"), 0o755))
				require.NoError(t, os.Symlink("../real-objects", filepath.Join(root, ".git", "objects")))
			},
		},
		{
			name: "objects info",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, ".git", "objects"), 0o755))
				require.NoError(t, os.MkdirAll(filepath.Join(root, "real-oinfo"), 0o755))
				require.NoError(t, os.Symlink("../../real-oinfo", filepath.Join(root, ".git", "objects", "info")))
			},
		},
		{
			name: "nested git",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, "vendor", "dep", "real-git"), 0o755))
				require.NoError(t, os.Symlink("real-git", filepath.Join(root, "vendor", "dep", ".git")))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.setup(t, root)

			err := sanitizeDownload(root)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "git metadata")
		})
	}
}

func TestSanitizeDownload_RejectsRedirectedRootGitMetadata(t *testing.T) {
	requireGit(t)
	for _, layout := range []string{"gitfile", "commondir"} {
		t.Run(layout, func(t *testing.T) {
			r := newGitRepo(t)
			head := r.commitFile("file.txt", "original")
			gitDir := filepath.Join(r.dir, ".git")
			headFile, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
			require.NoError(t, err)
			require.NoError(t, os.Rename(gitDir, filepath.Join(r.dir, "git-metadata")))
			if layout == "gitfile" {
				writeFile(t, r.dir, ".git", "gitdir: git-metadata\n")
			} else {
				writeFile(t, r.dir, ".git/HEAD", string(headFile))
				writeFile(t, r.dir, ".git/commondir", "../git-metadata\n")
			}

			assert.Equal(t, head, r.git("rev-parse", "HEAD"), "Git must recognize the redirected layout")
			require.Error(t, sanitizeDownload(r.dir), "redirected Git metadata must be rejected")
		})
	}
}

// TestSanitizeDownload_AcceptsReftableGitStorage accepts a real reftable
// repository and preserves its refs, HEAD, and config bytes.
func TestSanitizeDownload_AcceptsReftableGitStorage(t *testing.T) {
	requireReftableSanitization(t)
	r := newReftableRepo(t)
	head := r.commitFile("file.txt", "original")
	configPath := filepath.Join(r.dir, ".git", "config")
	configBefore, err := os.ReadFile(configPath)
	require.NoError(t, err)

	require.NoError(t, sanitizeDownload(r.dir))

	assert.Equal(t, head, r.git("rev-parse", "HEAD"))
	assert.Equal(t, "main", r.git("symbolic-ref", "--short", "HEAD"))
	assert.Equal(t, head, r.git("rev-parse", "main"))
	_, err = os.Stat(filepath.Join(r.dir, ".git", "reftable", "tables.list"))
	require.NoError(t, err, "reftable tables must survive sanitization")
	configAfter, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Equal(t, configBefore, configAfter, "repository config bytes must be preserved")
}

// TestSanitizeDownload_RemovesReftableReplacementRefs removes a forged
// ancestry bridge stored as a reftable replacement ref.
func TestSanitizeDownload_RemovesReftableReplacementRefs(t *testing.T) {
	requireReftableSanitization(t)
	r := newReftableRepo(t)
	human := r.commitFile("human.txt", "human work")
	tree := r.git("rev-parse", human+"^{tree}")
	orphan := r.git("commit-tree", tree, "-m", "agent orphan")
	bridge := r.git("commit-tree", tree, "-p", human, "-m", "bridge")
	r.git("replace", orphan, bridge)

	require.True(t, r.isAncestor(human, orphan))
	require.NoError(t, sanitizeDownload(r.dir))

	assert.False(t, r.isAncestor(human, orphan), "forged ancestry must not survive")
	assert.Empty(t, r.git("replace", "--list"))
	assert.Equal(t, human, r.git("rev-parse", "main"))
}

// TestSanitizeDownload_ReftableSymbolicReplacementRemovesLinkOnly deletes a
// symbolic replacement ref without dereferencing its target branch.
func TestSanitizeDownload_ReftableSymbolicReplacementRemovesLinkOnly(t *testing.T) {
	requireReftableSanitization(t)
	r := newReftableRepo(t)
	head := r.commitFile("human.txt", "human work")
	link := "refs/replace/" + head
	r.git("symbolic-ref", link, "refs/heads/main")
	require.Equal(t, "refs/heads/main", r.git("symbolic-ref", link))

	require.NoError(t, sanitizeDownload(r.dir))

	_, err := r.tryGit("show-ref", "--verify", link)
	require.Error(t, err, "symbolic replacement ref must be removed")
	assert.Equal(t, head, r.git("rev-parse", "refs/heads/main"), "target branch must be unchanged")
}

// TestSanitizeDownload_ReftableUnbornChecksOut accepts an unborn reftable
// repository with no refs yet.
func TestSanitizeDownload_ReftableUnbornChecksOut(t *testing.T) {
	requireReftableSanitization(t)
	r := newReftableRepo(t)

	require.NoError(t, sanitizeDownload(r.dir))

	assert.Equal(t, "refs/heads/main", r.git("symbolic-ref", "HEAD"))
	assert.Empty(t, r.git("for-each-ref", "refs/heads/"))
}

// TestSanitizeDownload_ReftableShallowBoundary accepts a shallow reftable
// repository and preserves its shallow boundary.
func TestSanitizeDownload_ReftableShallowBoundary(t *testing.T) {
	requireReftableSanitization(t)
	src := newGitRepo(t)
	src.commitFile("file.txt", "one")
	src.commitFile("file.txt", "two")

	r := newReftableRepo(t)
	r.git("remote", "add", "origin", src.dir)
	r.git("fetch", "--quiet", "--depth=1", "origin", "main")
	r.git("reset", "--quiet", "--hard", "FETCH_HEAD")
	shallowBefore, err := os.ReadFile(filepath.Join(r.dir, ".git", "shallow"))
	require.NoError(t, err, "fixture must be shallow")

	require.NoError(t, sanitizeDownload(r.dir))

	shallowAfter, err := os.ReadFile(filepath.Join(r.dir, ".git", "shallow"))
	require.NoError(t, err)
	assert.Equal(t, shallowBefore, shallowAfter, "shallow boundary must survive sanitization")
	assert.Equal(t, "1", r.git("rev-list", "--count", "HEAD"))
}

// TestSanitizeDownload_ReftableIgnoresEvilConfig proves repository fsck and
// hook settings cannot weaken replacement cleanup or execute code.
func TestSanitizeDownload_ReftableIgnoresEvilConfig(t *testing.T) {
	requireReftableSanitization(t)
	r := newReftableRepo(t)
	human := r.commitFile("human.txt", "human work")
	tree := r.git("rev-parse", human+"^{tree}")
	orphan := r.git("commit-tree", tree, "-m", "agent orphan")
	bridge := r.git("commit-tree", tree, "-p", human, "-m", "bridge")
	r.git("replace", orphan, bridge)

	hookDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hook := filepath.Join(hookDir, "reference-transaction")
	script := "#!/bin/sh\nprintf ran > " + marker + "\n"
	require.NoError(t, os.WriteFile(hook, []byte(script), 0o755))
	skipList := writeFile(t, t.TempDir(), "skiplist", "")

	for key, value := range map[string]string{
		"core.hooksPath":             hookDir,
		"fsck.badReftableTableName":  "ignore",
		"fsck.badRefOid":             "ignore",
		"fsck.badHeadTarget":         "ignore",
		"fsck.badReferentName":       "ignore",
		"fsck.symrefTargetIsNotARef": "ignore",
		"fsck.skipList":              skipList,
	} {
		r.git("config", key, value)
	}
	configPath := filepath.Join(r.dir, ".git", "config")
	configBefore, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.NoFileExists(t, marker, "hook must not run during setup")

	require.NoError(t, sanitizeDownload(r.dir))

	assert.NoFileExists(t, marker, "repository hook must never execute")
	assert.Empty(t, r.git("replace", "--list"), "repository fsck config must not weaken cleanup")
	configAfter, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Equal(t, configBefore, configAfter, "repository config bytes must be preserved")
}

// TestSafeDownload_ReftableIgnoresConfigHooks proves repository config cannot
// define a reference-transaction hook that runs while replacement refs are
// deleted, for both object formats, and that ordinary refs survive.
func TestSafeDownload_ReftableIgnoresConfigHooks(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			requireReftableSanitization(t)
			r := newReftableRepoFormat(t, format)
			human := r.commitFile("human.txt", "human work")
			tree := r.git("rev-parse", human+"^{tree}")
			orphan := r.git("commit-tree", tree, "-m", "agent orphan")
			bridge := r.git("commit-tree", tree, "-p", human, "-m", "bridge")
			r.git("replace", orphan, bridge)
			r.git("symbolic-ref", "refs/heads/alias", "refs/heads/main")
			r.git("tag", "-a", "v1", "-m", "v1")
			head := r.git("rev-parse", "HEAD")
			tag := r.git("rev-parse", "v1")

			marker := filepath.Join(t.TempDir(), "hook-ran")
			r.git("config", "hook.probe.command", "printf ran > '"+marker+"'")
			r.git("config", "hook.probe.event", "reference-transaction")
			r.git("config", "hook.probe.enabled", "true")
			require.NoFileExists(t, marker, "hook must not run while refs are created")

			serveDownloadFrom(t, r.dir)
			localDir := filepath.Join(t.TempDir(), "repo")
			require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir,
				RepositorySnapshot{}))

			assert.NoFileExists(t, marker, "repository config hook must never execute")
			out, err := runGit(t, localDir, r.env, "replace", "--list")
			require.NoError(t, err)
			assert.Empty(t, out, "replacement refs must not be published")
			assert.Equal(t, head, r.gitAt(localDir, "rev-parse", "HEAD"))
			assert.Equal(t, head, r.gitAt(localDir, "rev-parse", "main"))
			assert.Equal(t, "refs/heads/main", r.gitAt(localDir, "symbolic-ref", "refs/heads/alias"))
			assert.Equal(t, tag, r.gitAt(localDir, "rev-parse", "v1"))
		})
	}
}

// TestSanitizeDownload_ReftableCorruptTableFailsClosed rejects a corrupt
// reftable record and never publishes it through SafeDownload.
func TestSanitizeDownload_ReftableCorruptTableFailsClosed(t *testing.T) {
	r := newReftableRepo(t)
	r.commitFile("file.txt", "original")
	tables, err := filepath.Glob(filepath.Join(r.dir, ".git", "reftable", "*.ref"))
	require.NoError(t, err)
	require.NotEmpty(t, tables)
	data, err := os.ReadFile(tables[0])
	require.NoError(t, err)
	data[4] ^= 0xff // corrupt the table format version
	require.NoError(t, os.WriteFile(tables[0], data, 0o644))

	require.Error(t, sanitizeDownload(r.dir), "corrupt reftable bytes must fail closed")

	serveDownloadFrom(t, r.dir)
	localDir := filepath.Join(t.TempDir(), "repo")
	err = SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, RepositorySnapshot{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRepositoryValidation), "want ErrRepositoryValidation, got %v", err)
	assert.NoDirExists(t, localDir, "corrupt reftable storage must not be published")
}

// TestSafeDownload_RemovesReplacementRefsByExactName removes every replacement
// ref by its exact byte suffix, including suffixes that are not object ids or
// contain non-breaking spaces, and leaves HEAD and branches unchanged.
func TestSafeDownload_RemovesReplacementRefsByExactName(t *testing.T) {
	cases := []struct {
		name     string
		suffix   string
		ordinary bool
	}{
		{name: "object id suffix", ordinary: true},
		{name: "non-hex suffix", suffix: "not-an-object-id"},
		{name: "non-breaking space suffix", suffix: "\u00a0"},
		{name: "surrounded non-breaking space suffix", suffix: "\u00a0name\u00a0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireReftableSanitization(t)
			r := newReftableRepo(t)
			human := r.commitFile("file.txt", "original")
			suffix, target := tc.suffix, human
			if tc.ordinary {
				target = r.commitFile("agent.txt", "agent work")
				suffix = human
			}
			ref := "refs/replace/" + suffix
			out, err := runGit(t, r.dir, r.env, "update-ref", "--no-deref", ref, target)
			require.NoError(t, err, out)
			_, err = runGit(t, r.dir, r.env, "show-ref", "--verify", ref)
			require.NoError(t, err, "fixture must contain the replacement ref")
			head := r.git("rev-parse", "HEAD")

			serveDownloadFrom(t, r.dir)
			localDir := filepath.Join(t.TempDir(), "repo")
			require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir,
				RepositorySnapshot{}))

			_, err = runGit(t, localDir, r.env, "show-ref", "--verify", ref)
			require.Error(t, err, "replacement ref %q must be removed", ref)
			out, err = runGit(t, localDir, r.env, "for-each-ref", "refs/replace/")
			require.NoError(t, err)
			assert.Empty(t, out, "no replacement refs may be published")
			assert.Equal(t, head, r.gitAt(localDir, "rev-parse", "HEAD"))
			assert.Equal(t, head, r.gitAt(localDir, "rev-parse", "main"))
		})
	}
}

// TestSanitizeDownload_ReftableVersionAndQueryFailures fails closed when the
// native version is too old, unparseable, the storage or object-format query
// fails, the object format is unsupported, or the trusted store cannot be
// initialized.
func TestSanitizeDownload_ReftableVersionAndQueryFailures(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "git 2.52",
			body: "if [ \"$1\" = \"--version\" ]; then\n  echo 'git version 2.52.0'\n  exit 0\nfi",
		},
		{
			name: "unparseable version",
			body: "if [ \"$1\" = \"--version\" ]; then\n  echo 'git version bogus'\n  exit 0\nfi",
		},
		{
			name: "version query failure",
			body: "if [ \"$1\" = \"--version\" ]; then exit 3; fi",
		},
		{
			name: "storage query failure",
			body: "for a in \"$@\"; do\n  if [ \"$a\" = config ]; then exit 2; fi\ndone",
		},
		{
			name: "object format query failure",
			body: "for a in \"$@\"; do\n  if [ \"$a\" = extensions.objectFormat ]; then exit 2; fi\ndone",
		},
		{
			name: "unsupported object format",
			body: "for a in \"$@\"; do\n  if [ \"$a\" = extensions.objectFormat ]; then echo sha3; exit 0; fi\ndone",
		},
		{
			name: "trusted store init failure",
			body: "for a in \"$@\"; do\n  if [ \"$a\" = init ]; then exit 2; fi\ndone",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newReftableRepo(t)
			r.commitFile("file.txt", "original")
			installGitShim(t, tc.body)

			require.Error(t, sanitizeDownload(r.dir))
		})
	}
}

// TestSanitizeDownload_ReftableListingFailureFailsClosed rejects a repository
// whose replacement refs cannot be listed, before any ref is modified.
func TestSanitizeDownload_ReftableListingFailureFailsClosed(t *testing.T) {
	r := newReftableRepo(t)
	r.commitFile("file.txt", "original")
	installGitShim(t, "for a in \"$@\"; do\n  if [ \"$a\" = replace ]; then exit 2; fi\ndone")

	require.Error(t, sanitizeDownload(r.dir))
}

// TestSanitizeDownload_ReftableDeleteFailureFailsClosed rejects the download
// when replacement refs cannot be deleted.
func TestSanitizeDownload_ReftableDeleteFailureFailsClosed(t *testing.T) {
	r := newReftableRepo(t)
	human := r.commitFile("file.txt", "original")
	tree := r.git("rev-parse", human+"^{tree}")
	orphan := r.git("commit-tree", tree, "-m", "agent orphan")
	bridge := r.git("commit-tree", tree, "-p", human, "-m", "bridge")
	r.git("replace", orphan, bridge)
	installGitShim(t, "for a in \"$@\"; do\n  if [ \"$a\" = update-ref ]; then exit 2; fi\ndone")

	require.Error(t, sanitizeDownload(r.dir))
	assert.NotEmpty(t, r.git("replace", "--list"), "failed deletion must not drop the replacement refs")
}

// TestSanitizeDownload_ReftableLayoutMatrix rejects malformed reftable
// layouts before native Git runs.
func TestSanitizeDownload_ReftableLayoutMatrix(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, root string)
	}{
		{
			name: "reftable symlink",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
				require.NoError(t, os.MkdirAll(filepath.Join(root, "outside"), 0o755))
				require.NoError(t, os.Symlink("../outside", filepath.Join(root, ".git", "reftable")))
			},
		},
		{
			name: "reftable file",
			setup: func(t *testing.T, root string) {
				writeFile(t, root, ".git/reftable", "not a directory\n")
			},
		},
		{
			name: "tables list symlink",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, ".git", "reftable"), 0o755))
				writeFile(t, root, "outside", "x\n")
				require.NoError(t, os.Symlink("../../outside", filepath.Join(root, ".git", "reftable", "tables.list")))
			},
		},
		{
			name: "tables list fifo",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, ".git", "reftable"), 0o755))
				requireFIFO(t, filepath.Join(root, ".git", "reftable"), "tables.list")
			},
		},
		{
			name: "table symlink",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, ".git", "reftable"), 0o755))
				writeFile(t, root, "outside", "x\n")
				require.NoError(t, os.Symlink("../../outside", filepath.Join(root, ".git", "reftable", "table.ref")))
			},
		},
		{
			name: "nested directory",
			setup: func(t *testing.T, root string) {
				writeFile(t, root, ".git/reftable/sub/table.ref", "x\n")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.setup(t, root)

			err := sanitizeDownload(root)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "reftable")
		})
	}
}

// TestSanitizeDownload_ReftableRejectsNonRegularHEAD fails closed when the
// reftable checkout's HEAD is missing or not a regular file.
func TestSanitizeDownload_ReftableRejectsNonRegularHEAD(t *testing.T) {
	requireReftableSanitization(t)
	cases := []struct {
		name         string
		setup        func(t *testing.T, path string)
		wantRejected bool
		wantNotExist bool
	}{
		{
			name:         "missing",
			setup:        func(t *testing.T, path string) { require.NoError(t, os.Remove(path)) },
			wantRejected: true,
			wantNotExist: true,
		},
		{
			name: "directory",
			setup: func(t *testing.T, path string) {
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Mkdir(path, 0o755))
			},
			wantRejected: true,
		},
		{
			name: "symlink",
			setup: func(t *testing.T, path string) {
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Symlink("config", path))
			},
			wantRejected: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newReftableRepo(t)
			r.commitFile("file.txt", "original")
			tc.setup(t, filepath.Join(r.dir, ".git", "HEAD"))

			err := sanitizeDownload(r.dir)
			require.Error(t, err)
			assert.Equal(t, tc.wantRejected, errors.Is(err, ErrRepositoryRejected), "got %v", err)
			if tc.wantNotExist {
				assert.True(t, errors.Is(err, os.ErrNotExist), "a missing HEAD keeps its cause: %v", err)
			}
		})
	}
}

// TestRemoveReplacementRefs_MissingStoreFailsClosed surfaces native verification
// failures instead of deleting anything.
func TestRemoveReplacementRefs_MissingStoreFailsClosed(t *testing.T) {
	requireGit(t)
	require.Error(t, removeReplacementRefs(t.TempDir(), filepath.Join(t.TempDir(), "missing")))
}

// TestSanitizeDownload_RejectsReftableWorktreeMetadata rejects registered
// worktree metadata for reftable, where extra ref stores could live.
func TestSanitizeDownload_RejectsReftableWorktreeMetadata(t *testing.T) {
	r := newReftableRepo(t)
	r.commitFile("file.txt", "original")
	writeFile(t, r.dir, ".git/worktrees/wt/gitdir", "../outside\n")
	sentinel := filepath.Join(t.TempDir(), "outside")
	require.NoFileExists(t, sentinel)

	err := sanitizeDownload(r.dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "worktree")
	assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
	assert.NoFileExists(t, sentinel, "worktree manifest must not be followed")
}

// TestSanitizeDownload_ReftableNestedStoresCleaned cleans replacement refs in
// both the root and an ordinary nested checkout's own reftable store.
func TestSanitizeDownload_ReftableNestedStoresCleaned(t *testing.T) {
	requireReftableSanitization(t)
	root := newReftableRepo(t)
	rootHuman := root.commitFile("root.txt", "root work")
	rootTree := root.git("rev-parse", rootHuman+"^{tree}")
	rootOrphan := root.git("commit-tree", rootTree, "-m", "root orphan")
	rootBridge := root.git("commit-tree", rootTree, "-p", rootHuman, "-m", "root bridge")
	root.git("replace", rootOrphan, rootBridge)

	nested := initReftableRepo(t, filepath.Join(root.dir, "vendor", "dep"))
	nestedHuman := nested.commitFile("dep.txt", "dep work")
	nestedTree := nested.git("rev-parse", nestedHuman+"^{tree}")
	nestedOrphan := nested.git("commit-tree", nestedTree, "-m", "dep orphan")
	nestedBridge := nested.git("commit-tree", nestedTree, "-p", nestedHuman, "-m", "dep bridge")
	nested.git("replace", nestedOrphan, nestedBridge)

	require.NoError(t, sanitizeDownload(root.dir))

	assert.Empty(t, root.git("replace", "--list"), "root replacement refs must be removed")
	assert.Empty(t, nested.git("replace", "--list"), "nested replacement refs must be removed")
	assert.Equal(t, rootHuman, root.git("rev-parse", "main"))
	assert.Equal(t, nestedHuman, nested.git("rev-parse", "main"))
}

// TestSanitizeDownload_PackedRefsDropsReplacements drops refs/replace records
// and their peeled lines while preserving every other record byte-for-byte.
func TestSanitizeDownload_PackedRefsDropsReplacements(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "drops replacement record and peeled line",
			in:   testOID + " refs/replace/" + testOID + "\n^" + testOID + "\n" + testOID + " refs/heads/main\n^" + testOID256 + "\n",
			want: testOID + " refs/heads/main\n^" + testOID256 + "\n",
		},
		{
			name: "keeps other namespaces with header, order, and peeled lines",
			in:   "# pack-refs with: peeled fully-peeled sorted \n" + testOID + " refs/heads/main\n" + testOID + " refs/tags/v1\n^" + testOID256 + "\n" + testOID + " refs/remotes/origin/main\n" + testOID + " refs/notes/x\n" + testOID + " refs/stash\n" + testOID + " refs/bisect/good\n" + testOID + " refs/custom/x\n",
			want: "# pack-refs with: peeled fully-peeled sorted \n" + testOID + " refs/heads/main\n" + testOID + " refs/tags/v1\n^" + testOID256 + "\n" + testOID + " refs/remotes/origin/main\n" + testOID + " refs/notes/x\n" + testOID + " refs/stash\n" + testOID + " refs/bisect/good\n" + testOID + " refs/custom/x\n",
		},
		{
			name: "namespace match is case-sensitive and exact",
			in:   testOID + " REFS/replace/x\n" + testOID + " refs/Replace/x\n" + testOID + " refs/replaces/x\n" + testOID + " refs/replace\n" + testOID + " refs/heads/replace\n",
			want: testOID + " REFS/replace/x\n" + testOID + " refs/Replace/x\n" + testOID + " refs/replaces/x\n" + testOID + " refs/replace\n" + testOID + " refs/heads/replace\n",
		},
		{
			name: "preserves unicode ref names and tab separators",
			in:   testOID + " refs/heads/feat\u00a0ure\n" + testOID + "\trefs/tags/v\u20031\n",
			want: testOID + " refs/heads/feat\u00a0ure\n" + testOID + "\trefs/tags/v\u20031\n",
		},
		{
			name: "sha256 record",
			in:   testOID256 + " refs/heads/main\n",
			want: testOID256 + " refs/heads/main\n",
		},
		{
			name: "keeps malformed, unicode, near-miss, and interrupted peeled lines",
			in: "# pack-refs with: peeled fully-peeled sorted \n" +
				"scratch\n" +
				testOID + " refs/replaces/x\r\n" +
				testOID + " refs/heads/feat\u00a0ure\n" +
				testOID + " refs/replace/" + testOID + "\n" +
				"# refs/replace/comment between record and peeled line\n" +
				"^" + testOID + "\n" +
				testOID + " refs/replace/x\n" +
				"^" + testOID + "\n" +
				"^" + testOID256 + "\n",
			want: "# pack-refs with: peeled fully-peeled sorted \n" +
				"scratch\n" +
				testOID + " refs/replaces/x\r\n" +
				testOID + " refs/heads/feat\u00a0ure\n" +
				"# refs/replace/comment between record and peeled line\n" +
				"^" + testOID + "\n" +
				"^" + testOID256 + "\n",
		},
		{
			name: "blank line interrupts peeled association",
			in:   testOID + " refs/replace/x\n\n^" + testOID + "\n",
			want: "\n^" + testOID + "\n",
		},
		{
			name: "final line without newline is preserved",
			in:   testOID + " refs/heads/main\n# tail",
			want: testOID + " refs/heads/main\n# tail",
		},
		{
			name: "final replacement line without newline is dropped",
			in:   testOID + " refs/heads/main\n" + testOID + " refs/replace/x",
			want: testOID + " refs/heads/main\n",
		},
		{
			name: "crlf replacement record and peeled line are dropped",
			in:   testOID + " refs/replace/x\r\n^" + testOID + "\r\n" + testOID + " refs/heads/main\r\n",
			want: testOID + " refs/heads/main\r\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, ".git/packed-refs", tc.in)
			require.NoError(t, sanitizeDownload(root))
			got, err := os.ReadFile(filepath.Join(root, ".git", "packed-refs"))
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(got))
		})
	}

	t.Run("orphan peeled line is preserved", func(t *testing.T) {
		root := t.TempDir()
		content := "^" + testOID + "\n"
		writeFile(t, root, ".git/packed-refs", content)
		require.NoError(t, sanitizeDownload(root))
		got, err := os.ReadFile(filepath.Join(root, ".git", "packed-refs"))
		require.NoError(t, err)
		assert.Equal(t, content, string(got))
	})

	t.Run("non-regular packed refs is dropped", func(t *testing.T) {
		root := t.TempDir()
		external := filepath.Join(t.TempDir(), "packed-refs")
		require.NoError(t, os.WriteFile(external, []byte(testOID+" refs/heads/main\n"), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
		require.NoError(t, os.Symlink(external, filepath.Join(root, ".git", "packed-refs")))

		require.NoError(t, sanitizeDownload(root))
		_, err := os.Lstat(filepath.Join(root, ".git", "packed-refs"))
		assert.True(t, os.IsNotExist(err), "symlinked packed-refs must be dropped")
		_, err = os.Stat(external)
		assert.NoError(t, err, "symlink target must not be touched")
	})

	t.Run("hardlink target is not truncated", func(t *testing.T) {
		root := t.TempDir()
		external := filepath.Join(t.TempDir(), "shared-packed-refs")
		content := testOID + " refs/replace/x\n" + testOID + " refs/heads/main\n"
		require.NoError(t, os.WriteFile(external, []byte(content), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
		packed := filepath.Join(root, ".git", "packed-refs")
		if err := os.Link(external, packed); err != nil {
			t.Skipf("hardlinks unavailable: %v", err)
		}

		require.NoError(t, sanitizeDownload(root))
		shared, err := os.ReadFile(external)
		require.NoError(t, err)
		assert.Equal(t, content, string(shared), "hardlink target must not be truncated")
		got, err := os.ReadFile(packed)
		require.NoError(t, err)
		assert.Equal(t, testOID+" refs/heads/main\n", string(got))
	})

	t.Run("read error propagates", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root bypasses permission checks")
		}
		root := t.TempDir()
		packed := writeFile(t, root, ".git/packed-refs", testOID+" refs/heads/main\n")
		require.NoError(t, os.Chmod(packed, 0o000))
		t.Cleanup(func() { _ = os.Chmod(packed, 0o644) })

		err := sanitizeDownload(root)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "packed-refs")
	})

	t.Run("directory is rejected", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".git", "packed-refs"), 0o755))
		err := sanitizeDownload(root)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "packed-refs")
	})
}

// TestReadBoundedFile checks the exact bound: limit bytes are accepted and one
// more is reported as oversized.
func TestReadBoundedFile(t *testing.T) {
	cases := []struct {
		name      string
		content   string
		limit     int64
		oversized bool
	}{
		{name: "empty file", limit: 8},
		{name: "exactly the limit", content: "12345678", limit: 8},
		{name: "one over the limit", content: "123456789", limit: 8, oversized: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "file")
			require.NoError(t, os.WriteFile(path, []byte(tc.content), 0o644))

			data, oversized, err := readBoundedFile(path, tc.limit)
			require.NoError(t, err)
			assert.Equal(t, tc.oversized, oversized)
			assert.Equal(t, tc.content, string(data))
		})
	}
}

// TestFilterPackedRefs_Limits rejects an oversized packed-refs file without
// touching it and skips the rewrite when nothing is removed.
func TestFilterPackedRefs_Limits(t *testing.T) {
	t.Run("oversized file fails closed", func(t *testing.T) {
		root := t.TempDir()
		packed := writeFile(t, root, "packed-refs", "")
		require.NoError(t, os.Truncate(packed, maxPackedRefsBytes+1))

		err := filterPackedRefs(packed)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
		assert.Contains(t, err.Error(), fmt.Sprintf("larger than %d bytes", maxPackedRefsBytes))
		info, statErr := os.Stat(packed)
		require.NoError(t, statErr)
		assert.Equal(t, int64(maxPackedRefsBytes+1), info.Size(), "a rejected file must be left untouched")
		entries, readErr := os.ReadDir(root)
		require.NoError(t, readErr)
		require.Len(t, entries, 1, "no temporary file is created")
	})

	t.Run("unchanged file is not replaced", func(t *testing.T) {
		root := t.TempDir()
		packed := writeFile(t, root, "packed-refs", testOID+" refs/heads/main\n")
		alias := filepath.Join(t.TempDir(), "alias")
		if err := os.Link(packed, alias); err != nil {
			t.Skipf("hardlinks unavailable: %v", err)
		}

		require.NoError(t, filterPackedRefs(packed))
		packedInfo, err := os.Stat(packed)
		require.NoError(t, err)
		aliasInfo, err := os.Stat(alias)
		require.NoError(t, err)
		assert.True(t, os.SameFile(packedInfo, aliasInfo), "an unchanged file must keep its inode")
	})
}

// TestRestoreTrustedShallow_RejectsOversizedMarker rejects a marker above the
// whole-file limit and leaves it untouched.
func TestRestoreTrustedShallow_RejectsOversizedMarker(t *testing.T) {
	localDir := t.TempDir()
	gitDir := filepath.Join(localDir, ".git")
	require.NoError(t, os.Mkdir(gitDir, 0o755))
	shallowPath := writeFile(t, gitDir, "shallow", "")
	require.NoError(t, os.Truncate(shallowPath, maxShallowBytes+1))

	err := restoreTrustedShallow(localDir, ShallowState{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
	assert.Contains(t, err.Error(), fmt.Sprintf("larger than %d bytes", maxShallowBytes))
	info, statErr := os.Stat(shallowPath)
	require.NoError(t, statErr)
	assert.Equal(t, int64(maxShallowBytes+1), info.Size(), "a rejected marker must be left untouched")
}

// TestSanitizeDownload_RejectsAlternateObjectStores rejects repositories that
// delegate object lookup to an alternate store, including a real shared clone.
func TestSanitizeDownload_RejectsAlternateObjectStores(t *testing.T) {
	for _, name := range []string{"alternates", "http-alternates"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, ".git/objects/info/"+name, "/elsewhere/objects\n")
			err := sanitizeDownload(root)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "git metadata")
		})
	}

	t.Run("shared clone", func(t *testing.T) {
		requireGit(t)
		r := newGitRepo(t)
		head := r.commitFile("file.txt", "original")
		parent := t.TempDir()
		cloneDir := filepath.Join(parent, "clone")
		r.gitAt(parent, "clone", "--quiet", "--shared", r.dir, "clone")
		_, err := runGit(t, cloneDir, r.env, "cat-file", "-e", head)
		require.NoError(t, err, "Git must read objects through the alternate store")
		require.Error(t, sanitizeDownload(cloneDir), "alternate object stores must be rejected")
	})
}

func TestSanitizeDownload_NestedGitCompatibility(t *testing.T) {
	root := t.TempDir()

	// Ordinary directory-form nested repo is filtered.
	writeFile(t, root, "vendor/dep/.git/refs/replace/"+testOID, testOID+"\n")
	writeFile(t, root, "vendor/dep/.git/config", "[core]\n")
	writeFile(t, root, "vendor/dep/.git/refs/heads/main", testOID+"\n")

	// Unlike a root gitfile, a nested gitfile does not redirect Git operations
	// run at the download root. Preserve it for compatibility; this does not
	// establish safety for Git operations inside submodules or linked worktrees.
	gitfile := writeFile(t, root, "vendor/other/.git", "gitdir: ../../.git/modules/other\n")
	writeFile(t, root, ".git/modules/other/config", "[core]\n")
	writeFile(t, root, ".git/worktrees/wt/HEAD", "ref: refs/heads/wt\n")

	require.NoError(t, sanitizeDownload(root))

	_, err := os.Lstat(filepath.Join(root, "vendor", "dep", ".git", "refs", "replace"))
	assert.True(t, os.IsNotExist(err), "nested replace refs must be filtered")
	data, err := os.ReadFile(filepath.Join(root, "vendor", "dep", ".git", "config"))
	require.NoError(t, err)
	assert.Equal(t, "[core]\n", string(data))

	data, err = os.ReadFile(gitfile)
	require.NoError(t, err)
	assert.Equal(t, "gitdir: ../../.git/modules/other\n", string(data), "nested gitfile must survive")
}

// TestSanitizeDownload_PreservesNormalRepo checks that an ordinary repository's
// status, index, and history survive sanitization.
func TestSanitizeDownload_PreservesNormalRepo(t *testing.T) {
	requireGit(t)

	r := newGitRepo(t)
	r.commitFile("a.txt", "one")
	r.commitFile("b.txt", "two")
	require.NoError(t, os.WriteFile(filepath.Join(r.dir, "a.txt"), []byte("one\nstaged\n"), 0o644))
	r.git("add", "a.txt")
	require.NoError(t, os.WriteFile(filepath.Join(r.dir, "b.txt"), []byte("two\nunstaged\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(r.dir, "untracked.txt"), []byte("new\n"), 0o644))

	before := r.snapshot()
	require.NoError(t, sanitizeDownload(r.dir))
	assert.Equal(t, before, r.snapshot())

	r.git("commit", "--quiet", "-m", "after sanitize")
	assert.Equal(t, "after sanitize", r.git("log", "-1", "--format=%s"))
}

func TestSanitizeDownload_BlocksInventedGitAncestry(t *testing.T) {
	requireGit(t)

	cases := []struct {
		name   string
		inject func(t *testing.T, r *gitRepo, orphan, bridge string)
	}{
		{
			name: "loose replacement ref",
			inject: func(t *testing.T, r *gitRepo, orphan, bridge string) {
				r.git("replace", orphan, bridge)
			},
		},
		{
			name: "packed replacement ref",
			inject: func(t *testing.T, r *gitRepo, orphan, bridge string) {
				r.git("replace", orphan, bridge)
				r.git("pack-refs", "--all", "--prune")
				packed, err := os.ReadFile(filepath.Join(r.dir, ".git", "packed-refs"))
				require.NoError(t, err)
				require.Contains(t, string(packed), "refs/replace/"+orphan)
			},
		},
		{
			name: "graft",
			inject: func(t *testing.T, r *gitRepo, orphan, bridge string) {
				writeFile(t, r.dir, ".git/info/grafts", orphan+" "+bridge+"\n")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newGitRepo(t)
			human := r.commitFile("human.txt", "human work")
			tree := r.git("rev-parse", human+"^{tree}")
			orphan := r.git("commit-tree", tree, "-m", "agent orphan")
			bridge := r.git("commit-tree", tree, "-p", human, "-m", "bridge")
			tc.inject(t, r, orphan, bridge)

			assert.True(t, r.isAncestor(human, orphan))
			require.NoError(t, sanitizeDownload(r.dir))
			assert.False(t, r.isAncestor(human, orphan))
			assert.Equal(t, human, r.git("rev-parse", "main"))
		})
	}
}

func TestSanitizeDownload_LocalPlainPushRejectsInventedAncestry(t *testing.T) {
	requireGit(t)

	r := newGitRepo(t)
	human := r.commitFile("human.txt", "human work")
	remote := filepath.Join(t.TempDir(), "remote.git")
	r.git("init", "--bare", "--quiet", remote)
	r.git("push", "--quiet", remote, "main")

	tree := r.git("rev-parse", human+"^{tree}")
	orphan := r.git("commit-tree", tree, "-m", "agent orphan")
	bridge := r.git("commit-tree", tree, "-p", human, "-m", "bridge")
	r.git("replace", orphan, bridge)
	require.True(t, r.isAncestor(human, orphan))

	require.NoError(t, sanitizeDownload(r.dir))
	require.False(t, r.isAncestor(human, orphan))

	_, err := r.tryGit("push", "--quiet", remote, orphan+":main")
	require.Error(t, err, "plain push of divergent history must be rejected")
	assert.Equal(t, human, r.gitAt(remote, "rev-parse", "main"))
}

func TestErrSymlink_ErrorMethod(t *testing.T) {
	// Verify that errSymlink implements error and that fmt.Sprintf does not panic.
	underlying := fmt.Errorf("permission denied")

	t.Run("with target", func(t *testing.T) {
		e := &errSymlink{Path: "/repo/bad-link", Target: "/etc/passwd", Err: underlying}
		msg := e.Error()
		assert.Contains(t, msg, "/repo/bad-link")
		assert.Contains(t, msg, "/etc/passwd")
		assert.Contains(t, msg, "permission denied")

		// Verify fmt.Sprintf does not panic on errSymlink.
		formatted := fmt.Sprintf("%v", e)
		assert.NotEmpty(t, formatted)
	})

	t.Run("without target", func(t *testing.T) {
		e := &errSymlink{Path: "/repo/unreadable", Err: underlying}
		msg := e.Error()
		assert.Contains(t, msg, "/repo/unreadable")
		assert.Contains(t, msg, "permission denied")
		assert.NotContains(t, msg, "->")
	})

	t.Run("unwrap", func(t *testing.T) {
		e := &errSymlink{Path: "/repo/link", Target: "/tmp", Err: underlying}
		assert.ErrorIs(t, e, underlying)
	})
}

func TestSanitizeDownload_RemoveFailureReturnsErrSymlink(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("read-only directory trick is Linux-specific")
	}
	if os.Getuid() == 0 {
		t.Skip("root bypasses permission checks")
	}

	// Create a directory with a dangerous absolute symlink, then make the
	// parent directory read-only so os.Remove fails inside sanitizeDownload.
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(sub, "bad-link")))

	// A second dangerous symlink later in the walk — it should remain
	// unremoved because WalkDir aborts after the first error.
	require.NoError(t, os.Symlink("/etc/shadow", filepath.Join(sub, "second-link")))

	// Make sub read-only so os.Remove fails.
	require.NoError(t, os.Chmod(sub, 0o555))
	t.Cleanup(func() {
		os.Chmod(sub, 0o755) // restore for TempDir cleanup
	})

	err := sanitizeDownload(dir)
	require.Error(t, err)

	var symErr *errSymlink
	require.True(t, errors.As(err, &symErr), "expected *errSymlink, got %T: %v", err, err)
	assert.Contains(t, symErr.Path, "bad-link")
	assert.NotNil(t, symErr.Err)

	// The second symlink should still exist because the walk aborted.
	_, statErr := os.Lstat(filepath.Join(sub, "second-link"))
	assert.NoError(t, statErr, "second symlink should remain after walk aborted on first error")
}

func TestSafeDownload_RetriesIntoFreshDirectoryAfterPartialExtraction(t *testing.T) {
	requireGit(t)
	src := newGitRepo(t)

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "download-paths.log")
	counterPath := filepath.Join(binDir, "attempt-count")

	script := fmt.Sprintf(`#!/bin/sh
set -e
if [ "$2" != "download" ]; then
  exit 0
fi
dest=$5
printf '%%s\n' "$dest" >> "%s"
attempt=0
if [ -f "%s" ]; then
  attempt=$(/bin/cat "%s")
fi
attempt=$((attempt + 1))
printf '%%s\n' "$attempt" > "%s"
if [ "$attempt" -eq 1 ]; then
  /bin/mkdir -p "$dest/node_modules/@esbuild/linux-x64/bin" "$dest/node_modules/esbuild/bin"
  printf 'esbuild' > "$dest/node_modules/@esbuild/linux-x64/bin/esbuild"
  /bin/ln "$dest/node_modules/@esbuild/linux-x64/bin/esbuild" "$dest/node_modules/esbuild/bin/esbuild"
  exit 1
fi
/bin/cp -R "%s/.git" "$dest/.git"
printf 'safe\n' > "$dest/safe.txt"
/bin/ln -s /etc/passwd "$dest/dangerous-link"
exit 0
`, logPath, counterPath, counterPath, counterPath, src.dir)
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(filepath.ListSeparator)+os.Getenv("PATH"))

	parent := t.TempDir()
	localDir := filepath.Join(parent, "repo")
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, RepositorySnapshot{}))

	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	paths := strings.Split(strings.TrimSpace(string(log)), "\n")
	require.Len(t, paths, 2)
	assert.NotEqual(t, paths[0], paths[1], "retry must use a fresh staging directory")

	content, err := os.ReadFile(filepath.Join(localDir, "safe.txt"))
	require.NoError(t, err)
	assert.Equal(t, "safe\n", string(content))

	_, lstatErr := os.Lstat(filepath.Join(localDir, "dangerous-link"))
	assert.True(t, os.IsNotExist(lstatErr), "dangerous symlink must be removed before publishing")
	_, statErr := os.Lstat(filepath.Join(localDir, "node_modules"))
	assert.True(t, os.IsNotExist(statErr), "partial first attempt must not leak into the published directory")

	entries, err := os.ReadDir(parent)
	require.NoError(t, err)
	for _, entry := range entries {
		assert.False(t, strings.HasPrefix(entry.Name(), ".fullsend-safe-download-"),
			"staging directory must be removed after success: %s", entry.Name())
	}
}

func TestSafeDownload_CleansTrailingSeparator(t *testing.T) {
	requireGit(t)

	r := newGitRepo(t)
	writeFile(t, r.dir, "safe.txt", "safe\n")
	serveDownloadFrom(t, r.dir)

	parent := t.TempDir()
	localDir := filepath.Join(parent, "repo") + string(os.PathSeparator)
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, RepositorySnapshot{}))

	content, err := os.ReadFile(filepath.Join(parent, "repo", "safe.txt"))
	require.NoError(t, err)
	assert.Equal(t, "safe\n", string(content))

	entries, readErr := os.ReadDir(parent)
	require.NoError(t, readErr)
	require.Len(t, entries, 1)
	assert.Equal(t, "repo", entries[0].Name())
}

func TestSafeDownload_ExhaustsRetries(t *testing.T) {
	binDir := t.TempDir()
	script := `#!/bin/sh
if [ "$2" = "download" ]; then
  exit 1
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir)

	parent := t.TempDir()
	localDir := filepath.Join(parent, "repo")
	err := SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, RepositorySnapshot{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "safe download from sandbox failed after 2 attempts")
	assert.Contains(t, err.Error(), "attempt 1/2")
	assert.Contains(t, err.Error(), "attempt 2/2")

	_, statErr := os.Lstat(localDir)
	assert.True(t, os.IsNotExist(statErr), "failed download must not publish a partial directory")

	entries, readErr := os.ReadDir(parent)
	require.NoError(t, readErr)
	for _, entry := range entries {
		assert.False(t, strings.HasPrefix(entry.Name(), ".fullsend-safe-download-"),
			"staging directory must be removed after failure: %s", entry.Name())
	}
}

func TestSafeDownload_ReturnsErrorWhenReplacingExistingDownloadFails(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	requireGit(t)

	r := newGitRepo(t)
	writeFile(t, r.dir, "safe.txt", "safe\n")
	serveDownloadFrom(t, r.dir)

	parent := t.TempDir()
	localDir := filepath.Join(parent, "repo")
	readonlyDir := filepath.Join(localDir, "readonly")
	require.NoError(t, os.MkdirAll(readonlyDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(readonlyDir, "file.txt"), []byte("keep"), 0o644))
	require.NoError(t, os.Chmod(readonlyDir, 0o555))
	t.Cleanup(func() {
		require.NoError(t, os.Chmod(readonlyDir, 0o755))
	})

	err := SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, RepositorySnapshot{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "replacing existing download directory")
	assert.False(t, errors.Is(err, ErrRepositoryValidation), "a publication failure is not a validation failure: %v", err)
}

func TestSafeDownload_ReturnsErrorWhenSanitizationFails(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses permission checks")
	}

	binDir := t.TempDir()
	script := `#!/bin/sh
if [ "$2" = "download" ]; then
  printf 'safe\n' > "$5/safe.txt"
  /bin/ln -s /etc/passwd "$5/dangerous-link"
  /bin/mkdir "$5/readonly"
  printf 'nested\n' > "$5/readonly/nested.txt"
  /bin/chmod 0555 "$5/readonly"
  /bin/chmod 0555 "$5"
  exit 0
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir)

	parent := t.TempDir()
	localDir := filepath.Join(parent, "repo")
	t.Cleanup(func() {
		entries, readErr := os.ReadDir(parent)
		if readErr != nil {
			return
		}
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), ".fullsend-safe-download-") {
				continue
			}
			stagingDir := filepath.Join(parent, entry.Name())
			_ = os.Chmod(stagingDir, 0o755)
			_ = os.RemoveAll(stagingDir)
		}
	})

	err := SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, RepositorySnapshot{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sanitizing downloaded directory")
	assert.True(t, errors.Is(err, ErrRepositoryValidation), "want ErrRepositoryValidation, got %v", err)
	var symErr *errSymlink
	assert.True(t, errors.As(err, &symErr), "the sanitization cause must stay reachable: %v", err)

	_, statErr := os.Lstat(localDir)
	assert.True(t, os.IsNotExist(statErr), "unsafe download must not be published")

	entries, readErr := os.ReadDir(parent)
	require.NoError(t, readErr)
	for _, entry := range entries {
		assert.False(t, strings.HasPrefix(entry.Name(), ".fullsend-safe-download-"),
			"staging directory must be removed after sanitization failure: %s", entry.Name())
	}
}

func TestSafeDownload_DoesNotPublishUnsupportedGitMetadata(t *testing.T) {
	cases := map[string]string{
		"root git":                       `/bin/mkdir -p "$5/real-git" && /bin/ln -s real-git "$5/.git"`,
		"objects":                        `/bin/mkdir -p "$5/.git" && /bin/ln -s ../real-objects "$5/.git/objects"`,
		"gitfile":                        `/bin/mkdir -p "$5/real-git" && printf 'gitdir: real-git\n' > "$5/.git"`,
		"commondir":                      `/bin/mkdir -p "$5/.git" "$5/real-git" && printf '../real-git\n' > "$5/.git/commondir"`,
		"reftable symlink":               `/bin/mkdir -p "$5/.git" && /bin/ln -s ../outside "$5/.git/reftable"`,
		"nested git symlink":             `/bin/mkdir -p "$5/vendor/dep/real-git" && /bin/ln -s real-git "$5/vendor/dep/.git"`,
		"alternates":                     `/bin/mkdir -p "$5/.git/objects/info" && printf '/elsewhere/objects\n' > "$5/.git/objects/info/alternates"`,
		"http-alternates":                `/bin/mkdir -p "$5/.git/objects/info" && printf 'https://example.invalid/objects\n' > "$5/.git/objects/info/http-alternates"`,
		"shallow symlink":                `/bin/mkdir -p "$5/.git" && /bin/ln -s ../../outside "$5/.git/shallow"`,
		"shallow directory":              `/bin/mkdir -p "$5/.git/shallow"`,
		"shallow symlink case variant":   `/bin/mkdir -p "$5/.git" && /bin/ln -s ../../outside "$5/.git/SHALLOW"`,
		"shallow directory case variant": `/bin/mkdir -p "$5/.git/Shallow"`,
		"packed-refs directory":          `/bin/mkdir -p "$5/.git/packed-refs"`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			binDir := t.TempDir()
			script := "#!/bin/sh\nif [ \"$2\" = \"download\" ]; then\n" + body + "\nexit 0\nfi\nexit 0\n"
			require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
			t.Setenv("PATH", binDir)

			parent := t.TempDir()
			localDir := filepath.Join(parent, "repo")
			err := SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, RepositorySnapshot{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "sanitizing downloaded directory")
			assert.True(t, errors.Is(err, ErrRepositoryValidation), "want ErrRepositoryValidation, got %v", err)
			assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
			_, statErr := os.Lstat(localDir)
			assert.True(t, os.IsNotExist(statErr), "unsafe download must not be published")
		})
	}
}

// serveDownloadFrom makes the fake openshell download copy src's contents
// into the destination directory, so SafeDownload processes a real repository.
func serveDownloadFrom(t *testing.T, src string) {
	t.Helper()
	binDir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$2\" = \"download\" ]; then\n  /bin/cp -R \"%s/.\" \"$5/\"\nfi\nexit 0\n", src)
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	// Keep git on PATH for assertions after the download.
	t.Setenv("PATH", binDir+string(filepath.ListSeparator)+os.Getenv("PATH"))
}

// TestValidateGitCheckout checks the shared read-only preflight used for
// trusted host inputs: real directories only, native resolution, no mutation.
func TestValidateGitCheckout(t *testing.T) {
	requireGit(t)

	r := newGitRepo(t)
	r.commitFile("file.txt", "content")

	t.Run("normal checkout", func(t *testing.T) {
		require.NoError(t, validateGitCheckout(r.dir))
	})

	t.Run("missing .git", func(t *testing.T) {
		tree := t.TempDir()
		writeFile(t, tree, "file.txt", "content\n")
		err := validateGitCheckout(tree)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "checking git checkout")
	})

	t.Run("empty .git", func(t *testing.T) {
		tree := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(tree, ".git"), 0o755))
		require.Error(t, validateGitCheckout(tree))
	})

	t.Run("gitfile", func(t *testing.T) {
		tree := t.TempDir()
		writeFile(t, tree, ".git", "gitdir: "+filepath.Join(r.dir, ".git")+"\n")
		err := validateGitCheckout(tree)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a real directory")
	})

	t.Run("symlink .git rejected before query", func(t *testing.T) {
		tree := t.TempDir()
		require.NoError(t, os.Symlink(filepath.Join(r.dir, ".git"), filepath.Join(tree, ".git")))
		t.Setenv("PATH", t.TempDir()) // no Git: the layout guard must run first
		err := validateGitCheckout(tree)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a real directory")
	})

	t.Run("trusted source metadata is not modified", func(t *testing.T) {
		src := newGitRepo(t)
		src.commitFile("file.txt", "content")
		hook := writeFile(t, src.dir, ".git/hooks/pre-commit", "#!/bin/sh\nmalicious\n")
		grafts := writeFile(t, src.dir, ".git/info/grafts", "graft data\n")
		replace := writeFile(t, src.dir, ".git/refs/replace/"+strings.Repeat("a", 40), "replace data\n")

		require.NoError(t, validateGitCheckout(src.dir))

		for path, want := range map[string]string{
			hook:    "#!/bin/sh\nmalicious\n",
			grafts:  "graft data\n",
			replace: "replace data\n",
		} {
			got, err := os.ReadFile(path)
			require.NoError(t, err, "%s must survive validation", path)
			assert.Equal(t, want, string(got))
		}
	})

	t.Run("reftable checkout is read-only", func(t *testing.T) {
		requireReftableSanitization(t)
		src := newReftableRepo(t)
		src.commitFile("file.txt", "content")
		configPath := filepath.Join(src.dir, ".git", "config")
		configBefore, err := os.ReadFile(configPath)
		require.NoError(t, err)
		hook := writeFile(t, src.dir, ".git/hooks/pre-commit", "#!/bin/sh\nmalicious\n")
		grafts := writeFile(t, src.dir, ".git/info/grafts", "graft data\n")

		require.NoError(t, validateGitCheckout(src.dir))

		configAfter, err := os.ReadFile(configPath)
		require.NoError(t, err)
		assert.Equal(t, configBefore, configAfter, "reftable config bytes must be preserved")
		for path, want := range map[string]string{
			hook:   "#!/bin/sh\nmalicious\n",
			grafts: "graft data\n",
		} {
			got, err := os.ReadFile(path)
			require.NoError(t, err, "%s must survive validation", path)
			assert.Equal(t, want, string(got))
		}
	})

	t.Run("reftable rejected on git 2.52", func(t *testing.T) {
		src := newReftableRepo(t)
		src.commitFile("file.txt", "content")

		installGitShim(t, "if [ \"$1\" = \"--version\" ]; then\n  echo 'git version 2.52.0'\n  exit 0\nfi")

		err := validateGitCheckout(src.dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "2.53")
	})

	t.Run("files backend is not version gated", func(t *testing.T) {
		src := newGitRepo(t)
		src.commitFile("file.txt", "content")

		installGitShim(t, "if [ \"$1\" = \"--version\" ]; then\n  echo 'git version 2.52.0'\n  exit 0\nfi")

		require.NoError(t, validateGitCheckout(src.dir))
	})
}

func TestValidateRepositoryRoot(t *testing.T) {
	t.Run("checkout is not plain", func(t *testing.T) {
		requireGit(t)
		r := newGitRepo(t)
		r.commitFile("file.txt", "content")
		plain, err := ValidateRepositoryRoot(r.dir)
		require.NoError(t, err)
		assert.False(t, plain)
	})

	t.Run("unborn checkout is not plain", func(t *testing.T) {
		requireGit(t)
		plain, err := ValidateRepositoryRoot(newGitRepo(t).dir)
		require.NoError(t, err)
		assert.False(t, plain)
	})

	t.Run("gitfile is never plain", func(t *testing.T) {
		tree := t.TempDir()
		writeFile(t, tree, ".git", "gitdir: elsewhere\n")
		plain, err := ValidateRepositoryRoot(tree)
		require.Error(t, err)
		assert.False(t, plain)
		assert.Contains(t, err.Error(), "not a real directory")
	})

	cases := []struct {
		name    string
		entries []string
		wantErr bool
	}{
		{name: "empty directory", entries: nil},
		{name: "head only", entries: []string{"HEAD"}},
		{name: "objects only", entries: []string{"objects"}},
		{name: "refs only", entries: []string{"refs"}},
		{name: "commondir only", entries: []string{"commondir"}},
		{name: "head and objects", entries: []string{"HEAD", "objects"}},
		{name: "head and refs", entries: []string{"HEAD", "refs"}},
		{name: "objects and refs", entries: []string{"objects", "refs"}},
		{name: "head and commondir", entries: []string{"HEAD", "commondir"}, wantErr: true},
		{name: "bare root", entries: []string{"HEAD", "objects", "refs"}, wantErr: true},
		{name: "bare root with commondir", entries: []string{"HEAD", "commondir", "objects", "refs"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tree := t.TempDir()
			for _, name := range tc.entries {
				writeFile(t, tree, name, "x\n")
			}
			plain, err := ValidateRepositoryRoot(tree)
			if tc.wantErr {
				require.Error(t, err)
				assert.False(t, plain)
				assert.Contains(t, err.Error(), "checking git checkout")
				return
			}
			require.NoError(t, err)
			assert.True(t, plain, "a root without .git or Git directory metadata is plain")
		})
	}

	t.Run("dangling symlink marker still counts", func(t *testing.T) {
		tree := t.TempDir()
		writeFile(t, tree, "HEAD", "ref: refs/heads/main\n")
		require.NoError(t, os.Symlink("missing", filepath.Join(tree, "commondir")))
		plain, err := ValidateRepositoryRoot(tree)
		require.Error(t, err)
		assert.False(t, plain)
	})

	t.Run("dangling symlink bare root still counts", func(t *testing.T) {
		tree := t.TempDir()
		require.NoError(t, os.Symlink("missing", filepath.Join(tree, "HEAD")))
		writeFile(t, tree, "objects", "x\n")
		writeFile(t, tree, "refs", "x\n")
		plain, err := ValidateRepositoryRoot(tree)
		require.Error(t, err)
		assert.False(t, plain)
	})

	t.Run("non-directory root fails closed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "root")
		require.NoError(t, os.WriteFile(path, []byte("x\n"), 0o644))
		plain, err := ValidateRepositoryRoot(path)
		require.Error(t, err)
		assert.False(t, plain)
	})
}

// requireFIFO creates a FIFO so a test can prove the timed checkout query
// cannot be blocked by a special entry.
func requireFIFO(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, syscall.Mkfifo(path, 0o600))
	return path
}

// TestSafeDownload_AcceptsReftableCheckout publishes a sanitized reftable
// checkout with its HEAD and refs intact and replacement refs removed.
func TestSafeDownload_AcceptsReftableCheckout(t *testing.T) {
	requireReftableSanitization(t)
	r := newReftableRepo(t)
	human := r.commitFile("file.txt", "original")
	tree := r.git("rev-parse", human+"^{tree}")
	orphan := r.git("commit-tree", tree, "-m", "agent orphan")
	bridge := r.git("commit-tree", tree, "-p", human, "-m", "bridge")
	r.git("replace", orphan, bridge)
	require.True(t, r.isAncestor(human, orphan))

	serveDownloadFrom(t, r.dir)
	localDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir,
		RepositorySnapshot{}))

	assert.Equal(t, human, r.gitAt(localDir, "rev-parse", "HEAD"))
	assert.Equal(t, human, r.gitAt(localDir, "rev-parse", "main"))
	out, err := runGit(t, localDir, r.env, "replace", "--list")
	require.NoError(t, err)
	assert.Empty(t, out, "replacement refs must not be published")
}

// TestSafeDownload_RequiresGitCheckout accepts a real host checkout and
// preserves its HEAD and worktree.
func TestSafeDownload_RequiresGitCheckout(t *testing.T) {
	requireGit(t)

	r := newGitRepo(t)
	r.commitFile("file.txt", "content")
	head := r.git("rev-parse", "HEAD")

	serveDownloadFrom(t, r.dir)
	localDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir,
		RepositorySnapshot{}))

	got, err := runGit(t, localDir, r.env, "rev-parse", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, head, got)
	status, err := runGit(t, localDir, r.env, "status", "--porcelain=v1")
	require.NoError(t, err)
	assert.Empty(t, status)
	data, err := os.ReadFile(filepath.Join(localDir, "file.txt"))
	require.NoError(t, err)
	assert.Equal(t, "content\n", string(data))
}

// TestSafeDownload_RejectsPlainOutputForCheckoutInput covers the regression
// where a checkout input returned a plain tree: the sandbox can remove .git,
// and publishing that tree would let later host Git discovery resolve metadata
// through a parent repository. The download must fail before publication.
func TestSafeDownload_RejectsPlainOutputForCheckoutInput(t *testing.T) {
	requireGit(t)

	r := newGitRepo(t)
	r.commitFile("file.txt", "content")

	// A real, non-shallow checkout as the remote tree, with the sandbox-side
	// .git removed.
	downloaded := filepath.Join(t.TempDir(), "downloaded")
	require.NoError(t, os.CopyFS(downloaded, os.DirFS(r.dir)))
	require.NoError(t, os.RemoveAll(filepath.Join(downloaded, ".git")))

	reject := func(t *testing.T, localDir string) {
		t.Helper()
		serveDownloadFrom(t, downloaded)
		err := SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir,
			RepositorySnapshot{IsCheckout: true})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must remain a git checkout")
		assert.True(t, errors.Is(err, ErrRepositoryValidation), "want ErrRepositoryValidation, got %v", err)
		assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
	}

	t.Run("absent destination remains absent", func(t *testing.T) {
		localDir := filepath.Join(t.TempDir(), "repo")
		reject(t, localDir)
		_, statErr := os.Lstat(localDir)
		assert.True(t, os.IsNotExist(statErr), "a rejected checkout must not be published")
	})

	t.Run("existing destination is preserved", func(t *testing.T) {
		localDir := filepath.Join(t.TempDir(), "repo")
		sentinel := writeFile(t, localDir, "sentinel.txt", "keep\n")
		reject(t, localDir)
		data, readErr := os.ReadFile(sentinel)
		require.NoError(t, readErr)
		assert.Equal(t, "keep\n", string(data), "a rejected download must not replace the existing destination")
	})
}

// TestSafeDownload_AcceptsCheckoutForCheckoutInput keeps the positive
// checkout-to-checkout path: a required-checkout snapshot accepts a real
// checkout output.
func TestSafeDownload_AcceptsCheckoutForCheckoutInput(t *testing.T) {
	requireGit(t)

	r := newGitRepo(t)
	r.commitFile("file.txt", "content")
	head := r.git("rev-parse", "HEAD")

	serveDownloadFrom(t, r.dir)
	localDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir,
		RepositorySnapshot{IsCheckout: true}))

	got, err := runGit(t, localDir, r.env, "rev-parse", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, head, got)
}

// TestSafeDownload_PlainInputMayPublishCheckout keeps the plain-to-checkout
// path: without IsCheckout, a plain host input may come back as a valid
// checkout that keeps the normal sanitization and validation.
func TestSafeDownload_PlainInputMayPublishCheckout(t *testing.T) {
	requireGit(t)

	r := newGitRepo(t)
	r.commitFile("file.txt", "content")
	head := r.git("rev-parse", "HEAD")

	serveDownloadFrom(t, r.dir)
	localDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir,
		RepositorySnapshot{}))

	got, err := runGit(t, localDir, r.env, "rev-parse", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, head, got)
}

func TestSafeDownload_PublishesPlainDirectory(t *testing.T) {
	tree := t.TempDir()
	writeFile(t, tree, "file.txt", "content\n")

	serveDownloadFrom(t, tree)
	localDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir,
		RepositorySnapshot{}))

	data, err := os.ReadFile(filepath.Join(localDir, "file.txt"))
	require.NoError(t, err)
	assert.Equal(t, "content\n", string(data))
	_, err = os.Lstat(filepath.Join(localDir, ".git"))
	assert.True(t, os.IsNotExist(err), "a plain download must not gain git metadata")
}

// TestSafeDownload_RejectsMissingOrInvalidRootCheckout rejects, without
// publishing, roots that look like a repository but are neither a checkout nor
// a plain directory; there is no discovery fallback to the bare root.
func TestSafeDownload_RejectsMissingOrInvalidRootCheckout(t *testing.T) {
	requireGit(t)

	r := newGitRepo(t)
	r.commitFile("file.txt", "content")

	reject := func(t *testing.T, src string) error {
		t.Helper()
		serveDownloadFrom(t, src)
		localDir := filepath.Join(t.TempDir(), "repo")
		err := SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir,
			RepositorySnapshot{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "checking git checkout")
		assert.True(t, errors.Is(err, ErrRepositoryValidation), "want ErrRepositoryValidation, got %v", err)
		_, statErr := os.Lstat(localDir)
		assert.True(t, os.IsNotExist(statErr), "a rejected checkout must not be published")
		return err
	}

	t.Run("root git directory metadata", func(t *testing.T) {
		tree := t.TempDir()
		writeFile(t, tree, "HEAD", "ref: refs/heads/main\n")
		writeFile(t, tree, "commondir", "../.git\n")
		err := reject(t, tree)
		assert.True(t, errors.Is(err, ErrRepositoryRejected), "a bare-shaped root is detected directly: %v", err)
	})

	t.Run("bare root", func(t *testing.T) {
		bare := filepath.Join(t.TempDir(), "bare.git")
		r.git("clone", "--bare", "--quiet", r.dir, bare)
		assert.Equal(t, "true", r.gitAt(bare, "rev-parse", "--is-bare-repository"),
			"native Git must recognize the bare root")
		err := reject(t, bare)
		assert.True(t, errors.Is(err, ErrRepositoryRejected), "a bare root is detected directly: %v", err)
	})

	t.Run("empty .git beside bare root", func(t *testing.T) {
		tree := t.TempDir()
		writeFile(t, tree, "HEAD", r.git("rev-parse", "HEAD")+"\n")
		writeFile(t, tree, "objects/pack/source.idx", "source\n")
		writeFile(t, tree, "refs/heads/main", r.git("rev-parse", "HEAD")+"\n")
		require.NoError(t, os.Mkdir(filepath.Join(tree, ".git"), 0o755))
		err := reject(t, tree)
		assert.False(t, errors.Is(err, ErrRepositoryRejected), "a Git query failure is not a rejection: %v", err)
	})

	t.Run("invalid .git layout", func(t *testing.T) {
		tree := t.TempDir()
		writeFile(t, tree, ".git/HEAD", "ref: refs/heads/main\n")
		err := reject(t, tree)
		assert.False(t, errors.Is(err, ErrRepositoryRejected), "a Git layout query failure is not a rejection: %v", err)
	})
}

// TestSafeDownload_RejectsSpecialHEAD times out a checkout whose .git/HEAD is a
// FIFO instead of publishing it.
func TestSafeDownload_RejectsSpecialHEAD(t *testing.T) {
	requireGit(t)

	tree := t.TempDir()
	writeFile(t, tree, ".git/objects/pack/source.idx", "source\n")
	writeFile(t, tree, ".git/refs/heads/main", strings.Repeat("0", 40)+"\n")
	requireFIFO(t, filepath.Join(tree, ".git"), "HEAD")

	serveDownloadFrom(t, tree)
	localDir := filepath.Join(t.TempDir(), "repo")
	err := SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir,
		RepositorySnapshot{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRepositoryValidation), "want ErrRepositoryValidation, got %v", err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded), "want a deadline error, got %v", err)
	assert.False(t, errors.Is(err, ErrRepositoryRejected), "a timeout is not a rejection: %v", err)
	_, statErr := os.Lstat(localDir)
	assert.True(t, os.IsNotExist(statErr), "a special HEAD must not be published")
}

// TestSafeDownload_CheckoutQueryFailures makes the native layout query fail or
// print the wrong path; every result must be a checkout error, and an
// unqueryable Git must fail closed.
func TestSafeDownload_CheckoutQueryFailures(t *testing.T) {
	requireGit(t)

	r := newGitRepo(t)
	r.commitFile("file.txt", "content")

	installDownloadAndGit := func(t *testing.T, gitBody string) {
		t.Helper()
		binDir := t.TempDir()
		script := fmt.Sprintf("#!/bin/sh\nif [ \"$2\" = \"download\" ]; then\n  /bin/cp -R \"%s/.\" \"$5/\"\nfi\nexit 0\n", r.dir)
		require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
		if gitBody != "" {
			require.NoError(t, os.WriteFile(filepath.Join(binDir, "git"), []byte("#!/bin/sh\n"+gitBody+"\n"), 0o755))
		}
		t.Setenv("PATH", binDir)
	}

	cases := []struct {
		name    string
		gitBody string
		want    string
	}{
		{"status failure", "exit 129", "checking git checkout"},
		{"malformed output", "printf 'junk\\n'", "git directory resolved to"},
		{"git unavailable", "", "checking git checkout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			installDownloadAndGit(t, tc.gitBody)
			localDir := filepath.Join(t.TempDir(), "repo")
			err := SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir,
				RepositorySnapshot{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			_, statErr := os.Lstat(localDir)
			assert.True(t, os.IsNotExist(statErr), "a failed query must not publish")
		})
	}
}

// TestSafeDownload_CheckoutQueryIgnoresEnvironment proves inherited Git
// variables and repository config cannot redirect or trace the layout query.
func TestSafeDownload_CheckoutQueryIgnoresEnvironment(t *testing.T) {
	requireGit(t)

	decoy := newGitRepo(t)
	decoy.commitFile("decoy.txt", "decoy")

	r := newGitRepo(t)
	r.commitFile("file.txt", "content")
	tracePath := filepath.Join(t.TempDir(), "trace2.log")
	t.Setenv("GIT_DIR", filepath.Join(decoy.dir, ".git"))
	t.Setenv("GIT_COMMON_DIR", filepath.Join(decoy.dir, ".git"))
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(decoy.dir, ".git", "objects"))
	t.Setenv("GIT_TRACE2", tracePath)

	serveDownloadFrom(t, r.dir)
	localDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir,
		RepositorySnapshot{}))
	assert.NoFileExists(t, tracePath)
}

// shallowCommitCount returns Git's commit count for HEAD in dir, which reflects
// the root shallow boundary Git is using.
func shallowCommitCount(t *testing.T, dir string, env []string) string {
	t.Helper()
	out, err := runGit(t, dir, env, "rev-list", "--count", "HEAD")
	require.NoError(t, err)
	return out
}

func TestSafeDownload_PreservesTrustedShallowBoundary(t *testing.T) {
	requireGit(t)

	cases := []struct {
		name            string
		trusted         string // "", "head", or "middle"
		downloaded      string // "", "head", or "middle"
		wantCount       string // published count when the change is allowed
		downloadedCount string // baseline count with the untrusted marker
	}{
		{"deeper sandbox boundary is rejected", "head", "middle", "", "2"},
		{"sandbox cutoff in a full repository is rejected", "", "middle", "", "2"},
		{"trusted boundary is recreated when the sandbox removed it", "head", "", "1", ""},
		{"full history stays full when the sandbox removed the marker", "", "", "3", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := gitTestEnv(t.TempDir())
			r := newGitRepo(t)
			r.commitFile("one.txt", "one")
			middle := r.commitFile("two.txt", "two")
			head := r.commitFile("three.txt", "three")
			oid := func(which string) string {
				switch which {
				case "head":
					return head
				case "middle":
					return middle
				}
				t.Fatalf("unknown boundary %q", which)
				return ""
			}
			shallowPath := filepath.Join(r.dir, ".git", "shallow")
			if tc.downloaded != "" {
				require.NoError(t, os.WriteFile(shallowPath, []byte(oid(tc.downloaded)+"\n"), 0o644))
				assert.Equal(t, tc.downloadedCount, shallowCommitCount(t, r.dir, env),
					"fixture must use the untrusted boundary")
			}
			trusted := RepositorySnapshot{}
			if tc.trusted != "" {
				trusted = RepositorySnapshot{Shallow: ShallowState{Present: true, Data: []byte(oid(tc.trusted) + "\n")}}
			}
			serveDownloadFrom(t, r.dir)
			localDir := filepath.Join(t.TempDir(), "repo")

			if tc.wantCount == "" {
				err := SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, trusted)
				require.Error(t, err)
				assert.Contains(t, err.Error(), "unsupported sandbox shallow boundary change")
				assert.Contains(t, err.Error(), "fetch --depth")
				assert.NoDirExists(t, localDir, "a rejected boundary change must not be published")
				return
			}

			require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, trusted))
			assert.Equal(t, tc.wantCount, shallowCommitCount(t, localDir, env))
			got, err := os.ReadFile(filepath.Join(localDir, ".git", "shallow"))
			if !trusted.Shallow.Present {
				assert.True(t, os.IsNotExist(err), "untrusted shallow metadata must not be published")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, trusted.Shallow.Data, got)
		})
	}
}

// TestSafeDownload_RejectsShallowCloneAddedCutoff uses a real local file
// transport clone at depth 1, whose boundary commit is missing its parent, to
// show that a valid Git shallow fetch can add a cutoff absent from the trusted
// snapshot and is rejected before publication.
func TestSafeDownload_RejectsShallowCloneAddedCutoff(t *testing.T) {
	requireGit(t)
	env := gitTestEnv(t.TempDir())

	src := newGitRepo(t)
	src.commitFile("one.txt", "one")
	middle := src.commitFile("two.txt", "two")
	head := src.commitFile("three.txt", "three")

	downloaded := t.TempDir()
	out, err := runGit(t, downloaded, env, "clone", "--quiet", "--depth=1", "file://"+src.dir, ".")
	require.NoError(t, err, out)
	marker, err := os.ReadFile(filepath.Join(downloaded, ".git", "shallow"))
	require.NoError(t, err, "the depth-1 clone must carry a shallow marker")
	require.Equal(t, head+"\n", string(marker), "the clone boundary must be the new tip")
	require.NotEqual(t, middle, strings.TrimSpace(string(marker)), "the trusted boundary must differ")
	r := &gitRepo{t: t, dir: downloaded, env: env}
	assert.Equal(t, "1", r.git("rev-list", "--count", "HEAD"),
		"Git must read the downloaded repository with its own marker")

	trusted := RepositorySnapshot{Shallow: ShallowState{Present: true, Data: []byte(middle + "\n")}}
	serveDownloadFrom(t, downloaded)
	localDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, os.MkdirAll(localDir, 0o755))
	prior := filepath.Join(localDir, "prior.txt")
	require.NoError(t, os.WriteFile(prior, []byte("prior\n"), 0o644))

	err = SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, trusted)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRepositoryValidation), "want ErrRepositoryValidation, got %v", err)
	assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
	assert.Contains(t, err.Error(), "unsupported sandbox shallow boundary change")
	assert.Contains(t, err.Error(), "fetch --depth")
	got, err := os.ReadFile(prior)
	require.NoError(t, err)
	assert.Equal(t, "prior\n", string(got), "an earlier output must remain untouched")
	_, err = os.Lstat(filepath.Join(localDir, ".git"))
	assert.True(t, os.IsNotExist(err), "the rejected download must not be published")
}

// TestSafeDownload_FailurePhaseMarkers tags failures inside post-download
// validation with the phase marker, leaves transport failures untagged, and
// preserves every original cause.
func TestSafeDownload_FailurePhaseMarkers(t *testing.T) {
	t.Run("download failure", func(t *testing.T) {
		binDir := t.TempDir()
		script := "#!/bin/sh\nif [ \"$2\" = \"download\" ]; then\n  exit 1\nfi\nexit 0\n"
		require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
		t.Setenv("PATH", binDir)

		localDir := filepath.Join(t.TempDir(), "repo")
		err := SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, RepositorySnapshot{})
		require.Error(t, err)
		assert.False(t, errors.Is(err, ErrRepositoryValidation), "a failed transfer is not a validation failure: %v", err)
		var exitErr *exec.ExitError
		assert.True(t, errors.As(err, &exitErr), "the transfer cause must stay reachable: %v", err)
	})

	t.Run("git query failure", func(t *testing.T) {
		r := newGitRepo(t)
		r.commitFile("file.txt", "content")

		binDir := t.TempDir()
		download := fmt.Sprintf("#!/bin/sh\nif [ \"$2\" = \"download\" ]; then\n  /bin/cp -R \"%s/.\" \"$5/\"\nfi\nexit 0\n", r.dir)
		require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(download), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(binDir, "git"), []byte("#!/bin/sh\nexit 129\n"), 0o755))
		t.Setenv("PATH", binDir)

		localDir := filepath.Join(t.TempDir(), "repo")
		err := SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, RepositorySnapshot{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "checking git checkout")
		assert.True(t, errors.Is(err, ErrRepositoryValidation), "a Git query failure happens in root validation: %v", err)
		assert.False(t, errors.Is(err, ErrRepositoryRejected), "a Git query failure is not a rejection: %v", err)
	})

	t.Run("filesystem failure during sanitization", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root bypasses permission checks")
		}
		binDir := t.TempDir()
		script := `#!/bin/sh
if [ "$2" = "download" ]; then
  printf 'safe\n' > "$5/safe.txt"
  /bin/ln -s /etc/passwd "$5/dangerous-link"
  /bin/chmod 0555 "$5"
  exit 0
fi
exit 0
`
		require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
		t.Setenv("PATH", binDir)

		parent := t.TempDir()
		t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
		localDir := filepath.Join(parent, "repo")
		err := SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, RepositorySnapshot{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sanitizing downloaded directory")
		assert.True(t, errors.Is(err, ErrRepositoryValidation), "a sanitization failure happens in the validation phase: %v", err)
		assert.False(t, errors.Is(err, ErrRepositoryRejected), "a filesystem failure is not a rejection: %v", err)
		var symErr *errSymlink
		assert.True(t, errors.As(err, &symErr), "the filesystem cause must stay reachable: %v", err)
	})
}

// TestRestoreTrustedShallow_EntrySets checks the boundary-entry contract:
// returned entries must be a subset of the trusted set, while order,
// duplicates, removals, and an empty marker are tolerated.
func TestRestoreTrustedShallow_EntrySets(t *testing.T) {
	first := strings.Repeat("1", 40)
	second := strings.Repeat("2", 40)
	root := newGitRepo(t).commitFile("root.txt", "root")
	present := func(entries ...string) ShallowState {
		return ShallowState{Present: true, Data: []byte(strings.Join(entries, "\n") + "\n")}
	}
	cases := []struct {
		name     string
		trusted  ShallowState
		returned string
		absent   bool
		wantErr  bool
	}{
		{name: "unchanged marker restores the trusted bytes", trusted: present(first), returned: first + "\n"},
		{name: "reordered and duplicated entries are allowed", trusted: present(first, second), returned: second + "\n" + first + "\n" + second + "\n"},
		{name: "removal is allowed", trusted: present(first, second), returned: first + "\n"},
		{name: "empty returned file is allowed when trusted is absent", returned: ""},
		{name: "absent returned marker is allowed when trusted is absent", absent: true},
		{name: "added cutoff is rejected", trusted: present(first), returned: first + "\n" + second + "\n", wantErr: true},
		{name: "malformed added entry is rejected", trusted: present(first), returned: first + "\njunk\n", wantErr: true},
		{name: "new zero-parent root cutoff is rejected", returned: root + "\n", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			localDir := t.TempDir()
			gitDir := filepath.Join(localDir, ".git")
			require.NoError(t, os.Mkdir(gitDir, 0o755))
			shallowPath := filepath.Join(gitDir, "shallow")
			if !tc.absent {
				require.NoError(t, os.WriteFile(shallowPath, []byte(tc.returned), 0o644))
			}

			err := restoreTrustedShallow(localDir, tc.trusted)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "unsupported sandbox shallow boundary change")
				assert.Contains(t, err.Error(), "fetch --depth")
				got, readErr := os.ReadFile(shallowPath)
				require.NoError(t, readErr)
				assert.Equal(t, tc.returned, string(got), "a rejected boundary must be left untouched")
				return
			}
			require.NoError(t, err)
			got, readErr := os.ReadFile(shallowPath)
			if !tc.trusted.Present {
				assert.True(t, os.IsNotExist(readErr), "trusted absence must remove the marker")
				return
			}
			require.NoError(t, readErr)
			assert.Equal(t, tc.trusted.Data, got, "trusted bytes must be restored exactly")
		})
	}
}

// TestSafeDownload_RestoresTrustedShallowWhenBoundaryCommitIsRewritten covers a
// sandbox that rewrote history so the trusted boundary commit is no longer on
// the downloaded branch. Restoration stays byte-exact, so the published
// repository carries a shallow entry for a commit the new history does not
// reach; these cases establish that Git still reads the repository, that the
// rewritten candidate history and worktree survive, and that the stale entry
// neither errors nor re-introduces the dropped commit.
func TestSafeDownload_RestoresTrustedShallowWhenBoundaryCommitIsRewritten(t *testing.T) {
	requireGit(t)

	cases := []struct {
		name string
		// rewrite mutates the fixture after the trusted boundary is captured and
		// returns the subjects the rewritten branch should expose, newest first.
		rewrite func(t *testing.T, r *gitRepo, one, middle string) []string
	}{
		{
			name: "squash rewrite leaves the boundary commit unreachable but stored",
			rewrite: func(t *testing.T, r *gitRepo, one, middle string) []string {
				r.git("reset", "--quiet", "--soft", one)
				r.git("commit", "--quiet", "-s", "-m", "squashed")
				_, err := runGit(t, r.dir, r.env, "merge-base", "--is-ancestor", middle, "HEAD")
				require.Error(t, err, "rewritten history must not contain the trusted boundary commit")
				_, err = runGit(t, r.dir, r.env, "cat-file", "-e", middle)
				require.NoError(t, err, "an unpruned squash keeps the old boundary object")
				return []string{"squashed", "one"}
			},
		},
		{
			name: "rebase rewrite prunes the boundary object",
			rewrite: func(t *testing.T, r *gitRepo, one, middle string) []string {
				r.git("checkout", "--quiet", "-b", "upstream", one)
				require.NoError(t, os.WriteFile(filepath.Join(r.dir, "upstream.txt"), []byte("upstream\n"), 0o644))
				r.git("add", "upstream.txt")
				r.git("commit", "--quiet", "-s", "-m", "upstream")
				r.git("checkout", "--quiet", "main")
				r.git("rebase", "--quiet", "upstream")
				r.git("reflog", "expire", "--expire=now", "--all")
				r.git("gc", "--quiet", "--prune=now")
				_, err := runGit(t, r.dir, r.env, "merge-base", "--is-ancestor", middle, "HEAD")
				require.Error(t, err, "rewritten history must not contain the trusted boundary commit")
				_, err = runGit(t, r.dir, r.env, "cat-file", "-e", middle)
				require.Error(t, err, "pruning must drop the trusted boundary object")
				return []string{"three", "two", "upstream", "one"}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newGitRepo(t)
			one := r.commitFile("one.txt", "one")
			middle := r.commitFile("two.txt", "two")
			r.commitFile("three.txt", "three")
			wantSubjects := tc.rewrite(t, r, one, middle)
			head := r.git("rev-parse", "HEAD")

			trusted := RepositorySnapshot{Shallow: ShallowState{Present: true, Data: []byte(middle + "\n")}}
			serveDownloadFrom(t, r.dir)
			localDir := filepath.Join(t.TempDir(), "repo")
			require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, trusted))

			got, err := os.ReadFile(filepath.Join(localDir, ".git", "shallow"))
			require.NoError(t, err)
			assert.Equal(t, trusted.Shallow.Data, got)

			assert.Equal(t, head, r.gitAt(localDir, "rev-parse", "HEAD"),
				"rewritten candidate commit must survive the download")
			assert.Equal(t, strings.Join(wantSubjects, "\n"), r.gitAt(localDir, "log", "--format=%s"))
			assert.Equal(t, fmt.Sprintf("%d", len(wantSubjects)), shallowCommitCount(t, localDir, r.env))

			status, err := runGit(t, localDir, r.env, "status", "--porcelain")
			require.NoError(t, err, "Git must read the published repository")
			assert.Empty(t, status, "the restored branch boundary must not alter the worktree")
			_, err = runGit(t, localDir, r.env, "merge-base", "--is-ancestor", one, "HEAD")
			require.NoError(t, err, "history traversal must work after the stale boundary is restored")
		})
	}
}

func TestSafeDownload_RejectsTrustedShallowWithoutGitMetadata(t *testing.T) {
	binDir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$2\" = \"download\" ]; then\n  printf 'work\\n' > \"$5/file.txt\"\nfi\nexit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir)

	localDir := filepath.Join(t.TempDir(), "repo")
	err := SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir,
		RepositorySnapshot{Shallow: ShallowState{Present: true, Data: []byte("0123456789012345678901234567890123456789\n")}})
	require.Error(t, err)
	// A plain output is a supported root, but a trusted shallow boundary needs
	// git metadata, so restoration fails closed rather than creating it.
	assert.Contains(t, err.Error(), "restoring trusted git shallow metadata")
	_, statErr := os.Lstat(localDir)
	assert.True(t, os.IsNotExist(statErr), "unsafe download must not be published")
}

func TestRestoreTrustedShallow_ErrorPaths(t *testing.T) {
	t.Run("shallow path cannot be inspected", func(t *testing.T) {
		localDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(localDir, ".git"), []byte("gitdir: elsewhere\n"), 0o644))

		err := restoreTrustedShallow(localDir, ShallowState{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "inspecting git shallow metadata")
	})

	t.Run("git entry is not a directory", func(t *testing.T) {
		localDir := t.TempDir()
		require.NoError(t, os.Symlink("missing", filepath.Join(localDir, ".git")))

		err := restoreTrustedShallow(localDir, ShallowState{Present: true, Data: []byte("0123456789012345678901234567890123456789\n")})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is not a directory")
	})
}

func TestRestoreTrustedShallow_PermissionFailures(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses permission checks")
	}

	t.Run("removing untrusted shallow fails", func(t *testing.T) {
		localDir := t.TempDir()
		gitDir := filepath.Join(localDir, ".git")
		require.NoError(t, os.Mkdir(gitDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(gitDir, "shallow"), nil, 0o644))
		require.NoError(t, os.Chmod(gitDir, 0o555))
		t.Cleanup(func() { _ = os.Chmod(gitDir, 0o755) })

		err := restoreTrustedShallow(localDir, ShallowState{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "removing untrusted git shallow metadata")
	})

	t.Run("reading downloaded shallow fails", func(t *testing.T) {
		localDir := t.TempDir()
		gitDir := filepath.Join(localDir, ".git")
		require.NoError(t, os.Mkdir(gitDir, 0o755))
		shallowPath := filepath.Join(gitDir, "shallow")
		require.NoError(t, os.WriteFile(shallowPath, []byte("0123456789012345678901234567890123456789\n"), 0o644))
		require.NoError(t, os.Chmod(shallowPath, 0))
		t.Cleanup(func() { _ = os.Chmod(shallowPath, 0o644) })

		err := restoreTrustedShallow(localDir, ShallowState{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reading downloaded git shallow metadata")
	})

	t.Run("writing trusted shallow fails", func(t *testing.T) {
		localDir := t.TempDir()
		gitDir := filepath.Join(localDir, ".git")
		require.NoError(t, os.Mkdir(gitDir, 0o555))
		t.Cleanup(func() { _ = os.Chmod(gitDir, 0o755) })

		err := restoreTrustedShallow(localDir, ShallowState{Present: true, Data: []byte("0123456789012345678901234567890123456789\n")})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "writing trusted git shallow metadata")
	})
}

func TestSafeDownload_ReturnsErrorWhenSanitizationCleanupFails(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses permission checks")
	}

	binDir := t.TempDir()
	script := `#!/bin/sh
if [ "$2" = "download" ]; then
  printf 'safe\n' > "$5/safe.txt"
  /bin/ln -s /etc/passwd "$5/dangerous-link"
  /bin/chmod 0555 "$5"
  staging_parent="${5%/*}"
  /bin/chmod 0555 "$staging_parent"
  exit 0
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir)

	parent := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, os.Chmod(parent, 0o755))
	})
	localDir := filepath.Join(parent, "repo")

	err := SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, RepositorySnapshot{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sanitizing downloaded directory")
	assert.Contains(t, err.Error(), "cleaning staging directory after sanitization failure")
	assert.Contains(t, err.Error(), "removing staging directory")
}

func TestCleanupStagingDir_MissingPath(t *testing.T) {
	require.NoError(t, cleanupStagingDir(filepath.Join(t.TempDir(), "missing")))
}

func TestEffectiveReadyTimeout_Default(t *testing.T) {
	t.Setenv("FULLSEND_SANDBOX_READY_TIMEOUT", "")
	got := effectiveReadyTimeout(0)
	assert.Equal(t, readyTimeout, got)
}

func TestEffectiveReadyTimeout_Override(t *testing.T) {
	got := effectiveReadyTimeout(90 * time.Second)
	assert.Equal(t, 90*time.Second, got)
}

func TestEffectiveReadyTimeout_EnvVar(t *testing.T) {
	t.Setenv("FULLSEND_SANDBOX_READY_TIMEOUT", "180s")
	got := effectiveReadyTimeout(0)
	assert.Equal(t, 180*time.Second, got)
}

func TestEffectiveReadyTimeout_OverrideTakesPrecedenceOverEnv(t *testing.T) {
	t.Setenv("FULLSEND_SANDBOX_READY_TIMEOUT", "180s")
	got := effectiveReadyTimeout(90 * time.Second)
	assert.Equal(t, 90*time.Second, got)
}

func TestEffectiveReadyTimeout_InvalidEnvVar(t *testing.T) {
	t.Setenv("FULLSEND_SANDBOX_READY_TIMEOUT", "not-a-duration")
	got := effectiveReadyTimeout(0)
	assert.Equal(t, readyTimeout, got)
}

func TestEffectiveReadyTimeout_NegativeEnvVar(t *testing.T) {
	t.Setenv("FULLSEND_SANDBOX_READY_TIMEOUT", "-30s")
	got := effectiveReadyTimeout(0)
	assert.Equal(t, readyTimeout, got)
}

func TestCreateWithRetry_OpenshellNotInPath(t *testing.T) {
	t.Setenv("PATH", "")

	err := CreateWithRetry("test-sandbox", nil, "", "", 1, 0, false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "sandbox creation failed after 1 attempts")
}

func TestCreateWithRetry_ZeroAttempts(t *testing.T) {
	err := CreateWithRetry("test-sandbox", nil, "", "", 0, 0, false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "maxAttempts must be >= 1")
}

func TestCreateWithRetry_NegativeAttempts(t *testing.T) {
	err := CreateWithRetry("test-sandbox", nil, "", "", -1, 0, false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "maxAttempts must be >= 1")
}

func TestCreateWithRetry_SleepsBetweenAttempts(t *testing.T) {
	t.Setenv("PATH", "")

	var sleeps []time.Duration
	orig := RetrySleepFn
	RetrySleepFn = func(d time.Duration) { sleeps = append(sleeps, d) }
	t.Cleanup(func() { RetrySleepFn = orig })

	err := CreateWithRetry("test-sandbox", nil, "", "", 3, 0, false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "sandbox creation failed after 3 attempts")
	assert.Equal(t, []time.Duration{retryInitialBackoff, retryInitialBackoff * 2}, sleeps)
}

func TestCreateOnce_DetachedPersistentCommand(t *testing.T) {
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "args.log")

	// Fake openshell: logs create args and always reports Ready on get.
	script := fmt.Sprintf(`#!/bin/sh
if [ "$2" = "create" ]; then
  echo "$@" >> %s
  exit 0
fi
if [ "$2" = "get" ]; then
  echo "Phase: Ready"
  exit 0
fi
exit 0
`, argsLog)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir)

	err := createOnce("test-sandbox", nil, "", "", 10*time.Second)
	assert.NoError(t, err)

	logged, readErr := os.ReadFile(argsLog)
	require.NoError(t, readErr)
	args := string(logged)
	assert.Contains(t, args, "--detach",
		"sandbox create must use --detach for persistent sandboxes")
	assert.Contains(t, strings.TrimSpace(args), "--detach -- sleep infinity",
		"the persistent command must be the trailing argument sequence")
	assert.NotContains(t, args, "-- true",
		"sandbox create must not use 'true' as the command (breaks OpenShell 0.0.111+)")
}

func TestCreate_PassesReadOnlyImageMountsToPodmanDriver(t *testing.T) {
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "args.log")
	script := fmt.Sprintf(`#!/bin/sh
if [ "$2" = "create" ]; then
  printf '%%s\n' "$@" >> %s
  exit 0
fi
if [ "$2" = "get" ]; then
  echo "Phase: Ready"
  exit 0
fi
exit 0
`, argsLog)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir)

	mounts := []ImageMount{
		{Source: "registry.example/tools:v1", Target: "/opt/tools"},
		{Source: "registry.example/config:v2", Target: "/etc/tool"},
	}
	require.NoError(t, Create("test-sandbox", nil, "", "", mounts...))

	args, err := os.ReadFile(argsLog)
	require.NoError(t, err)
	argv := strings.Split(strings.TrimSpace(string(args)), "\n")
	index := -1
	for i, arg := range argv {
		if arg == "--driver-config-json" {
			index = i
			break
		}
	}
	require.NotEqual(t, -1, index, "create should pass Podman driver configuration")
	require.Less(t, index+1, len(argv))
	var config podmanDriverConfig
	require.NoError(t, json.Unmarshal([]byte(argv[index+1]), &config))
	assert.Equal(t, []podmanImageMount{
		{Type: "image", Source: "registry.example/tools:v1", Target: "/opt/tools", ReadOnly: true},
		{Type: "image", Source: "registry.example/config:v2", Target: "/etc/tool", ReadOnly: true},
	}, config.Podman.Mounts)
}

// TestCreateOnce_ReadyFallsBackWhenPhaseFieldAbsent covers output-format
// drift: if OpenShell stops emitting a parseable "Phase:" field, anchoring
// alone would time out every healthy sandbox, so the historical substring
// check still applies when no phase parses at all.
func TestCreateOnce_ReadyFallsBackWhenPhaseFieldAbsent(t *testing.T) {
	dir := t.TempDir()

	script := `#!/bin/sh
if [ "$2" = "create" ]; then
  exit 0
fi
if [ "$2" = "get" ]; then
  echo "Sandbox:"
  echo "  Status: Ready"
  exit 0
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir)

	err := createOnce("test-sandbox", nil, "", "", 5*time.Second)
	assert.NoError(t, err, "a renamed phase field must not strand a Ready sandbox")
}

// TestCreateOnce_ReadyWithColorizedPhase covers the success path when the
// phase value is ANSI-wrapped; TestTerminalSandboxPhase only tabulates the
// failure phases.
func TestCreateOnce_ReadyWithColorizedPhase(t *testing.T) {
	dir := t.TempDir()

	script := "#!/bin/sh\n" +
		"if [ \"$2\" = \"create\" ]; then\n  exit 0\nfi\n" +
		"if [ \"$2\" = \"get\" ]; then\n" +
		"  printf '  \\033[2mPhase:\\033[0m \\033[32mReady\\033[0m\\n'\n" +
		"  exit 0\nfi\nexit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir)

	err := createOnce("test-sandbox", nil, "", "", 5*time.Second)
	assert.NoError(t, err, "a colorized Ready phase must be recognised")
}

// TestCreateOnce_CreateAndGetBothFail asserts the immediate-failure path
// reports the exec error, which is the only diagnostic when openshell
// produces no output at all.
func TestCreateOnce_CreateAndGetBothFail(t *testing.T) {
	dir := t.TempDir()

	script := `#!/bin/sh
exit 3
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir)

	err := createOnce("test-sandbox", nil, "", "", 5*time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sandbox create failed")
	assert.Contains(t, err.Error(), "exit status 3",
		"the exec error must survive when the command produces no output")
}

// TestCreateOnce_ReadyRequiresPhaseField guards the readiness check against
// the decoy problem the terminal-phase check already handles: "Ready" in the
// printed policy YAML must not be mistaken for a Ready sandbox.
func TestCreateOnce_ReadyRequiresPhaseField(t *testing.T) {
	dir := t.TempDir()

	// Fake openshell: get reports Provisioning, but "Ready" appears in the
	// policy YAML that `sandbox get` prints below the sandbox fields.
	script := `#!/bin/sh
if [ "$2" = "create" ]; then
  exit 0
fi
if [ "$2" = "get" ]; then
  echo "Sandbox:"
  echo "  Name: test-sandbox"
  echo "  Phase: Provisioning"
  echo "network:"
  echo "  egress:"
  echo "    allow:"
  echo "      - host: Ready.example.com"
  exit 0
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir)

	err := createOnce("test-sandbox", nil, "", "", 2*time.Second)
	require.Error(t, err, "a Provisioning sandbox must not be reported Ready")
	assert.Contains(t, err.Error(), "not ready after")
}

func TestCreateOnce_TerminalPhaseFastFailure(t *testing.T) {
	dir := t.TempDir()

	// Fake openshell: create succeeds, get always shows Error phase.
	script := `#!/bin/sh
if [ "$2" = "create" ]; then
  exit 0
fi
if [ "$2" = "get" ]; then
  echo "Phase: Error"
  exit 0
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir)

	start := time.Now()
	err := createOnce("test-sandbox", nil, "", "", 30*time.Second)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "terminal phase")
	assert.Contains(t, err.Error(), "Error")
	// Must detect the terminal phase and fail fast, well within the 30s timeout.
	assert.Less(t, elapsed, 10*time.Second,
		"terminal phase detection must short-circuit polling")
}

// TestCreateOnce_TerminalPhaseIncludesLogs asserts the terminal-phase fast
// failure carries the same supervisor/gateway/container logs as the timeout
// failure: CreateWithRetry deletes the sandbox right after, so this error is
// the only place they survive.
func TestCreateOnce_TerminalPhaseIncludesLogs(t *testing.T) {
	dir := t.TempDir()

	// Fake openshell: create succeeds, get shows Error phase, and logs
	// answers per source (`openshell logs <name> --source <src> -n 0`).
	script := `#!/bin/sh
if [ "$2" = "create" ]; then
  exit 0
fi
if [ "$2" = "get" ]; then
  echo "Phase: Error"
  exit 0
fi
if [ "$1" = "logs" ]; then
  echo "$4-log-line"
  exit 0
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir)

	err := createOnce("test-sandbox", nil, "", "", 30*time.Second)
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "terminal phase")
	assert.Contains(t, msg, "supervisor logs: supervisor-log-line")
	assert.Contains(t, msg, "gateway logs: gateway-log-line")
	assert.Contains(t, msg, "container logs: (podman not available on this host)")
}

// writeTerminalPhaseOpenshell installs a fake openshell whose sandbox always
// reaches the Error phase, and returns the path of the file recording each
// create and delete call.
func writeTerminalPhaseOpenshell(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	callsFile := filepath.Join(dir, "calls")
	script := fmt.Sprintf(`#!/bin/sh
if [ "$2" = "create" ] || [ "$2" = "delete" ]; then
  echo "$2" >> %s
  exit 0
fi
if [ "$2" = "get" ]; then
  echo "Phase: Error"
  exit 0
fi
exit 0
`, callsFile)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir)

	orig := RetrySleepFn
	RetrySleepFn = func(time.Duration) {}
	t.Cleanup(func() { RetrySleepFn = orig })
	return callsFile
}

func TestCreateWithRetry_KeepStopsWithoutDelete(t *testing.T) {
	callsFile := writeTerminalPhaseOpenshell(t)

	err := CreateWithRetry("test-sandbox", nil, "", "", 3, 5*time.Second, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sandbox kept")
	assert.Contains(t, err.Error(), "terminal phase")

	data, readErr := os.ReadFile(callsFile)
	require.NoError(t, readErr)
	assert.Equal(t, "create\n", string(data),
		"keep mode must make one attempt and never delete the failed sandbox")
}

func TestCreateWithRetry_TerminalPhaseDeletesAndRetries(t *testing.T) {
	callsFile := writeTerminalPhaseOpenshell(t)

	err := CreateWithRetry("test-sandbox", nil, "", "", 3, 5*time.Second, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sandbox creation failed after 3 attempts")

	data, readErr := os.ReadFile(callsFile)
	require.NoError(t, readErr)
	assert.Equal(t, strings.Repeat("create\ndelete\n", 3), string(data),
		"without keep each failed attempt is deleted and retried")
}

func TestCreateOnce_DiagnosticPreservation(t *testing.T) {
	dir := t.TempDir()

	// Fake openshell: create fails with diagnostics, get shows Error phase.
	script := `#!/bin/sh
if [ "$2" = "create" ]; then
  echo "image pull timeout: registry.example.com/sandbox:latest" >&2
  exit 1
fi
if [ "$2" = "get" ]; then
  echo "Phase: Error"
  exit 0
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir)

	err := createOnce("test-sandbox", nil, "", "", 5*time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "image pull timeout",
		"error must include original sandbox create output for diagnostics")
}

func TestTerminalSandboxPhase(t *testing.T) {
	// Shaped like real `openshell sandbox get` output: the phase names also
	// appear in the sandbox name, a label, and the printed policy YAML, none
	// of which may be mistaken for the sandbox phase.
	decoyOutput := `Sandbox:

  Id: abc123
  Name: fullsend-Error-Completed-repro
  Phase: Provisioning
  Resource version: 7
  Labels:
    last-outcome: Completed
  Policy source: sandbox
  Revision: 3
network:
  egress:
    allow:
      - host: Completed.example.com
      - host: Error.example.com
`

	tests := []struct {
		name   string
		output string
		want   string
	}{
		{"ready is not terminal", "  Phase: Ready", ""},
		{"provisioning is not terminal", "  Phase: Provisioning", ""},
		{"stopping is not terminal", "  Phase: Stopping", ""},
		{"error is terminal", "  Phase: Error", "Error"},
		{"completed is terminal", "  Phase: Completed", "Completed"},
		{"colorized phase label", "  \x1b[2mPhase:\x1b[0m Error", "Error"},
		{"colorized phase value", "  Phase: \x1b[31mError\x1b[0m", "Error"},
		{"colorized label and value", "  \x1b[1mPhase:\x1b[0m \x1b[31mError\x1b[0m", "Error"},
		{"no space after colon", "  Phase:Error", "Error"},
		{"empty output", "", ""},
		{"no phase field", "Sandbox:\n  Id: abc123\n", ""},
		{"phase names outside the phase field are ignored", decoyOutput, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := terminalSandboxPhase(tt.output)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParsePolicySource(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{
			name:   "sandbox policy source",
			output: "  Name: test-sandbox\n  Phase: Ready\n  Policy source: sandbox\n",
			want:   "sandbox",
		},
		{
			name:   "global policy source",
			output: "  Name: test-sandbox\n  Phase: Ready\n  Policy source: global\n",
			want:   "global",
		},
		{
			name:   "no policy source field",
			output: "  Name: test-sandbox\n  Phase: Ready\n",
			want:   "",
		},
		{
			name:   "extra whitespace",
			output: "  Policy source:   sandbox  \n",
			want:   "sandbox",
		},
		{
			name:   "policy source among many fields",
			output: "  Name: sb\n  Phase: Ready\n  Policy source: sandbox\n  Revision: 3\n",
			want:   "sandbox",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePolicySource(tt.output)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestHasPolicySection(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name:   "policy section present",
			output: "  Policy source: sandbox\n\nPolicy:\n\n  net:\n    outbound: block\n",
			want:   true,
		},
		{
			name:   "no policy section",
			output: "  Name: sb\n  Phase: Ready\n  Policy source: sandbox\n",
			want:   false,
		},
		{
			name:   "policy source not confused with policy section",
			output: "  Policy source: sandbox\n",
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasPolicySection(tt.output)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestVerifyPolicy_Match(t *testing.T) {
	output := "  Name: sb\n  Phase: Ready\n  Policy source: sandbox\n\nPolicy:\n\n  net:\n    outbound: block\n"
	err := verifyPolicy("sb", output, "/path/to/policy.yaml")
	assert.NoError(t, err)
}

func TestVerifyPolicy_GlobalWhenSandboxExpected(t *testing.T) {
	output := "  Name: sb\n  Phase: Ready\n  Policy source: global\n\nPolicy:\n\n  net:\n    outbound: allow\n"
	err := verifyPolicy("sb", output, "/path/to/policy.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "policy source")
	assert.Contains(t, err.Error(), "global")
	assert.ErrorIs(t, err, errPolicyGlobal)
}

func TestVerifyPolicy_NoPolicyRequested(t *testing.T) {
	output := "  Name: sb\n  Phase: Ready\n"
	err := verifyPolicy("sb", output, "")
	assert.NoError(t, err)
}

func TestVerifyPolicy_NoSourceReported(t *testing.T) {
	output := "  Name: sb\n  Phase: Ready\n"
	err := verifyPolicy("sb", output, "/path/to/policy.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no policy source reported")
}

func TestVerifyPolicy_ANSIEscapesStripped(t *testing.T) {
	// The openshell CLI wraps labels in ANSI colour codes; verifyPolicy
	// must strip them so the parsers can match plain-text prefixes.
	output := "  Name: sb\n  Phase: Ready\n  \x1b[1mPolicy source:\x1b[0m sandbox\n\n\x1b[1mPolicy:\x1b[0m\n\n  net:\n    outbound: block\n"
	err := verifyPolicy("sb", output, "/path/to/policy.yaml")
	assert.NoError(t, err)
}

func TestVerifyPolicy_SourceButNoPolicyContent(t *testing.T) {
	output := "  Name: sb\n  Phase: Ready\n  Policy source: sandbox\n"
	err := verifyPolicy("sb", output, "/path/to/policy.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no policy content found")
}

func TestCreateOnce_PolicyMatch(t *testing.T) {
	dir := t.TempDir()
	// The fake openshell emits ANSI-wrapped labels to exercise the
	// stripANSI path end-to-end, matching real CLI output.
	script := "#!/bin/sh\n" +
		"if [ \"$2\" = \"create\" ]; then exit 0; fi\n" +
		"if [ \"$2\" = \"get\" ]; then\n" +
		"  echo \"Sandbox:\"\n" +
		"  echo \"\"\n" +
		"  echo \"  Name: test-sandbox\"\n" +
		"  echo \"  Phase: Ready\"\n" +
		"  printf '  \\033[2mPolicy source:\\033[0m sandbox\\n'\n" +
		"  echo \"  Revision: 1\"\n" +
		"  echo \"\"\n" +
		"  printf '\\033[1;36mPolicy:\\033[0m\\n'\n" +
		"  echo \"\"\n" +
		"  echo \"  net:\"\n" +
		"  echo \"    outbound: block\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"exit 1\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir)

	err := createOnce("test-sandbox", nil, "", "/path/to/policy.yaml", 10*time.Second)
	assert.NoError(t, err)
}

func TestCreateOnce_PolicySourceGlobal(t *testing.T) {
	dir := t.TempDir()
	// ANSI-wrapped labels to exercise the stripANSI path end-to-end.
	script := "#!/bin/sh\n" +
		"if [ \"$2\" = \"create\" ]; then exit 0; fi\n" +
		"if [ \"$2\" = \"get\" ]; then\n" +
		"  echo \"Sandbox:\"\n" +
		"  echo \"\"\n" +
		"  echo \"  Name: test-sandbox\"\n" +
		"  echo \"  Phase: Ready\"\n" +
		"  printf '  \\033[2mPolicy source:\\033[0m global\\n'\n" +
		"  echo \"\"\n" +
		"  printf '\\033[1;36mPolicy:\\033[0m\\n'\n" +
		"  echo \"\"\n" +
		"  echo \"  net:\"\n" +
		"  echo \"    outbound: allow\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"exit 1\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir)

	err := createOnce("test-sandbox", nil, "", "/path/to/policy.yaml", 10*time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "policy source")
	assert.ErrorIs(t, err, errPolicyGlobal)
}

func TestCreateOnce_NoPolicyRequested(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
if [ "$2" = "create" ]; then exit 0; fi
if [ "$2" = "get" ]; then
  echo "Sandbox:"
  echo ""
  echo "  Name: test-sandbox"
  echo "  Phase: Ready"
  exit 0
fi
exit 1
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir)

	err := createOnce("test-sandbox", nil, "", "", 10*time.Second)
	assert.NoError(t, err)
}

func TestCreateOnce_PolicyNotReported(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
if [ "$2" = "create" ]; then exit 0; fi
if [ "$2" = "get" ]; then
  echo "Sandbox:"
  echo ""
  echo "  Name: test-sandbox"
  echo "  Phase: Ready"
  exit 0
fi
exit 1
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir)

	// A missing policy source is retryable — createOnce polls until
	// the timeout, then reports the last policy verification error.
	err := createOnce("test-sandbox", nil, "", "/path/to/policy.yaml", 3*time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no policy source reported")
	assert.Contains(t, err.Error(), "policy verification failed")
}

func TestCreateWithRetry_PolicyGlobalNotRetried(t *testing.T) {
	dir := t.TempDir()
	attemptsFile := filepath.Join(dir, "attempts")

	// Fake openshell: create succeeds, get always reports global policy.
	// Logs each "get" invocation to count attempts.
	script := fmt.Sprintf(`#!/bin/sh
if [ "$2" = "create" ]; then exit 0; fi
if [ "$2" = "get" ]; then
  echo "get" >> %s
  echo "Sandbox:"
  echo ""
  echo "  Name: test-sandbox"
  echo "  Phase: Ready"
  echo "  Policy source: global"
  echo ""
  echo "Policy:"
  echo ""
  echo "  net:"
  echo "    outbound: allow"
  exit 0
fi
if [ "$2" = "delete" ]; then exit 0; fi
exit 0
`, attemptsFile)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir)

	orig := RetrySleepFn
	RetrySleepFn = func(d time.Duration) {}
	t.Cleanup(func() { RetrySleepFn = orig })

	err := CreateWithRetry("test-sandbox", nil, "", "/path/to/policy.yaml", 3, 5*time.Second, false)
	require.Error(t, err)
	assert.ErrorIs(t, err, errPolicyGlobal)

	// Should have made only 1 creation attempt — the global source error
	// is non-retryable, so CreateWithRetry must not delete and recreate.
	data, readErr := os.ReadFile(attemptsFile)
	require.NoError(t, readErr)
	gets := strings.Count(string(data), "get")
	assert.Equal(t, 1, gets, "global policy error must not trigger retries")
}

func TestCreateWithRetry_UnsupportedImageMountConfigurationNotRetried(t *testing.T) {
	dir := t.TempDir()
	createAttempts := filepath.Join(dir, "create-attempts")
	script := fmt.Sprintf(`#!/bin/sh
if [ "$2" = "create" ]; then
  echo create >> %s
  echo "driver config disabled: set allow_driver_config = true" >&2
  exit 1
fi
if [ "$2" = "get" ]; then exit 1; fi
exit 0
`, createAttempts)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir)

	orig := RetrySleepFn
	RetrySleepFn = func(d time.Duration) { t.Fatalf("unexpected retry sleep: %s", d) }
	t.Cleanup(func() { RetrySleepFn = orig })

	err := CreateWithRetry("test-sandbox", nil, "", "", 3, 5*time.Second, false,
		ImageMount{Source: "example/tools:latest", Target: "/opt/tools"})
	require.Error(t, err)
	assert.ErrorIs(t, err, errUnsupportedImageMountConfiguration)
	assert.Contains(t, err.Error(), "allow-image-mounts: true")
	assert.Contains(t, err.Error(), "effective settings")

	data, readErr := os.ReadFile(createAttempts)
	require.NoError(t, readErr)
	assert.Equal(t, "create\n", string(data), "unsupported gateway config must not be retried")
}

func TestCreateWithRetry_UnsupportedImageMountConfigurationKeepsSandbox(t *testing.T) {
	dir := t.TempDir()
	commandsFile := filepath.Join(dir, "commands")
	script := fmt.Sprintf(`#!/bin/sh
echo "$2" >> %s
if [ "$2" = "create" ]; then
  echo "allow_driver_config is disabled" >&2
  exit 1
fi
if [ "$2" = "get" ]; then echo "Phase: Error"; exit 0; fi
exit 0
`, commandsFile)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir)

	err := CreateWithRetry("test-sandbox", nil, "", "", 3, 5*time.Second, true,
		ImageMount{Source: "example/tools:latest", Target: "/opt/tools"})
	require.Error(t, err)
	assert.ErrorIs(t, err, errUnsupportedImageMountConfiguration)
	assert.Contains(t, err.Error(), "sandbox kept, not retried")
	assert.Contains(t, err.Error(), "allow-image-mounts: true")

	data, readErr := os.ReadFile(commandsFile)
	require.NoError(t, readErr)
	assert.Equal(t, "create\nget\n", string(data), "--keep-sandbox must preserve the failed sandbox")
}

func TestUnsupportedImageMountConfiguration(t *testing.T) {
	for _, tc := range []struct {
		message string
		want    bool
	}{
		{message: "allow_driver_config is disabled", want: true},
		{message: "resource admission rejected image mount", want: true},
		{message: "image mounts are not supported by this backend", want: true},
		{message: "Podman driver does not support image mounts", want: true},
		{message: "gateway temporarily unavailable", want: false},
	} {
		assert.Equal(t, tc.want, unsupportedImageMountConfiguration(tc.message), tc.message)
	}
}

func TestVerifyPolicy_OutputIncludedInErrors(t *testing.T) {
	output := "  Name: sb\n  Phase: Ready\n"
	err := verifyPolicy("sb", output, "/path/to/policy.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "output:")
	assert.Contains(t, err.Error(), "Name: sb")
}

func TestTruncatePolicyOutput(t *testing.T) {
	short := "hello"
	assert.Equal(t, short, truncatePolicyOutput(short, 512))

	long := strings.Repeat("x", 600)
	got := truncatePolicyOutput(long, 512)
	assert.Len(t, got, 515) // 512 + len("...")
	assert.True(t, strings.HasSuffix(got, "..."))
}

func TestEffectiveReadyTimeout_CappedAtMax(t *testing.T) {
	got := effectiveReadyTimeout(999 * time.Second)
	assert.Equal(t, maxReadyTimeout, got)
}

func TestEffectiveReadyTimeout_EnvVarCappedAtMax(t *testing.T) {
	t.Setenv("FULLSEND_SANDBOX_READY_TIMEOUT", "1h")
	got := effectiveReadyTimeout(0)
	assert.Equal(t, maxReadyTimeout, got)
}

func TestUploadFile_OpenshellNotInPath(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "test.txt")
	require.NoError(t, os.WriteFile(f, []byte("hello"), 0o644))

	t.Setenv("PATH", "")

	err := UploadFile("test-sandbox", f, "/sandbox/workspace/test.txt")
	assert.Error(t, err)
}

func TestUploadFile_ExactDestination(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%t", existing), func(t *testing.T) {
			dir := t.TempDir()
			local := filepath.Join(dir, "source ' file.json")
			require.NoError(t, os.WriteFile(local, []byte("replacement"), 0o644))
			remote := filepath.Join(dir, "repo", "main ' file.go")
			require.NoError(t, os.MkdirAll(filepath.Dir(remote), 0o755))
			neighbor := filepath.Join(filepath.Dir(remote), "neighbor.go")
			require.NoError(t, os.WriteFile(neighbor, []byte("preserve"), 0o644))
			if existing {
				require.NoError(t, os.WriteFile(remote, []byte("original"), 0o644))
			}
			installFileUploadFake(t)
			require.NoError(t, UploadFile("test", local, remote))
			got, err := os.ReadFile(remote)
			require.NoError(t, err)
			assert.Equal(t, "replacement", string(got))
			got, err = os.ReadFile(neighbor)
			require.NoError(t, err)
			assert.Equal(t, "preserve", string(got))
		})
	}
}

// Execute the remote commands locally and emulate OpenShell's directory
// destination semantics, including mkdir failure for an existing file. The
// sandbox runs GNU coreutils, but BSD mv on macOS lacks -T, so the fake
// emulates "mv -fT -- SRC DST" with portable shell instead of the host mv.
func installFileUploadFake(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/bin/sh
if [ "$2" = upload ]; then
  mkdir -p -- "$6" && cp -- "$5" "$6/"
  exit $?
fi
if [ "$2" = exec ]; then
  for arg in "$@"; do command="$arg"; done
  prelude='mv() {
  if [ "$1" = -fT ] && [ "$2" = -- ] && [ "$#" -eq 4 ]; then
    if [ -d "$4" ]; then
      echo "mv: cannot overwrite directory $4" >&2
      return 1
    fi
    command mv -f -- "$3" "$4"
  else
    command mv "$@"
  fi
}'
  sh -c "$prelude
$command"
  exit $?
fi
exit 1
`
	require.NoError(t, os.WriteFile(filepath.Join(bin, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestUploadFile_RejectsDirectoryDestination(t *testing.T) {
	installFileUploadFake(t)
	local := filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.WriteFile(local, []byte("content"), 0o644))
	destination := t.TempDir()
	assert.ErrorContains(t, UploadFile("test", local, destination), "installing uploaded file")
}

func TestUploadFile_MissingSource(t *testing.T) {
	installFileUploadFake(t)
	assert.Error(t, UploadFile("test", filepath.Join(t.TempDir(), "missing"), filepath.Join(t.TempDir(), "dest")))
}

func TestUploadDir_OpenshellNotInPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", "")

	err := UploadDir("test-sandbox", dir, "/sandbox/workspace/repo")
	assert.Error(t, err)
}

func TestUploadDir_TarIncludesCopyfileDisable(t *testing.T) {
	// Create a temp dir with a file, run UploadDir (which will fail because
	// openshell is unavailable), but first intercept the tar step by providing
	// a tar wrapper that records its environment.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), []byte("content"), 0o644))

	// Create a fake tar that writes its COPYFILE_DISABLE env var to a file.
	binDir := t.TempDir()
	envFile := filepath.Join(binDir, "copyfile_env")
	fakeTar := filepath.Join(binDir, "tar")
	script := "#!/bin/sh\necho \"$COPYFILE_DISABLE\" > " + envFile + "\n"
	require.NoError(t, os.WriteFile(fakeTar, []byte(script), 0o755))

	t.Setenv("PATH", binDir)
	// Will fail at the Upload step (no openshell), but tar runs first.
	_ = UploadDir("test-sandbox", dir, "/tmp/workspace/repo")

	data, err := os.ReadFile(envFile)
	require.NoError(t, err, "fake tar should have written env file")
	assert.Equal(t, "1", strings.TrimSpace(string(data)), "COPYFILE_DISABLE should be set to 1")
}

func TestUploadDir_ExcludesPatternsFromTarball(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("keep"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "output", "agent-x-1-1"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "output", "agent-x-1-1", "run-telemetry.jsonl"), []byte("telem\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub", "output"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sub", "output", "keep.txt"), []byte("nested\n"), 0o644))

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "openshell.log")
	sentinelPath := filepath.Join(binDir, "uploaded.tar.gz")
	fakeOpenshell(t, binDir, logPath, sentinelPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	require.NoError(t, UploadDir("test-sandbox", dir, "/sandbox/workspace/repo", "output/"))

	extracted := extractTar(t, sentinelPath)
	_, err := os.Stat(filepath.Join(extracted, "keep.txt"))
	require.NoError(t, err, "keep.txt should be in the tarball")
	_, err = os.Stat(filepath.Join(extracted, "output"))
	assert.True(t, os.IsNotExist(err), "root output/ must be excluded from the sandbox tarball")
	got, err := os.ReadFile(filepath.Join(extracted, "sub", "output", "keep.txt"))
	require.NoError(t, err, "nested sub/output/ must survive (bsdtar --exclude would drop it)")
	assert.Equal(t, "nested\n", string(got))
}

func TestUploadDir_PreservesInTreeSymlinks(t *testing.T) {
	// Only in-tree relative links: escaping/absolute links are deliberately
	// not asserted here, since #5253 may sanitize them on upload.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "real-dir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "real-dir", "f.txt"), []byte("x"), 0o644))
	require.NoError(t, os.Symlink("real-dir", filepath.Join(dir, "link-dir")))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	require.NoError(t, os.Symlink("../real-dir/f.txt", filepath.Join(dir, "sub", "link-file")))

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "openshell.log")
	sentinelPath := filepath.Join(binDir, "uploaded.tar.gz")
	fakeOpenshell(t, binDir, logPath, sentinelPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	require.NoError(t, UploadDir("test-sandbox", dir, "/sandbox/workspace/repo"))

	extracted := extractTar(t, sentinelPath)
	for link, want := range map[string]string{
		"link-dir":      "real-dir",          // top-level member passed to tar by tarRootMembers
		"sub/link-file": "../real-dir/f.txt", // nested entry reached by tar's own recursion
	} {
		t.Run(link, func(t *testing.T) {
			p := filepath.Join(extracted, filepath.FromSlash(link))
			fi, err := os.Lstat(p)
			require.NoError(t, err, "%s should be in the tarball", link)
			assert.NotZero(t, fi.Mode()&os.ModeSymlink, "%s must stay a symlink, not be dereferenced", link)
			got, err := os.Readlink(p)
			require.NoError(t, err)
			assert.Equal(t, want, got, "%s symlink target", link)
		})
	}
}

func TestTarRootMembers_RejectsNestedExclude(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("x"), 0o644))
	_, err := tarRootMembers(dir, "build/output")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "top-level")
}

// fakeOpenshell writes a script that logs every invocation's full argv to
// logPath, and — when invoked as
// "sandbox upload --no-git-ignore <name> <local> <remote>" —
// copies <local> to sentinelPath so the test can inspect exactly what bytes
// would have been shipped to the sandbox. It always exits 0. The real system
// tar (found via the pre-existing PATH) is used for archive creation, so
// these tests prove actual content survives the transfer, not just that some
// "tar -czf" command was invoked with the right flag.
func fakeOpenshell(t *testing.T, binDir, logPath, sentinelPath string) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> " + shellQuote(logPath) + "\n" +
		"if [ \"$2\" = \"upload\" ]; then cp \"$5\" " + shellQuote(sentinelPath) + "; fi\n" +
		"exit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
}

// extractTar extracts a tar.gz archive into a fresh temp dir using the real
// system tar and returns the directory.
func extractTar(t *testing.T, archivePath string) string {
	t.Helper()
	dest := t.TempDir()
	out, err := exec.Command("tar", "-xzf", archivePath, "-C", dest).CombinedOutput()
	require.NoError(t, err, "extracting archive: %s", string(out))
	return dest
}

func TestUpload_SymlinkToDirectory_RealContentSurvivesTransfer(t *testing.T) {
	// Reproduce the #5247 cache layout: a "tree" directory holding the real
	// content, and a named symlink (as created by fetch.CacheNamedSymlink)
	// pointing at it.
	cacheDir := t.TempDir()
	treeDir := filepath.Join(cacheDir, "tree")
	require.NoError(t, os.MkdirAll(treeDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(treeDir, "SKILL.md"), []byte("real content"), 0o644))
	require.NoError(t, os.Symlink("tree", filepath.Join(cacheDir, "pr-review")))

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "openshell.log")
	sentinelPath := filepath.Join(binDir, "uploaded.tar.gz")
	fakeOpenshell(t, binDir, logPath, sentinelPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := Upload("test-sandbox", filepath.Join(cacheDir, "pr-review"), "/sandbox/claude-config/skills/")
	require.NoError(t, err)

	// Prove the symlink's real target content — not a dangling symlink entry
	// — actually made it into the archive that was "uploaded".
	extracted := extractTar(t, sentinelPath)
	got, err := os.ReadFile(filepath.Join(extracted, "SKILL.md"))
	require.NoError(t, err, "SKILL.md should be present in the uploaded archive")
	assert.Equal(t, "real content", string(got))

	// Prove the destination the sandbox extracts to is the skill's real
	// name, not the cache-internal "tree" directory name — the regression
	// #5247 describes.
	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(log), "/sandbox/claude-config/skills/pr-review",
		"extraction destination should be named after the skill, not \"tree\"")
}

func TestUpload_RegularDirectory_WrapsUnderBasenameLikeBefore(t *testing.T) {
	skillDir := filepath.Join(t.TempDir(), "my-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("plain dir content"), 0o644))

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "openshell.log")
	sentinelPath := filepath.Join(binDir, "uploaded.tar.gz")
	fakeOpenshell(t, binDir, logPath, sentinelPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := Upload("test-sandbox", skillDir, "/sandbox/claude-config/skills/")
	require.NoError(t, err)

	extracted := extractTar(t, sentinelPath)
	got, err := os.ReadFile(filepath.Join(extracted, "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, "plain dir content", string(got))

	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(log), "/sandbox/claude-config/skills/my-skill")
}

func TestUpload_DanglingSymlink_FailsInsteadOfSilentlySucceeding(t *testing.T) {
	cacheDir := t.TempDir()
	require.NoError(t, os.Symlink("missing-target", filepath.Join(cacheDir, "broken-skill")))

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "openshell.log")
	sentinelPath := filepath.Join(binDir, "uploaded.tar.gz")
	fakeOpenshell(t, binDir, logPath, sentinelPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := Upload("test-sandbox", filepath.Join(cacheDir, "broken-skill"), "/sandbox/claude-config/skills/")
	// A dangling symlink must surface as an error now, not a silent
	// no-content "success" the way the original bug did.
	assert.Error(t, err)
}

func TestUpload_SymlinkToRegularFile_UploadsFileDirectly(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.txt")
	require.NoError(t, os.WriteFile(target, []byte("file content"), 0o644))
	link := filepath.Join(dir, "link.txt")
	require.NoError(t, os.Symlink(target, link))

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "openshell.log")
	sentinelPath := filepath.Join(binDir, "uploaded.tar.gz")
	fakeOpenshell(t, binDir, logPath, sentinelPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := Upload("test-sandbox", link, "/sandbox/workspace/link.txt")
	require.NoError(t, err)

	// A symlink to a regular file must upload the file's content directly —
	// not be misrouted into the tar/directory path, which would fail
	// because "tar -C <file>" can't chdir into a non-directory.
	got, readErr := os.ReadFile(sentinelPath)
	require.NoError(t, readErr, "sentinel should be the uploaded file itself, not a tar archive")
	assert.Equal(t, "file content", string(got))

	// Upload must pass openshell the symlink's resolved real path, not the
	// symlink itself, so correctness doesn't depend on whether openshell's
	// own "sandbox upload" dereferences a symlink argument.
	wantPath, err := filepath.EvalSymlinks(target)
	require.NoError(t, err)
	log, readErr := os.ReadFile(logPath)
	require.NoError(t, readErr)
	assert.Contains(t, string(log), "upload --no-git-ignore test-sandbox "+wantPath+" /sandbox/workspace/link.txt")
	assert.NotContains(t, string(log), "test-sandbox "+link+" ",
		"the symlink path itself should never reach openshell")
}

func TestUpload_ExactDestination_NoTrailingSlash(t *testing.T) {
	skillDir := filepath.Join(t.TempDir(), "checked-out-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("exact dest content"), 0o644))

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "openshell.log")
	sentinelPath := filepath.Join(binDir, "uploaded.tar.gz")
	fakeOpenshell(t, binDir, logPath, sentinelPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// No trailing slash: remotePath is the exact destination directory,
	// contents land there directly (not wrapped under skillDir's basename).
	err := Upload("test-sandbox", skillDir, "/sandbox/claude-config/skills/exact-name")
	require.NoError(t, err)

	extracted := extractTar(t, sentinelPath)
	got, readErr := os.ReadFile(filepath.Join(extracted, "SKILL.md"))
	require.NoError(t, readErr)
	assert.Equal(t, "exact dest content", string(got))

	log, readErr := os.ReadFile(logPath)
	require.NoError(t, readErr)
	assert.Contains(t, string(log), "/sandbox/claude-config/skills/exact-name")
	assert.NotContains(t, string(log), "/sandbox/claude-config/skills/exact-name/checked-out-skill")
}

// TestUpload_MaliciousBasename_ExtractCommandNotInjected proves the
// shell-quoting fix by actually executing UploadDir's extract command
// through a real "sh -c", the same way the sandbox would — not just
// checking that a mocked command logged the right-looking string. A fake
// "openshell" that only records argv (like fakeOpenshell above) would pass
// this test even with the quoting removed, since it never runs anything;
// this one genuinely runs the command so an injection would genuinely fire.
func TestUpload_MaliciousBasename_ExtractCommandNotInjected(t *testing.T) {
	cacheDir := t.TempDir()
	treeDir := filepath.Join(cacheDir, "tree")
	require.NoError(t, os.MkdirAll(treeDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(treeDir, "SKILL.md"), []byte("content"), 0o644))

	pwnedMarker := filepath.Join(cacheDir, "PWNED")
	t.Setenv("PWNED_MARKER", pwnedMarker)
	// A skill/plugin basename derived from a fetched name could contain
	// shell metacharacters; the extract command must treat it as inert data.
	// The payload references $PWNED_MARKER (expanded only if injection
	// actually fires) rather than embedding a literal "/"-containing path,
	// since the basename itself must remain a valid single filename.
	evilName := "evil'; touch \"$PWNED_MARKER\" #"
	require.NoError(t, os.Symlink("tree", filepath.Join(cacheDir, evilName)))

	destParent := t.TempDir()

	binDir := t.TempDir()
	// This fake genuinely executes: "upload" copies the local file to the
	// literal remote path argument (both are real local paths in this
	// test), and "exec" runs the trailing "sh -c <command>" argument for
	// real via the host's own sh. This reproduces, on the test machine,
	// exactly what UploadDir's extractCmd would do inside the sandbox.
	script := "#!/bin/sh\n" +
		"if [ \"$2\" = \"upload\" ]; then cp \"$5\" \"$6\"; exit 0; fi\n" +
		"if [ \"$2\" = \"exec\" ]; then\n" +
		"  for a in \"$@\"; do last=\"$a\"; done\n" +
		"  sh -c \"$last\"\n" +
		"  exit $?\n" +
		"fi\n" +
		"exit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := Upload("test-sandbox", filepath.Join(cacheDir, evilName), destParent+"/")
	require.NoError(t, err)

	// The injected "touch" must never have run.
	_, statErr := os.Stat(pwnedMarker)
	assert.True(t, os.IsNotExist(statErr), "malicious basename must not execute injected shell commands")

	// The legitimate extract must still have succeeded, landing real
	// content at the (oddly-named but correctly quoted) destination.
	got, readErr := os.ReadFile(filepath.Join(destParent, evilName, "SKILL.md"))
	require.NoError(t, readErr, "extraction should still succeed at the exact literal destination")
	assert.Equal(t, "content", string(got))
}

func TestUpload_RegularFile_SkipsTarPath(t *testing.T) {
	f := filepath.Join(t.TempDir(), "settings.json")
	require.NoError(t, os.WriteFile(f, []byte("{}"), 0o644))

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "openshell.log")
	sentinelPath := filepath.Join(binDir, "uploaded.tar.gz")
	fakeOpenshell(t, binDir, logPath, sentinelPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := Upload("test-sandbox", f, "/sandbox/claude-config/settings.json")
	require.NoError(t, err)

	// A regular file must go straight through "sandbox upload" with no tar
	// archive step — the sentinel (only written on "sandbox upload") should
	// be the file itself, not a .tar.gz.
	got, err := os.ReadFile(sentinelPath)
	require.NoError(t, err)
	assert.Equal(t, "{}", string(got))

	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(log), "upload --no-git-ignore test-sandbox "+f+" /sandbox/claude-config/settings.json")
}

// TestUpload_PassesNoGitIgnore: every "sandbox upload" carries --no-git-ignore
// so OpenShell >= 0.1.3 never drops a file fullsend already chose because the
// host checkout's .gitignore matches it (NVIDIA/OpenShell#3957, #8484).
func TestUpload_PassesNoGitIgnore(t *testing.T) {
	// A git-ignored path inside a checkout, like the cached output schema.
	f := filepath.Join(t.TempDir(), ".fullsend-cache", "content.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(f), 0o755))
	require.NoError(t, os.WriteFile(f, []byte("{}"), 0o644))
	dir := filepath.Join(t.TempDir(), "skill")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("x"), 0o644))

	binDir := t.TempDir()
	argvLog := filepath.Join(binDir, "argv.log")
	script := "#!/bin/sh\n" +
		"if [ \"$2\" = \"upload\" ]; then printf '%s\\n' \"$*\" >> " + shellQuote(argvLog) + "; fi\n" +
		"exit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	require.NoError(t, Upload("test-sandbox", f, "/dest/f"))
	require.NoError(t, UploadDir("test-sandbox", dir, "/dest/skill"))

	data, err := os.ReadFile(argvLog)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 2)
	assert.Equal(t, "sandbox upload --no-git-ignore test-sandbox "+f+" /dest/f", lines[0])
	fields := strings.Fields(lines[1])
	require.Len(t, fields, 6, "UploadDir tarball upload: %s", lines[1])
	assert.Equal(t, []string{"sandbox", "upload", "--no-git-ignore", "test-sandbox"}, fields[:4])
	assert.True(t, strings.HasSuffix(fields[4], ".tar.gz"), "UploadDir uploads its tarball: %s", fields[4])
}

func TestUpload_TrailingDotConvention_CopiesContentsWithoutWrapping(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "input.json"), []byte("agent input"), 0o644))

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "openshell.log")
	sentinelPath := filepath.Join(binDir, "uploaded.tar.gz")
	fakeOpenshell(t, binDir, logPath, sentinelPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Mirrors internal/cli/run.go's h.AgentInput+"/." -> remoteInput+"/" call.
	err := Upload("test-sandbox", dir+"/.", "/sandbox/workspace/input/")
	require.NoError(t, err)

	extracted := extractTar(t, sentinelPath)
	got, err := os.ReadFile(filepath.Join(extracted, "input.json"))
	require.NoError(t, err)
	assert.Equal(t, "agent input", string(got))

	// Contents land directly at the destination, not wrapped under a
	// subdirectory named "." or after dir's own basename.
	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(log), "mkdir -p '/sandbox/workspace/input' &&")
	assert.NotContains(t, string(log), "/sandbox/workspace/input/.")
}

func TestSanitizeDownload_RemovesAppleDoubleInGitDir(t *testing.T) {
	dir := t.TempDir()

	// Create .git/objects/pack/ with a normal file and an AppleDouble file.
	packDir := filepath.Join(dir, ".git", "objects", "pack")
	require.NoError(t, os.MkdirAll(packDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(packDir, "pack-abc.idx"), []byte("idx"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(packDir, "._pack-abc.idx"), []byte("apple"), 0o644))

	// Create an AppleDouble file outside .git/ — should NOT be removed.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "._regular.txt"), []byte("ok"), 0o644))

	err := sanitizeDownload(dir)
	require.NoError(t, err)

	// Normal pack file survives.
	_, err = os.Stat(filepath.Join(packDir, "pack-abc.idx"))
	assert.NoError(t, err, "normal pack file should survive")

	// AppleDouble file inside .git/ should be removed.
	_, err = os.Stat(filepath.Join(packDir, "._pack-abc.idx"))
	assert.True(t, os.IsNotExist(err), "._* file inside .git/ should be removed")

	// AppleDouble file outside .git/ should survive.
	_, err = os.Stat(filepath.Join(dir, "._regular.txt"))
	assert.NoError(t, err, "._* file outside .git/ should survive")
}

func TestInGitDir(t *testing.T) {
	root := "/repo"
	tests := []struct {
		path    string
		wantRel string
		want    bool
	}{
		{"/repo/.git/objects/pack/file.idx", "objects/pack/file.idx", true},
		{"/repo/.git/config", "config", true},
		{"/repo/sub/.git/hooks/pre-commit", "hooks/pre-commit", true},
		{"/repo/.git", "", true},
		{"/repo", "", false},
		{"/repo/src/main.go", "", false},
		{"/repo/._file.txt", "", false},
		{"/repo/.GIT/refs/replace", "refs/replace", true},
		{"/repo/.git/modules/dep/.git/refs/x", "modules/dep/.git/refs/x", true},
	}
	for _, tt := range tests {
		got, gotRel := inGitDir(tt.path, root)
		assert.Equal(t, tt.want, got, "inGitDir(%q, %q) matched", tt.path, root)
		assert.Equal(t, tt.wantRel, filepath.ToSlash(gotRel), "inGitDir(%q, %q) rel", tt.path, root)
	}
}

func TestBuildProviderUpdateArgs(t *testing.T) {
	t.Setenv("MY_TOKEN", "tok123")

	credentials := map[string]string{"TOKEN": "${MY_TOKEN}"}
	config := map[string]string{"BASE_URL": "https://example.com"}

	args := buildProviderUpdateArgs("myprovider", credentials, config, false)

	assert.Equal(t, "provider", args[0])
	assert.Equal(t, "update", args[1])
	assert.Equal(t, "myprovider", args[2])
	assert.Contains(t, args, "--credential")
	assert.Contains(t, args, "TOKEN")
	assert.Contains(t, args, "--config")
	assert.Contains(t, args, "BASE_URL=https://example.com")

	// Secret value must not appear in args.
	for _, arg := range args {
		assert.NotContains(t, arg, "tok123", "secret must not appear in update args")
	}
}

func TestImportProfile_OpenshellNotInPath(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "profile.yaml")
	require.NoError(t, os.WriteFile(profilePath, []byte("id: test-profile"), 0o644))
	t.Setenv("PATH", t.TempDir())

	cachePath := profileFileCachePath("test-profile")
	t.Cleanup(func() { os.Remove(cachePath) })

	err := ImportProfile(context.Background(), "test-profile", profilePath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "openshell")
}

func TestImportProfile_Success(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "my-profile.yaml")
	require.NoError(t, os.WriteFile(profilePath, []byte("id: my-profile"), 0o644))

	script := `#!/bin/sh
exit 0
`
	fakePath := filepath.Join(dir, "openshell")
	require.NoError(t, os.WriteFile(fakePath, []byte(script), 0o755))
	t.Setenv("PATH", dir)

	cachePath := profileFileCachePath("my-profile")
	t.Cleanup(func() { os.Remove(cachePath) })

	err := ImportProfile(context.Background(), "my-profile", profilePath)
	assert.NoError(t, err)
}

func TestImportProfile_UsesFileFlag(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "my-profile.yaml")
	require.NoError(t, os.WriteFile(profilePath, []byte("id: my-profile"), 0o644))
	argsFile := filepath.Join(dir, "args.log")

	// Fake openshell that logs args on "import" invocations and exits 0.
	script := `#!/bin/sh
if [ "$3" = "import" ]; then
  echo "$@" >> ` + argsFile + `
fi
exit 0
`
	fakePath := filepath.Join(dir, "openshell")
	require.NoError(t, os.WriteFile(fakePath, []byte(script), 0o755))
	t.Setenv("PATH", dir)

	cachePath := profileFileCachePath("my-profile")
	t.Cleanup(func() { os.Remove(cachePath) })

	err := ImportProfile(context.Background(), "my-profile", profilePath)
	require.NoError(t, err)

	logged, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	assert.Contains(t, string(logged), "--file "+profilePath,
		"ImportProfile must pass --file flag to openshell provider profile import")
}

// alreadyExistsThenExportStub builds an openshell stub whose profile import
// fails with "already exists" and whose profile export prints exportYAML.
func alreadyExistsThenExportStub(t *testing.T, dir, exportYAML string) string {
	t.Helper()
	script := `#!/bin/sh
case "$3" in
  import)
    echo "profile already exists" >&2
    exit 1
    ;;
  export)
    printf '%s\n' ` + shellQuote(exportYAML) + `
    exit 0
    ;;
esac
exit 0
`
	fakePath := filepath.Join(dir, "openshell")
	require.NoError(t, os.WriteFile(fakePath, []byte(script), 0o755))
	return fakePath
}

func TestImportProfile_AlreadyExists(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "my-profile.yaml")
	require.NoError(t, os.WriteFile(profilePath, []byte("id: my-profile"), 0o644))

	// Gateway content matches the local file, so "already exists" is a no-op.
	alreadyExistsThenExportStub(t, dir, "id: my-profile")
	t.Setenv("PATH", dir)

	cachePath := profileFileCachePath("my-profile")
	t.Cleanup(func() { os.Remove(cachePath) })

	err := ImportProfile(context.Background(), "my-profile", profilePath)
	assert.NoError(t, err, "already-exists with matching gateway content should not be an error")
}

// TestImportProfile_AlreadyExists_GatewayMetadataIgnored verifies that a
// gateway-only metadata field (resource_version) is not a mismatch.
func TestImportProfile_AlreadyExists_GatewayMetadataIgnored(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "my-profile.yaml")
	require.NoError(t, os.WriteFile(profilePath, []byte("id: my-profile\n"), 0o644))

	alreadyExistsThenExportStub(t, dir, "id: my-profile\nresource_version: 7\n")
	t.Setenv("PATH", dir)

	cachePath := profileFileCachePath("my-profile")
	t.Cleanup(func() { os.Remove(cachePath) })

	err := ImportProfile(context.Background(), "my-profile", profilePath)
	assert.NoError(t, err, "a gateway-only metadata field must not be treated as a content mismatch")
}

// TestImportProfile_AlreadyExists_GatewayDefaultsIgnored covers #8211: the
// gateway's export of an unchanged profile adds defaulted fields the local
// file never declares, which must not be treated as a content mismatch.
func TestImportProfile_AlreadyExists_GatewayDefaultsIgnored(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "my-profile.yaml")
	local := "id: my-profile\ncategory: source_control\ncredentials:\n  - name: api_token\n    env_vars: [GH_TOKEN]\n    required: true\n"
	require.NoError(t, os.WriteFile(profilePath, []byte(local), 0o644))

	exported := "id: my-profile\nresource_version: 3\ndescription: ''\ncategory: source_control\n" +
		"credentials:\n- name: api_token\n  description: ''\n  env_vars:\n  - GH_TOKEN\n  required: true\n" +
		"  auth_style: ''\n  header_name: ''\n  query_param: ''\n" +
		"endpoints: []\nbinaries: []\ninference_capable: false\nsource: user\nscope: workspace\n"
	alreadyExistsThenExportStub(t, dir, exported)
	t.Setenv("PATH", dir)

	cachePath := profileFileCachePath("my-profile")
	t.Cleanup(func() { os.Remove(cachePath) })

	err := ImportProfile(context.Background(), "my-profile", profilePath)
	assert.NoError(t, err, "gateway-defaulted fields on an unchanged profile must not be a content mismatch")
}

func TestProfileContentEqual(t *testing.T) {
	const local = `id: my-profile
category: source_control
credentials:
  - name: api_token
    env_vars: [GH_TOKEN]
    required: true
endpoints:
  - host: api.github.com
    port: 443
binaries:
  - "**/gh"
`
	const exported = `id: my-profile
resource_version: 7
description: ''
category: source_control
credentials:
- name: api_token
  description: ''
  env_vars:
  - GH_TOKEN
  required: true
  auth_style: ''
  header_name: ''
  query_param: ''
endpoints:
- host: api.github.com
  port: 443
binaries:
- '**/gh'
inference_capable: false
source: user
scope: workspace
`
	tests := []struct {
		name     string
		local    string
		exported string
		want     bool
	}{
		{
			name:     "export adds only metadata and defaults",
			local:    local,
			exported: exported,
			want:     true,
		},
		{
			name:     "local declares defaults explicitly",
			local:    local + "inference_capable: false\ndescription: ''\n",
			exported: exported,
			want:     true,
		},
		{
			name:     "local declares a default the export omits",
			local:    "id: p\nendpoints: []\n",
			exported: "id: p\n",
			want:     true,
		},
		{
			name:     "local declares null and empty map the export omits",
			local:    "id: p\ndescription:\nannotations: {}\n",
			exported: "id: p\n",
			want:     true,
		},
		{
			name:     "no credentials locally, export has empty list",
			local:    "id: p\ncategory: inference\n",
			exported: "id: p\ncategory: inference\ncredentials: []\n",
			want:     true,
		},
		{
			name:     "category omitted locally defaults to other",
			local:    "id: p\n",
			exported: "id: p\ncategory: other\n",
			want:     true,
		},
		{
			name:     "credential env var differs",
			local:    local,
			exported: strings.Replace(exported, "  - GH_TOKEN", "  - OTHER_TOKEN", 1),
			want:     false,
		},
		{
			name:     "endpoint host differs",
			local:    local,
			exported: strings.Replace(exported, "host: api.github.com", "host: evil.example.com", 1),
			want:     false,
		},
		{
			name:     "credential count differs",
			local:    local,
			exported: strings.Replace(exported, "endpoints:\n", "- name: extra\nendpoints:\n", 1),
			want:     false,
		},
		{
			name:     "local drops credentials the export still has",
			local:    "id: p\n",
			exported: "id: p\ncredentials:\n- name: api_token\n  env_vars: [GH_TOKEN]\n",
			want:     false,
		},
		{
			name:     "local drops endpoints the export still has",
			local:    "id: p\n",
			exported: "id: p\nendpoints:\n- host: internal.example.com\n  port: 443\n",
			want:     false,
		},
		{
			name:     "export has non-default inference_capable",
			local:    local,
			exported: strings.Replace(exported, "inference_capable: false", "inference_capable: true", 1),
			want:     false,
		},
		{
			name:     "export has non-default credential query_param",
			local:    local,
			exported: strings.Replace(exported, "query_param: ''", "query_param: token", 1),
			want:     false,
		},
		{
			name:     "export has non-default credential auth_style",
			local:    local,
			exported: strings.Replace(exported, "auth_style: ''", "auth_style: bearer", 1),
			want:     false,
		},
		{
			name:     "export has non-default credential header_name",
			local:    local,
			exported: strings.Replace(exported, "header_name: ''", "header_name: X-Key", 1),
			want:     false,
		},
		{
			name:     "export has non-default endpoint scalar",
			local:    local,
			exported: strings.Replace(exported, "  port: 443\nbinaries", "  port: 443\n  allow_uninspected_credentials: true\nbinaries", 1),
			want:     false,
		},
		{
			name:     "export has non-default source",
			local:    local,
			exported: strings.Replace(exported, "source: user", "source: builtin", 1),
			want:     false,
		},
		{
			name:     "export has non-default scope",
			local:    local,
			exported: strings.Replace(exported, "scope: workspace", "scope: global", 1),
			want:     false,
		},
		{
			name:     "category dropped locally while export has non-default category",
			local:    "id: p\n",
			exported: "id: p\ncategory: inference\n",
			want:     false,
		},
		{
			name:     "unknown non-zero scalar only the export has",
			local:    local,
			exported: strings.Replace(exported, "category: source_control", "category: source_control\nfuture_field: x", 1),
			want:     false,
		},
		{
			name:     "new gateway default the local file never heard of",
			local:    local,
			exported: exported + "new_openshell_default: ''\nnew_list_default: []\nnew_map_default: {}\n",
			want:     true,
		},
		{
			name:     "new credential field only the export has",
			local:    local,
			exported: strings.Replace(exported, "query_param: ''", "query_param: ''\n  new_field: ''", 1),
			want:     true,
		},
		{
			name:     "export has non-empty list inside a credential the local entry lacks",
			local:    local,
			exported: strings.Replace(exported, "  query_param: ''", "  query_param: ''\n  scopes: [admin]", 1),
			want:     false,
		},
		{
			name:     "local sets inference_capable, export differs",
			local:    local + "inference_capable: true\n",
			exported: exported,
			want:     false,
		},
		{
			name:     "local credential field differs from export",
			local:    strings.Replace(local, "required: true", "required: true\n    query_param: token", 1),
			exported: exported,
			want:     false,
		},
		{
			name:     "local declares a non-empty value the export omits inside a credential",
			local:    strings.Replace(local, "required: true", "required: true\n    header_name: X-Key", 1),
			exported: strings.Replace(exported, "  header_name: ''\n", "", 1),
			want:     false,
		},
		{
			name:     "export has explicit MCP strict_tool_names false the local file omits",
			local:    "id: p\nendpoints:\n- host: mcp.example.com\n  mcp:\n    allow_all_known_mcp_methods: true\n",
			exported: "id: p\nendpoints:\n- host: mcp.example.com\n  mcp:\n    allow_all_known_mcp_methods: true\n    strict_tool_names: false\n",
			want:     false,
		},
		{
			name:     "local sets MCP strict_tool_names false the export omits",
			local:    "id: p\nendpoints:\n- host: mcp.example.com\n  mcp:\n    strict_tool_names: false\n",
			exported: "id: p\nendpoints:\n- host: mcp.example.com\n  mcp: {}\n",
			want:     false,
		},
		{
			name:     "local sets endpoint flag false the export omits",
			local:    strings.Replace(local, "    port: 443\n", "    port: 443\n    allow_uninspected_credentials: false\n", 1),
			exported: exported,
			want:     true,
		},
		{
			name:     "credential secret false only the export has",
			local:    local,
			exported: strings.Replace(exported, "  required: true\n", "  required: true\n  secret: false\n", 1),
			want:     true,
		},
		{
			name:     "export has unknown extra field",
			local:    "id: p\n",
			exported: "id: p\nannotations:\n  k: v\n",
			want:     false,
		},
		{
			name:     "local has field the export lacks",
			local:    "id: p\ndisplay_name: P\n",
			exported: "id: p\n",
			want:     false,
		},
		{
			name:     "map-shaped credentials compared exactly",
			local:    "id: p\ncredentials:\n  api_token: GH_TOKEN\n",
			exported: "id: p\ncredentials: {}\n",
			want:     false,
		},
		{
			name:     "list entries that are not maps compared exactly",
			local:    "id: p\ncredentials: [a]\n",
			exported: "id: p\ncredentials: [b]\n",
			want:     false,
		},
		{
			name:     "local object-form binaries vs exported scalar binaries",
			local:    "id: p\nbinaries:\n  - path: \"**/python\"\n  - path: \"**/curl\"\n",
			exported: "id: p\nbinaries:\n- '**/python'\n- '**/curl'\n",
			want:     true,
		},
		{
			name:     "mixed object and scalar local binaries vs scalar export",
			local:    "id: p\nbinaries:\n  - path: \"**/python\"\n  - \"**/curl\"\n",
			exported: "id: p\nbinaries:\n- '**/python'\n- '**/curl'\n",
			want:     true,
		},
		{
			name:     "object-form binaries in the full profile",
			local:    strings.Replace(local, "  - \"**/gh\"\n", "  - path: \"**/gh\"\n", 1),
			exported: exported,
			want:     true,
		},
		{
			name:     "object-form binary with a different path",
			local:    "id: p\nbinaries:\n  - path: \"**/python\"\n  - path: \"**/wget\"\n",
			exported: "id: p\nbinaries:\n- '**/python'\n- '**/curl'\n",
			want:     false,
		},
		{
			name:     "object-form binaries missing an exported path",
			local:    "id: p\nbinaries:\n  - path: \"**/python\"\n",
			exported: "id: p\nbinaries:\n- '**/python'\n- '**/curl'\n",
			want:     false,
		},
		{
			name:     "object-form binaries with an extra path",
			local:    "id: p\nbinaries:\n  - path: \"**/python\"\n  - path: \"**/curl\"\n  - path: \"**/node\"\n",
			exported: "id: p\nbinaries:\n- '**/python'\n- '**/curl'\n",
			want:     false,
		},
		{
			name:     "object-form binary with an extra key",
			local:    "id: p\nbinaries:\n  - path: \"**/curl\"\n    mode: strict\n",
			exported: "id: p\nbinaries:\n- '**/curl'\n",
			want:     false,
		},
		{
			name:     "object-form binary with a non-string path",
			local:    "id: p\nbinaries:\n  - path: [\"**/curl\"]\n",
			exported: "id: p\nbinaries:\n- '**/curl'\n",
			want:     false,
		},
		{
			name:     "object form outside the normalized fields compared exactly",
			local:    "id: p\nendpoints:\n  - path: api.example.com\n",
			exported: "id: p\nendpoints:\n- api.example.com\n",
			want:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := profileContentEqual([]byte(tt.local), []byte(tt.exported))
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestProfileContentEqual_UnparseableYAML(t *testing.T) {
	_, err := profileContentEqual([]byte("id: [unclosed"), []byte("id: p\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing local profile")

	_, err = profileContentEqual([]byte("id: p\n"), []byte("id: [unclosed"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing exported profile")
}

// TestImportProfile_AlreadyExists_StaleContent covers #7973: a blocked delete
// leaves old content on the gateway, so ImportProfile must fail rather than
// cache a false success.
func TestImportProfile_AlreadyExists_StaleContent(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "my-profile.yaml")
	require.NoError(t, os.WriteFile(profilePath, []byte("id: my-profile\ncredentials:\n  api_token: GH_TOKEN\n"), 0o644))

	// Gateway still has the old content.
	alreadyExistsThenExportStub(t, dir, "id: my-profile\ncredentials: {}\n")
	t.Setenv("PATH", dir)

	cachePath := profileFileCachePath("my-profile")
	t.Cleanup(func() { os.Remove(cachePath) })

	err := ImportProfile(context.Background(), "my-profile", profilePath)
	require.Error(t, err, "already-exists with mismatched gateway content must fail loudly")
	assert.Contains(t, err.Error(), "does not match")

	_, readErr := os.ReadFile(cachePath)
	assert.True(t, os.IsNotExist(readErr), "cache must not be written on a verified content mismatch")
}

// TestImportProfile_AlreadyExists_RemovedNonDefaultScalar covers a local edit
// that removes a non-default scalar the gateway still has after a blocked
// delete: it must fail loudly and must not cache a false success.
func TestImportProfile_AlreadyExists_RemovedNonDefaultScalar(t *testing.T) {
	tests := []struct {
		name     string
		exported string
	}{
		{"inference_capable", "id: my-profile\ninference_capable: true\n"},
		{"credential query_param", "id: my-profile\ncredentials:\n- name: api_token\n  query_param: token\n"},
		{"credential auth_style", "id: my-profile\ncredentials:\n- name: api_token\n  auth_style: bearer\n"},
		{"credential header_name", "id: my-profile\ncredentials:\n- name: api_token\n  header_name: X-Key\n"},
		{"endpoint flag", "id: my-profile\nendpoints:\n- host: api.github.com\n  allow_uninspected_credentials: true\n"},
		{"endpoint mcp strict_tool_names", "id: my-profile\nendpoints:\n- host: api.github.com\n  mcp:\n    strict_tool_names: false\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			profilePath := filepath.Join(dir, "my-profile.yaml")
			local := "id: my-profile\n"
			if strings.HasPrefix(tt.name, "credential") {
				local += "credentials:\n  - name: api_token\n"
			}
			if strings.HasPrefix(tt.name, "endpoint") {
				local += "endpoints:\n  - host: api.github.com\n"
			}
			if strings.HasSuffix(tt.name, "strict_tool_names") {
				local += "    mcp: {}\n"
			}
			require.NoError(t, os.WriteFile(profilePath, []byte(local), 0o644))

			alreadyExistsThenExportStub(t, dir, tt.exported)
			t.Setenv("PATH", dir)

			cachePath := profileFileCachePath("my-profile")
			t.Cleanup(func() { os.Remove(cachePath) })

			err := ImportProfile(context.Background(), "my-profile", profilePath)
			require.Error(t, err, "a stale non-default scalar left on the gateway must fail loudly")
			assert.Contains(t, err.Error(), "does not match")

			_, readErr := os.ReadFile(cachePath)
			assert.True(t, os.IsNotExist(readErr), "cache must not be written on a verified content mismatch")
		})
	}
}

// TestImportProfile_AlreadyExists_RemovedCredentials covers a local profile
// that dropped `credentials` while the gateway still has them.
func TestImportProfile_AlreadyExists_RemovedCredentials(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "my-profile.yaml")
	require.NoError(t, os.WriteFile(profilePath, []byte("id: my-profile\n"), 0o644))

	// Gateway still has the removed credentials.
	alreadyExistsThenExportStub(t, dir, "id: my-profile\ncredentials:\n  api_token: GH_TOKEN\n")
	t.Setenv("PATH", dir)

	cachePath := profileFileCachePath("my-profile")
	t.Cleanup(func() { os.Remove(cachePath) })

	err := ImportProfile(context.Background(), "my-profile", profilePath)
	require.Error(t, err, "a removed-field mismatch (gateway still has credentials) must fail loudly")
	assert.Contains(t, err.Error(), "does not match")

	_, readErr := os.ReadFile(cachePath)
	assert.True(t, os.IsNotExist(readErr), "cache must not be written on a verified content mismatch")
}

// TestImportProfile_AlreadyExists_RemovedEndpoints covers a local profile
// that dropped an `endpoints` grant the gateway still has.
func TestImportProfile_AlreadyExists_RemovedEndpoints(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "my-profile.yaml")
	require.NoError(t, os.WriteFile(profilePath, []byte("id: my-profile\n"), 0o644))

	// Gateway still has the removed endpoints grant.
	alreadyExistsThenExportStub(t, dir, "id: my-profile\nendpoints:\n  - host: internal.example.com\n    port: 443\n")
	t.Setenv("PATH", dir)

	cachePath := profileFileCachePath("my-profile")
	t.Cleanup(func() { os.Remove(cachePath) })

	err := ImportProfile(context.Background(), "my-profile", profilePath)
	require.Error(t, err, "a removed security-relevant field still present on the gateway must fail loudly")
	assert.Contains(t, err.Error(), "does not match")

	_, readErr := os.ReadFile(cachePath)
	assert.True(t, os.IsNotExist(readErr), "cache must not be written on a verified content mismatch")
}

// TestImportProfile_AlreadyExists_ExportFails verifies that an unverifiable
// "already exists" is not treated as success.
func TestImportProfile_AlreadyExists_ExportFails(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "my-profile.yaml")
	require.NoError(t, os.WriteFile(profilePath, []byte("id: my-profile"), 0o644))

	script := `#!/bin/sh
case "$3" in
  import)
    echo "profile already exists" >&2
    exit 1
    ;;
  export)
    echo "gateway unreachable" >&2
    exit 1
    ;;
esac
exit 0
`
	fakePath := filepath.Join(dir, "openshell")
	require.NoError(t, os.WriteFile(fakePath, []byte(script), 0o755))
	t.Setenv("PATH", dir)

	cachePath := profileFileCachePath("my-profile")
	t.Cleanup(func() { os.Remove(cachePath) })

	err := ImportProfile(context.Background(), "my-profile", profilePath)
	require.Error(t, err, "an unverifiable already-exists must not be treated as success")
	assert.Contains(t, err.Error(), "could not be verified")
}

func TestImportProfile_OtherError(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "my-profile.yaml")
	require.NoError(t, os.WriteFile(profilePath, []byte("id: my-profile"), 0o644))

	script := `#!/bin/sh
echo "connection refused" >&2
exit 1
`
	fakePath := filepath.Join(dir, "openshell")
	require.NoError(t, os.WriteFile(fakePath, []byte(script), 0o755))
	t.Setenv("PATH", dir)

	cachePath := profileFileCachePath("my-profile")
	t.Cleanup(func() { os.Remove(cachePath) })

	err := ImportProfile(context.Background(), "my-profile", profilePath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "my-profile.yaml")
	assert.Contains(t, err.Error(), "connection refused")
}

func TestImportProfile_SkipsWhenCacheMatches(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "test.yaml")
	require.NoError(t, os.WriteFile(profilePath, []byte("id: test-profile\nname: test"), 0o644))

	hash, err := hashProfileFile(profilePath)
	require.NoError(t, err)

	cachePath := profileFileCachePath("test-profile")
	require.NoError(t, os.WriteFile(cachePath, []byte(hash), 0o600))
	t.Cleanup(func() { os.Remove(cachePath) })

	// openshell is not in PATH — if ImportProfile tries to run it, it will fail.
	// A successful return means the cache short-circuited the import.
	t.Setenv("PATH", "")
	err = ImportProfile(context.Background(), "test-profile", profilePath)
	assert.NoError(t, err, "should skip import when cache hash matches")
}

func TestImportProfile_ReimportsWhenCacheDiffers(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "test.yaml")
	require.NoError(t, os.WriteFile(profilePath, []byte("id: test-profile\nname: test"), 0o644))

	cachePath := profileFileCachePath("test-profile")
	require.NoError(t, os.WriteFile(cachePath, []byte("stale-hash"), 0o600))
	t.Cleanup(func() { os.Remove(cachePath) })

	// With openshell missing, reimport will fail — proving the cache miss path runs.
	t.Setenv("PATH", t.TempDir())
	err := ImportProfile(context.Background(), "test-profile", profilePath)
	assert.Error(t, err, "should attempt reimport when cache hash differs")
}

func TestImportProfile_WritesCacheOnSuccess(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "test.yaml")
	require.NoError(t, os.WriteFile(profilePath, []byte("id: cached\nname: test"), 0o644))

	script := "#!/bin/sh\nexit 0\n"
	fakePath := filepath.Join(dir, "openshell")
	require.NoError(t, os.WriteFile(fakePath, []byte(script), 0o755))
	t.Setenv("PATH", dir)

	cachePath := profileFileCachePath("cached")
	t.Cleanup(func() { os.Remove(cachePath) })

	err := ImportProfile(context.Background(), "cached", profilePath)
	require.NoError(t, err)

	// Cache file should now contain the profile hash.
	cached, readErr := os.ReadFile(cachePath)
	require.NoError(t, readErr, "cache file should exist after successful import")

	expectedHash, _ := hashProfileFile(profilePath)
	assert.Equal(t, expectedHash, string(cached))
}

func TestImportProfile_WritesCacheOnAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "test.yaml")
	require.NoError(t, os.WriteFile(profilePath, []byte("id: exists\nname: test"), 0o644))

	// Gateway content matches the local file.
	alreadyExistsThenExportStub(t, dir, "id: exists\nname: test")
	t.Setenv("PATH", dir)

	cachePath := profileFileCachePath("exists")
	t.Cleanup(func() { os.Remove(cachePath) })

	err := ImportProfile(context.Background(), "exists", profilePath)
	require.NoError(t, err, "already-exists with matching gateway content should not be an error")

	// Cache should be written even on already-exists (parallel import).
	cached, readErr := os.ReadFile(cachePath)
	require.NoError(t, readErr, "cache file should exist after already-exists import")

	expectedHash, _ := hashProfileFile(profilePath)
	assert.Equal(t, expectedHash, string(cached))
}

func TestImportProfile_ConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "concurrent.yaml")
	require.NoError(t, os.WriteFile(profilePath, []byte("id: concurrent\nname: test"), 0o644))

	script := "#!/bin/sh\nexit 0\n"
	fakePath := filepath.Join(dir, "openshell")
	require.NoError(t, os.WriteFile(fakePath, []byte(script), 0o755))
	t.Setenv("PATH", dir)

	cachePath := profileFileCachePath("concurrent")
	t.Cleanup(func() { os.Remove(cachePath) })

	const goroutines = 12
	errs := make([]error, goroutines)
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = ImportProfile(context.Background(), "concurrent", profilePath)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "goroutine %d should succeed", i)
	}
}

func TestHashProfileFile_Deterministic(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "profile.yaml")
	require.NoError(t, os.WriteFile(f, []byte("id: test\nname: profile"), 0o644))

	h1, err := hashProfileFile(f)
	require.NoError(t, err)
	h2, err := hashProfileFile(f)
	require.NoError(t, err)
	assert.Equal(t, h1, h2, "hash must be deterministic for same content")
}

func TestHashProfileFile_ChangesOnContentChange(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "profile.yaml")
	require.NoError(t, os.WriteFile(f, []byte("id: test"), 0o644))

	h1, err := hashProfileFile(f)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(f, []byte("id: test-modified"), 0o644))

	h2, err := hashProfileFile(f)
	require.NoError(t, err)
	assert.NotEqual(t, h1, h2, "hash must change when file content changes")
}

func TestProfileFileCachePath_DeterministicAndUnique(t *testing.T) {
	p1 := profileFileCachePath("profile-a")
	p2 := profileFileCachePath("profile-a")
	p3 := profileFileCachePath("profile-b")

	assert.Equal(t, p1, p2, "same id must produce same cache path")
	assert.NotEqual(t, p1, p3, "different ids must produce different cache paths")
	assert.True(t, strings.HasPrefix(p1, os.TempDir()), "cache path must be in temp dir")
}

// TestEnsureProvider_AlreadyExists_FallsBackToUpdate uses a fake openshell
// script: first invocation exits 1 with AlreadyExists, second exits 0.
func TestEnsureProvider_AlreadyExists_FallsBackToUpdate(t *testing.T) {
	dir := t.TempDir()

	// Write a fake openshell that prints AlreadyExists on create, succeeds on update.
	script := `#!/bin/sh
if [ "$2" = "create" ]; then
  echo "code: 'Some entity that we attempted to create already exists', message: \"provider already exists\"" >&2
  exit 1
elif [ "$2" = "update" ]; then
  exit 0
else
  echo "unexpected subcommand: $2" >&2
  exit 1
fi
`
	fakePath := filepath.Join(dir, "openshell")
	require.NoError(t, os.WriteFile(fakePath, []byte(script), 0o755))
	t.Setenv("PATH", dir)

	err := EnsureProvider(context.Background(), "github", "github", map[string]string{"TOKEN": "tok"}, nil, false)
	assert.NoError(t, err)
}

// TestEnsureProvider_OtherError propagates non-AlreadyExists failures.
func TestEnsureProvider_OtherError(t *testing.T) {
	dir := t.TempDir()

	script := `#!/bin/sh
echo "status: PermissionDenied" >&2
exit 1
`
	fakePath := filepath.Join(dir, "openshell")
	require.NoError(t, os.WriteFile(fakePath, []byte(script), 0o755))
	t.Setenv("PATH", dir)

	err := EnsureProvider(context.Background(), "github", "github", nil, nil, false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "provider create")
}

// TestEnsureProvider_AlreadyExists_UpdateAlsoFails verifies error propagation
// and secret redaction when create returns AlreadyExists and update also fails.
func TestEnsureProvider_AlreadyExists_UpdateAlsoFails(t *testing.T) {
	dir := t.TempDir()

	script := `#!/bin/sh
if [ "$2" = "create" ]; then
  echo "code: 'Some entity that we attempted to create already exists', message: \"provider already exists\"" >&2
  exit 1
elif [ "$2" = "update" ]; then
  echo "gateway unavailable supersecret" >&2
  exit 1
fi
`
	fakePath := filepath.Join(dir, "openshell")
	require.NoError(t, os.WriteFile(fakePath, []byte(script), 0o755))
	t.Setenv("PATH", dir)

	err := EnsureProvider(context.Background(), "github", "github", map[string]string{"TOKEN": "supersecret"}, nil, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provider update")
	assert.NotContains(t, err.Error(), "supersecret", "secret must be redacted in update error")
	assert.Contains(t, err.Error(), "***")
}

func TestEnsureProvider_RejectsReservedCredentialKeys(t *testing.T) {
	tests := []struct {
		key     string
		wantErr bool
	}{
		{"API_KEY", false},
		{"LD_PRELOAD", true},
		{"ld_preload", true},
		{"PATH", true},
		{"BASH_ENV", true},
		{"HOME", true},
		{"HTTP_PROXY", true},
		{"https_proxy", true},
		{"NODE_OPTIONS", true},
		{"PROMPT_COMMAND", true},
		{"LD_AUDIT", true},
		{"SSL_CERT_FILE", true},
		{"SSL_CERT_DIR", true},
		{"CURL_CA_BUNDLE", true},
		{"NODE_EXTRA_CA_CERTS", true},
		{"HOSTALIASES", true},
		{"PYTHONSTARTUP", true},
		{"GIT_CONFIG_GLOBAL", true},
		{"GIT_EXEC_PATH", true},
		{"MY_TOKEN", false},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			// PATH is empty so openshell won't be found, but reserved key
			// check runs before exec.
			t.Setenv("PATH", "")
			err := EnsureProvider(context.Background(), "p", "custom", map[string]string{tt.key: "${SOME_VAR}"}, nil, true)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "reserved environment variable")
			} else {
				// Will fail with "openshell not found" but NOT with reserved key error.
				if err != nil {
					assert.NotContains(t, err.Error(), "reserved environment variable")
				}
			}
		})
	}
}

func TestEnsureProvider_AllowsReservedKeysForLocalProviders(t *testing.T) {
	t.Setenv("PATH", "")
	err := EnsureProvider(context.Background(), "p", "custom", map[string]string{"PATH": "val"}, nil, false)
	// Should fail with exec error (openshell not found), NOT reserved key error.
	if err != nil {
		assert.NotContains(t, err.Error(), "reserved environment variable")
	}
}

func TestBuildProviderArgs_ConfigNotExpandedForURL(t *testing.T) {
	t.Setenv("SECRET_VAR", "leaked-secret")

	config := map[string]string{
		"model": "${SECRET_VAR}",
	}

	args, _, _ := buildProviderArgs("p", "custom", nil, config, true)

	for _, arg := range args {
		assert.NotContains(t, arg, "leaked-secret",
			"URL-fetched provider config must not expand env vars")
	}
	assert.Contains(t, args, "model=${SECRET_VAR}",
		"URL-fetched provider config should preserve literal value")
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "'simple'"},
		{"has space", "'has space'"},
		{"it's", `'it'\''s'`},
		{"", "''"},
		{"a'b'c", `'a'\''b'\''c'`},
		{"/tmp/upload file.txt", "'/tmp/upload file.txt'"},
	}
	for _, tt := range tests {
		got := shellQuote(tt.input)
		assert.Equal(t, tt.want, got, "shellQuote(%q)", tt.input)
	}
}

func TestRandStringBytes_Length(t *testing.T) {
	for _, n := range []int{0, 1, 10, 50} {
		got := randStringBytes(n)
		assert.Len(t, got, n)
	}
}

func TestRandStringBytes_OnlyLetters(t *testing.T) {
	s := randStringBytes(200)
	for _, c := range s {
		assert.Contains(t, letterBytes, string(c))
	}
}

func TestRandStringBytes_NotConstant(t *testing.T) {
	a := randStringBytes(20)
	b := randStringBytes(20)
	assert.NotEqual(t, a, b, "two random strings should differ")
}

func TestBuildProviderArgs_ConfigExpandedForLocal(t *testing.T) {
	t.Setenv("MY_URL", "https://example.com")

	config := map[string]string{
		"base_url": "${MY_URL}",
	}

	args, _, _ := buildProviderArgs("p", "custom", nil, config, false)

	assert.Contains(t, args, "base_url=https://example.com",
		"local provider config should expand env vars")
}

func TestBuildProviderUpdateArgs_ConfigNotExpandedForURL(t *testing.T) {
	t.Setenv("SECRET_VAR", "leaked-secret")

	config := map[string]string{
		"model": "${SECRET_VAR}",
	}

	args := buildProviderUpdateArgs("p", nil, config, true)

	for _, arg := range args {
		assert.NotContains(t, arg, "leaked-secret",
			"URL-fetched provider config must not expand env vars on update")
	}
}

func TestProfileFileLockPath_DeterministicAndUnique(t *testing.T) {
	p1 := profileFileLockPath("profile-a")
	p2 := profileFileLockPath("profile-a")
	p3 := profileFileLockPath("profile-b")

	assert.Equal(t, p1, p2, "same id must produce same lock path")
	assert.NotEqual(t, p1, p3, "different ids must produce different lock paths")
	assert.True(t, strings.HasPrefix(p1, os.TempDir()), "lock path must be in temp dir")
	assert.True(t, strings.HasSuffix(p1, ".lock"), "lock path must end with .lock")
}

// TestImportProfile_FlockSerializesConcurrent verifies that the flock in
// ImportProfile serializes concurrent access. The fake openshell import
// handler uses a marker file to detect overlapping executions: it creates
// the marker at entry and removes it at exit, failing if the marker
// already exists. Without the flock, concurrent goroutines would enter
// the import section simultaneously and the marker-already-present check
// would trigger a failure, proving the lock is load-bearing.
func TestImportProfile_FlockSerializesConcurrent(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "flock-test.yaml")
	require.NoError(t, os.WriteFile(profilePath, []byte("id: flock-test\nname: test"), 0o644))

	// Marker file used by the fake openshell to detect concurrent imports.
	// The script creates the marker on entry and removes it on exit. If the
	// marker already exists at entry, another import is running concurrently
	// and the script fails — proving the flock was needed to serialize access.
	markerFile := filepath.Join(dir, "import-active")
	script := fmt.Sprintf(`#!/bin/sh
if [ "$3" = "import" ]; then
  if [ -f "%s" ]; then
    echo "concurrent import detected" >&2
    exit 1
  fi
  echo $$ > "%s"
  sleep 0.05
  rm -f "%s"
fi
exit 0
`, markerFile, markerFile, markerFile)
	fakePath := filepath.Join(dir, "openshell")
	require.NoError(t, os.WriteFile(fakePath, []byte(script), 0o755))
	t.Setenv("PATH", dir)

	cachePath := profileFileCachePath("flock-test")
	lockPath := profileFileLockPath("flock-test")
	t.Cleanup(func() {
		os.Remove(cachePath)
		os.Remove(lockPath)
	})

	const goroutines = 12
	errs := make([]error, goroutines)
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = ImportProfile(context.Background(), "flock-test", profilePath)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "goroutine %d should succeed under flock serialization", i)
	}
}

// TestEnsureProvider_RetriesUnsupportedProvider verifies that
// EnsureProvider retries when openshell returns the transient
// "unsupported provider type or profile" error.
func TestEnsureProvider_RetriesUnsupportedProvider(t *testing.T) {
	dir := t.TempDir()
	markerDir := filepath.Join(dir, "markers")
	require.NoError(t, os.MkdirAll(markerDir, 0o755))

	// Fake openshell: each create call creates a marker file. Once 3
	// markers exist the script succeeds. Uses only shell builtins to
	// avoid PATH issues (ls is replaced by glob counting).
	script := fmt.Sprintf(`#!/bin/sh
if [ "$2" = "create" ]; then
  # Create a unique marker file for this attempt.
  echo x > "%s/attempt.$$"
  count=0
  for f in "%s"/attempt.*; do
    [ -e "$f" ] && count=$((count + 1))
  done
  if [ "$count" -lt 3 ]; then
    echo "Error: × unsupported provider type or profile: fullsend-vertex-ai" >&2
    exit 1
  fi
  exit 0
fi
exit 0
`, markerDir, markerDir)
	fakePath := filepath.Join(dir, "openshell")
	require.NoError(t, os.WriteFile(fakePath, []byte(script), 0o755))
	t.Setenv("PATH", dir)

	err := EnsureProvider(context.Background(), "vertex-ai", "vertex-ai", nil, nil, false)
	assert.NoError(t, err, "should succeed after retries")

	// Verify that 3 attempts were made.
	entries, readErr := os.ReadDir(markerDir)
	require.NoError(t, readErr)
	assert.Len(t, entries, 3, "should have made 3 attempts")
}

// TestEnsureProvider_RetriesConcurrentUpdateConflict verifies that when the
// provider already exists and the gateway rejects the update because another
// run modified it concurrently, EnsureProvider retries the create+update
// cycle instead of failing the run.
func TestEnsureProvider_RetriesConcurrentUpdateConflict(t *testing.T) {
	dir := t.TempDir()
	markerDir := filepath.Join(dir, "markers")
	require.NoError(t, os.MkdirAll(markerDir, 0o755))

	// Fake openshell: create always reports the provider exists; update
	// fails with the optimistic-concurrency error until the 3rd attempt.
	script := fmt.Sprintf(`#!/bin/sh
if [ "$2" = "create" ]; then
  echo "Error: × code: 'Some entity that we attempted to create already exists', message: \"provider already exists\"" >&2
  exit 1
fi
if [ "$2" = "update" ]; then
  echo x > "%s/attempt.$$"
  count=0
  for f in "%s"/attempt.*; do
    [ -e "$f" ] && count=$((count + 1))
  done
  if [ "$count" -lt 3 ]; then
    echo "Error:   × code: 'The operation was aborted', message: \"provider was modified" >&2
    echo "  │ concurrently (current resource_version: 2)\"" >&2
    exit 1
  fi
  exit 0
fi
exit 0
`, markerDir, markerDir)
	fakePath := filepath.Join(dir, "openshell")
	require.NoError(t, os.WriteFile(fakePath, []byte(script), 0o755))
	t.Setenv("PATH", dir)

	err := EnsureProvider(context.Background(), "vertex-ai", "vertex-ai", nil, nil, false)
	assert.NoError(t, err, "should succeed after retrying the conflicting update")

	entries, readErr := os.ReadDir(markerDir)
	require.NoError(t, readErr)
	assert.Len(t, entries, 3, "should have retried the update 3 times")
}

func TestIsTransientProviderErr(t *testing.T) {
	t.Parallel()
	wrapped := "provider update \"vertex-ai\" failed: exit status 1 (output: Error:   × code: 'The operation was aborted', message: \"provider was modified\n  │ concurrently (current resource_version: 2)\"\n)"
	assert.False(t, isTransientProviderErr(nil))
	assert.True(t, isTransientProviderErr(fmt.Errorf("x: unsupported provider type or profile: p")))
	assert.True(t, isTransientProviderErr(errors.New(wrapped)), "must match across the CLI's line wrap")
	assert.True(t, isTransientProviderErr(errors.New("provider was modified concurrently")))
	assert.False(t, isTransientProviderErr(fmt.Errorf("status: PermissionDenied")))
	assert.False(t, isTransientProviderErr(fmt.Errorf("provider was modified by an operator")))

	// OpenShell 0.1.x wording for a profile a concurrent run is reimporting,
	// as seen in functional-tests with four parallel eval cases.
	notFound := `provider create "github-ro" failed: exit status 1 (output: Error:   × provider profile 'fullsend-github-ro' not found; import a matching profile`
	assert.True(t, isTransientProviderErr(errors.New(notFound)))
	notFoundWrapped := "Error:   × provider profile 'fullsend-vertex-ai' not\n  │ found; import a matching profile"
	assert.True(t, isTransientProviderErr(errors.New(notFoundWrapped)), "must match across the CLI's line wrap")
	// OpenShell 0.1.2 wording, as seen in functional-tests with parallel
	// triage eval cases (including the CLI's line wrap after "profile").
	notFound012 := "provider create \"vertex-ai\" failed: exit status 1 (output: Error:   × code: 'Client specified an invalid argument', message: \"provider profile\n  │ 'fullsend-vertex-ai' was not found in the requested scope; import a\n  │ matching profile before creating this provider\""
	assert.True(t, isTransientProviderErr(errors.New(notFound012)), "must match OpenShell 0.1.2's wording")
	assert.False(t, isTransientProviderErr(errors.New("provider 'github-ro' not found")), "a missing provider is not a missing profile")
}

// TestEnsureProvider_NoRetryOnOtherErrors verifies that non-transient
// errors are not retried.
func TestEnsureProvider_NoRetryOnOtherErrors(t *testing.T) {
	dir := t.TempDir()
	markerDir := filepath.Join(dir, "markers")
	require.NoError(t, os.MkdirAll(markerDir, 0o755))

	script := fmt.Sprintf(`#!/bin/sh
if [ "$2" = "create" ]; then
  echo x > "%s/attempt.$$"
  echo "status: PermissionDenied" >&2
  exit 1
fi
exit 0
`, markerDir)
	fakePath := filepath.Join(dir, "openshell")
	require.NoError(t, os.WriteFile(fakePath, []byte(script), 0o755))
	t.Setenv("PATH", dir)

	err := EnsureProvider(context.Background(), "p", "custom", nil, nil, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provider create")

	// Should have only attempted once — no retry on non-transient errors.
	entries, readErr := os.ReadDir(markerDir)
	require.NoError(t, readErr)
	assert.Len(t, entries, 1, "should not retry on non-transient errors")
}

// TestEnsureProvider_RetryCancelledByContext verifies that context
// cancellation during a provider creation attempt returns the context error
// instead of the process error produced when CommandContext kills openshell.
func TestEnsureProvider_RetryCancelledByContext(t *testing.T) {
	dir := t.TempDir()
	markerDir := filepath.Join(dir, "markers")
	require.NoError(t, os.MkdirAll(markerDir, 0o755))

	// Fake openshell: record the attempt, then block until context cancellation
	// kills the process. The busy loop uses only shell builtins so PATH can
	// contain just this fake binary.
	script := fmt.Sprintf(`#!/bin/sh
if [ "$2" = "create" ]; then
  echo x > "%s/attempt.$$"
  while :; do :; done
fi
exit 0
`, markerDir)
	fakePath := filepath.Join(dir, "openshell")
	require.NoError(t, os.WriteFile(fakePath, []byte(script), 0o755))
	t.Setenv("PATH", dir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- EnsureProvider(ctx, "p", "custom", nil, nil, false)
	}()

	require.Eventually(t, func() bool {
		entries, err := os.ReadDir(markerDir)
		return err == nil && len(entries) == 1
	}, 5*time.Second, 10*time.Millisecond, "provider creation should start")
	cancel()

	var err error
	select {
	case err = <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("EnsureProvider did not return after cancellation")
	}
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled, "should return context error when creation is cancelled")

	entries, readErr := os.ReadDir(markerDir)
	require.NoError(t, readErr)
	assert.Len(t, entries, 1, "should stop retrying when context is cancelled")
}

func TestResolvedBasename(t *testing.T) {
	t.Run("regular file", func(t *testing.T) {
		dir := t.TempDir()
		f := filepath.Join(dir, "hello.txt")
		require.NoError(t, os.WriteFile(f, []byte("hi"), 0o644))
		assert.Equal(t, "hello.txt", resolvedBasename(f))
	})

	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "content")
		require.NoError(t, os.WriteFile(target, []byte("data"), 0o644))
		link := filepath.Join(dir, "content.md")
		require.NoError(t, os.Symlink(target, link))
		assert.Equal(t, "content", resolvedBasename(link))
	})

	t.Run("nonexistent path", func(t *testing.T) {
		assert.Equal(t, "gone.txt", resolvedBasename("/no/such/gone.txt"))
	})
}

// stubOpenshellForExtract puts a fake openshell first on PATH. The listing
// runs "openshell sandbox exec …" and each copy runs
// "openshell sandbox download <name> <remote> <destDir>", so the script
// branches on $2.
func stubOpenshellForExtract(t *testing.T, execBody, downloadBody string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub requires a POSIX shell")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$2\" = \"exec\" ]; then\n" + execBody + "\nfi\n" +
		"if [ \"$2\" = \"download\" ]; then\n" + downloadBody + "\nfi\n" +
		"exit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestExtractOutputFiles_OpenshellFailureIsAnError(t *testing.T) {
	stubOpenshellForExtract(t,
		"  echo 'sandbox is not ready' >&2\n  exit 1",
		"  exit 0")

	got, err := ExtractOutputFiles("sb", "/sandbox/output", t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sandbox is not ready")
	assert.Empty(t, got)
}

func TestExtractOutputFiles_EmptyOutputIsNotAnError(t *testing.T) {
	stubOpenshellForExtract(t, "  exit 0", "  exit 0")

	got, err := ExtractOutputFiles("sb", "/sandbox/output", t.TempDir())
	assert.NoError(t, err)
	assert.Nil(t, got)
}

func TestExtractOutputFiles_ListedButNoneCopiedIsAnError(t *testing.T) {
	stubOpenshellForExtract(t,
		"  echo /sandbox/output/agent-result.json\n  exit 0",
		"  echo 'sandbox is not ready' >&2\n  exit 1")

	got, err := ExtractOutputFiles("sb", "/sandbox/output", t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 listed, none copied")
	assert.Empty(t, got)
}

func TestExtractOutputFiles_CopiesListedFiles(t *testing.T) {
	stubOpenshellForExtract(t,
		"  echo /sandbox/output/agent-result.json\n  exit 0",
		"  printf '{}' > \"$5/$(basename \"$4\")\"\n  exit 0")

	localDir := t.TempDir()
	got, err := ExtractOutputFiles("sb", "/sandbox/output", localDir)
	require.NoError(t, err)
	want := filepath.Join(localDir, "agent-result.json")
	assert.Equal(t, []string{want}, got)
	data, readErr := os.ReadFile(want)
	require.NoError(t, readErr)
	assert.Equal(t, "{}", string(data))
}

func writeFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

type gitRepo struct {
	t   *testing.T
	dir string
	env []string
}

func newGitRepo(t *testing.T) *gitRepo {
	t.Helper()
	home := t.TempDir()
	r := &gitRepo{t: t, dir: t.TempDir(), env: gitTestEnv(home)}
	_, err := runGit(t, r.dir, r.env, "init", "--quiet", "--initial-branch=main", ".")
	require.NoError(t, err)
	r.git("config", "user.name", "Test")
	r.git("config", "user.email", "test@example.invalid")
	r.git("config", "advice.graftFileDeprecated", "false")
	return r
}

// initReftableRepo initializes a reftable repository at dir, skipping when the
// native Git predates --ref-format=reftable. It is used by tests that exercise
// the reftable path; files-backend tests keep using newGitRepo.
func initReftableRepo(t *testing.T, dir string) *gitRepo {
	t.Helper()
	return initReftableRepoFormat(t, dir, "")
}

// initReftableRepoFormat initializes a reftable repository with an explicit
// object format; an empty format uses the native default.
func initReftableRepoFormat(t *testing.T, dir, objectFormat string) *gitRepo {
	t.Helper()
	requireGit(t)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	r := &gitRepo{t: t, dir: dir, env: gitTestEnv(t.TempDir())}
	args := []string{"init", "--quiet", "--initial-branch=main", "--ref-format=reftable"}
	if objectFormat != "" {
		args = append(args, "--object-format="+objectFormat)
	}
	out, err := runGit(t, r.dir, r.env, append(args, ".")...)
	if err != nil {
		if strings.Contains(out, "unknown option") || strings.Contains(out, "unknown ref storage format") {
			t.Skipf("git does not support --ref-format=reftable: %s", out)
		}
		require.NoError(t, err, out)
	}
	r.git("config", "user.name", "Test")
	r.git("config", "user.email", "test@example.invalid")
	return r
}

func newReftableRepo(t *testing.T) *gitRepo {
	t.Helper()
	return initReftableRepo(t, t.TempDir())
}

func newReftableRepoFormat(t *testing.T, objectFormat string) *gitRepo {
	t.Helper()
	return initReftableRepoFormat(t, t.TempDir(), objectFormat)
}

// requireReftableSanitization skips positive reftable sanitation tests on a
// native Git older than 2.53, whose strict ref verification cannot reject
// malformed reftable tables. Fixtures stay creatable on older Git so version
// rejection tests can still run there.
func requireReftableSanitization(t *testing.T) {
	t.Helper()
	requireGit(t)
	supported, err := nativeGitSupportsReftable(t.TempDir())
	require.NoError(t, err)
	if !supported {
		t.Skip("git predates 2.53 reftable verification")
	}
}

// installGitShim puts a wrapper first on PATH that runs body, then forwards to
// the real git. It lets a test alter one subcommand's behavior without
// replacing Git itself.
func installGitShim(t *testing.T, body string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	binDir := t.TempDir()
	script := "#!/bin/sh\n" + body + "\nexec \"" + realGit + "\" \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(filepath.ListSeparator)+os.Getenv("PATH"))
}

func gitTestEnv(home string) []string {
	env := make([]string, 0, len(os.Environ())+12)
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		// Inherited Git variables (GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE,
		// GIT_CONFIG_COUNT, ...) would redirect fixture commands outside the
		// fixture. Only the explicit settings appended below may apply.
		if key == "HOME" || strings.HasPrefix(key, "GIT_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"HOME="+home,
		"GIT_CONFIG_GLOBAL="+filepath.Join(home, ".gitconfig"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.invalid",
	)
}

// TestGitTestEnv_IgnoresInheritedGitVariables checks that inherited Git
// variables pointing at a decoy repository cannot redirect fixture commands or
// contaminate the fixture's identity.
func TestGitTestEnv_IgnoresInheritedGitVariables(t *testing.T) {
	decoy := newGitRepo(t)
	writeFile(t, decoy.dir, "decoy.txt", "decoy\n")
	decoy.git("add", "decoy.txt")
	decoy.git("commit", "--quiet", "-s", "-m", "decoy")
	decoyHead := decoy.git("rev-parse", "HEAD")
	decoyIndex := decoy.git("ls-files", "-s")
	decoyConfigPath := filepath.Join(decoy.dir, ".git", "config")
	decoyConfig, err := os.ReadFile(decoyConfigPath)
	require.NoError(t, err)

	t.Setenv("GIT_DIR", filepath.Join(decoy.dir, ".git"))
	t.Setenv("GIT_WORK_TREE", decoy.dir)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(decoy.dir, ".git", "index"))
	t.Setenv("GIT_CONFIG_GLOBAL", decoyConfigPath)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "user.email")
	t.Setenv("GIT_CONFIG_VALUE_0", "decoy@example.invalid")
	t.Setenv("GIT_AUTHOR_NAME", "Decoy")
	t.Setenv("GIT_AUTHOR_EMAIL", "decoy@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "Decoy")
	t.Setenv("GIT_COMMITTER_EMAIL", "decoy@example.invalid")

	fixture := newGitRepo(t)
	writeFile(t, fixture.dir, "fixture.txt", "fixture\n")
	fixture.git("add", "fixture.txt")
	fixture.git("commit", "--quiet", "-s", "-m", "fixture")

	assert.Equal(t, "fixture", fixture.git("log", "-1", "--format=%s"), "fixture commands must act on the fixture")
	assert.Equal(t, "1", fixture.git("rev-list", "--count", "HEAD"))
	assert.Equal(t, "fixture.txt", fixture.git("ls-files"), "fixture index must list only fixture files")
	assert.Equal(t, "main", fixture.git("symbolic-ref", "--short", "HEAD"))
	assert.Equal(t, "test@example.invalid", fixture.git("config", "user.email"), "fixture identity must ignore inherited config injection")
	assert.Equal(t, "Test", fixture.git("config", "user.name"))

	assert.Equal(t, decoyHead, decoy.git("rev-parse", "HEAD"), "decoy HEAD must be unchanged")
	assert.Equal(t, decoyIndex, decoy.git("ls-files", "-s"), "decoy index must be unchanged")
	gotConfig, err := os.ReadFile(decoyConfigPath)
	require.NoError(t, err)
	assert.Equal(t, decoyConfig, gotConfig, "decoy config must be unchanged")
	_, err = os.Lstat(filepath.Join(decoy.dir, "fixture.txt"))
	assert.True(t, os.IsNotExist(err), "fixture commit must not land in the decoy")
}

func (r *gitRepo) commitFile(name, content string) string {
	r.t.Helper()
	require.NoError(r.t, os.WriteFile(filepath.Join(r.dir, name), []byte(content+"\n"), 0o644))
	r.git("add", name)
	r.git("commit", "--quiet", "-m", content)
	return r.git("rev-parse", "HEAD")
}

func (r *gitRepo) snapshot() string {
	r.t.Helper()
	return strings.Join([]string{
		r.git("status", "--porcelain=v1"),
		r.git("diff", "--cached", "--name-status"),
		r.git("diff", "--name-status"),
		r.git("log", "--format=%H %s"),
	}, "\n")
}

func (r *gitRepo) gitAt(dir string, args ...string) string {
	r.t.Helper()
	out, err := runGit(r.t, dir, r.env, args...)
	require.NoErrorf(r.t, err, "git %v: %s", args, out)
	return out
}

func (r *gitRepo) git(args ...string) string {
	r.t.Helper()
	return r.gitAt(r.dir, args...)
}

func (r *gitRepo) tryGit(args ...string) (string, error) {
	return runGit(r.t, r.dir, r.env, args...)
}

func (r *gitRepo) isAncestor(ancestor, descendant string) bool {
	r.t.Helper()
	_, err := runGit(r.t, r.dir, r.env, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true
	}
	var exitErr *exec.ExitError
	require.ErrorAs(r.t, err, &exitErr)
	require.Equal(r.t, 1, exitErr.ExitCode())
	return false
}

func runGit(t *testing.T, dir string, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
