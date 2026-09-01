package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/pluginformat"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// captureStderr redirects os.Stderr to a pipe around fn and returns what
// was written (Bootstrap logs per-resource lines there).
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	func() {
		defer func() {
			os.Stderr = orig
			w.Close()
		}()
		fn()
	}()
	return <-done
}

// writeExtensionFixture builds an extension directory with nested files,
// an empty subdirectory (part of the hash) and names with spaces. No
// symlink: those are refused outright (TestPiExtensionTreeHash_RejectsNonFiles).
func writeExtensionFixture(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	files := map[string]string{
		"index.js":            "export default function (pi) { pi.registerTool({ name: 'go_diag' }); }\n",
		"package.json":        `{"name":"` + name + `","pi":{"extensions":["index.js"]}}`,
		"lib/util.js":         "export const x = 1;\n",
		"lib/with space.txt":  "spaces are fine\n",
		"node_modules/d/a.js": "module.exports = 1;\n",
		"empty.txt":           "",
	}
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "fixtures", "nested"), 0o755))
	return dir
}

// shaTool returns the sha256sum invocation the shell side of the hash can
// use on this host: the production form when `command -p sha256sum` exists,
// else a shim over `shasum -a 256` (stock macOS), which prints the same
// `<hex>  <path>` lines.
func shaTool(t *testing.T) string {
	t.Helper()
	if exec.Command("sh", "-c", "command -p sha256sum /dev/null >/dev/null").Run() == nil {
		return piSha256Tool
	}
	if _, err := exec.LookPath("shasum"); err != nil {
		t.Skip("neither sha256sum nor shasum available")
	}
	shim := filepath.Join(t.TempDir(), "sha256sum")
	require.NoError(t, os.WriteFile(shim, []byte("#!/bin/sh\nexec shasum -a 256 \"$@\"\n"), 0o755))
	return shellQuote(shim)
}

func shellsUnderTest(t *testing.T) []string {
	t.Helper()
	shells := []string{"sh"}
	if p, err := exec.LookPath("dash"); err == nil {
		shells = append(shells, p) // the sandbox image's /bin/sh
	}
	return shells
}

func TestPiExtensionTreeHash_MatchesShell(t *testing.T) {
	t.Parallel()
	dir := writeExtensionFixture(t, "go-diagnostics")
	want, err := piExtensionTreeHash(dir)
	require.NoError(t, err)
	require.Len(t, want, 64)

	tool := shaTool(t)
	for _, sh := range shellsUnderTest(t) {
		out, err := exec.Command(sh, "-c", piTreeHashCommand(dir, tool)).CombinedOutput()
		require.NoError(t, err, "%s: %s", sh, out)
		assert.Equal(t, want, strings.TrimSpace(string(out)), "shell %s must reproduce the Go tree hash", sh)
	}

	// The hash tracks content, names and the file set: a changed byte, a
	// renamed file and an added file each move it.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "lib", "util.js"), []byte("export const x = 2;\n"), 0o644))
	changed, err := piExtensionTreeHash(dir)
	require.NoError(t, err)
	assert.NotEqual(t, want, changed)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "extra.js"), nil, 0o644))
	added, err := piExtensionTreeHash(dir)
	require.NoError(t, err)
	assert.NotEqual(t, changed, added)

	// Directories are part of the hash: an empty `skills/` alone flips pi
	// from index.js to package layout, so it must move the verdict, and the
	// shell side must move with it. So must a renamed directory.
	require.NoError(t, os.Mkdir(filepath.Join(dir, "skills"), 0o755))
	withDir, err := piExtensionTreeHash(dir)
	require.NoError(t, err)
	assert.NotEqual(t, added, withDir, "an added empty directory must change the hash")
	require.NoError(t, os.Rename(filepath.Join(dir, "skills"), filepath.Join(dir, "themes")))
	renamedDir, err := piExtensionTreeHash(dir)
	require.NoError(t, err)
	assert.NotEqual(t, withDir, renamedDir, "a renamed directory must change the hash")
	for _, sh := range shellsUnderTest(t) {
		out, err := exec.Command(sh, "-c", piTreeHashCommand(dir, tool)).CombinedOutput()
		require.NoError(t, err, "%s: %s", sh, out)
		assert.Equal(t, renamedDir, strings.TrimSpace(string(out)), "shell %s must see directories too", sh)
	}

	// Empty directory hashes deterministically too (no file lines, one "."
	// directory line).
	empty := t.TempDir()
	got, err := piExtensionTreeHash(empty)
	require.NoError(t, err)
	out, err := exec.Command("sh", "-c", piTreeHashCommand(empty, tool)).CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Equal(t, got, strings.TrimSpace(string(out)))

	// A tree of nothing but directories still hashes, and differs from the
	// empty one.
	dirsOnly := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dirsOnly, "a", "b"), 0o755))
	gotDirs, err := piExtensionTreeHash(dirsOnly)
	require.NoError(t, err)
	assert.NotEqual(t, got, gotDirs)
	out, err = exec.Command("sh", "-c", piTreeHashCommand(dirsOnly, tool)).CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Equal(t, gotDirs, strings.TrimSpace(string(out)))
}

// TestPiExtensionTreeHash_RejectsNonFiles pins the symlink rule from both
// sides: the host refuses to hash the tree at all (naming the entry), and
// the sandbox pipeline prints nothing so the guard's comparison fails.
func TestPiExtensionTreeHash_RejectsNonFiles(t *testing.T) {
	t.Parallel()
	tool := shaTool(t)

	// A planted `index.js -> /elsewhere/evil.js` hijacks the extension pi
	// loads, so it must never hash like a clean tree.
	dir := writeExtensionFixture(t, "go-diagnostics")
	clean, err := piExtensionTreeHash(dir)
	require.NoError(t, err)
	outside := filepath.Join(t.TempDir(), "evil.js")
	require.NoError(t, os.WriteFile(outside, []byte("export default function () {}\n"), 0o644))
	require.NoError(t, os.Remove(filepath.Join(dir, "index.js")))
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, "index.js")))
	_, err = piExtensionTreeHash(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "index.js")
	assert.Contains(t, err.Error(), "neither a regular file nor a directory")

	for _, sh := range shellsUnderTest(t) {
		out, err := exec.Command(sh, "-c", piTreeHashCommand(dir, tool)).CombinedOutput()
		if err == nil {
			assert.Empty(t, strings.TrimSpace(string(out)), "shell %s must print no hash for a tree with a symlink", sh)
		}
		assert.NotEqual(t, clean, strings.TrimSpace(string(out)))
	}

	// The guard for such a tree exits 96 rather than letting pi start.
	exts := []piManifestExtension{{Name: "go-diagnostics", Path: dir, SHA256: clean}}
	cmd := exec.Command("sh", "-c", piExtensionsGuardWith(exts, tool)+" && echo RAN")
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, piExtensionTamperedExit, exitErr.ExitCode())
	assert.NotContains(t, string(out), "RAN")

	// A symlink nested below the root is caught the same way.
	nested := writeExtensionFixture(t, "nested-link")
	require.NoError(t, os.Symlink(outside, filepath.Join(nested, "lib", "shim.js")))
	_, err = piExtensionTreeHash(nested)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lib/shim.js")
}

// TestPiExtensionsGuard_EmptyDirectoryTampering is the second half of the
// directory rule: `mkdir skills` inside a loaded extension turns it into a
// package layout pi ignores, and adds no file, so only the directory part
// of the hash can catch it.
func TestPiExtensionsGuard_EmptyDirectoryTampering(t *testing.T) {
	t.Parallel()
	tool := shaTool(t)
	dir := writeExtensionFixture(t, "go-diagnostics")
	sum, err := piExtensionTreeHash(dir)
	require.NoError(t, err)
	exts := []piManifestExtension{{Name: "go-diagnostics", Path: dir, SHA256: sum}}

	require.NoError(t, os.Mkdir(filepath.Join(dir, "skills"), 0o755))
	out, err := exec.Command("sh", "-c", piExtensionsGuardWith(exts, tool)+" && echo RAN").CombinedOutput()
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr, string(out))
	assert.Equal(t, piExtensionTamperedExit, exitErr.ExitCode())
	assert.NotContains(t, string(out), "RAN")
	assert.Contains(t, string(out), `fullsend: pi extension "go-diagnostics" is missing or was modified`)
}

func TestPiExtensionTreeHash_RejectsUnhashableNames(t *testing.T) {
	t.Parallel()
	// GNU sha256sum escapes backslashes, newlines and carriage returns and
	// prefixes the line with "\", so the shell side could never match.
	for _, name := range []string{"a\\b.js", "a\rb.js", "a\nb.js"} {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), nil, 0o644))
		_, err := piExtensionTreeHash(dir)
		require.Errorf(t, err, "%q must be refused", name)
		assert.Contains(t, err.Error(), "carriage return or backslash")
	}

	// Directory names are held to the same rule: they go through the same
	// `find` listing.
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "a\rb"), 0o755))
	_, err := piExtensionTreeHash(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "carriage return or backslash")

	_, err = piExtensionTreeHash(filepath.Join(dir, "missing"))
	require.Error(t, err)
}

// TestPiExtensionsGuard runs the rendered guard under a real sh: it must
// exit 96 without running what follows when an extension is missing or
// modified, and fall through when every tree matches.
func TestPiExtensionsGuard(t *testing.T) {
	t.Parallel()
	tool := shaTool(t)
	dir := writeExtensionFixture(t, "go-diagnostics")
	sum, err := piExtensionTreeHash(dir)
	require.NoError(t, err)
	exts := []piManifestExtension{{Name: "go-diagnostics", Path: dir, SHA256: sum}}

	run := func(sh string) (int, string) {
		cmd := exec.Command(sh, "-c", piExtensionsGuardWith(exts, tool)+" && echo RAN")
		out, err := cmd.CombinedOutput()
		code := 0
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			require.NoError(t, err, string(out))
		}
		return code, string(out)
	}

	for _, sh := range shellsUnderTest(t) {
		code, out := run(sh)
		assert.Equal(t, 0, code, "%s intact: %s", sh, out)
		assert.Contains(t, out, "RAN")
	}

	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.js"), []byte("// tampered\n"), 0o644))
	code, out := run("sh")
	assert.Equal(t, piExtensionTamperedExit, code, "modified extension")
	assert.NotContains(t, out, "RAN")
	assert.Contains(t, out, `fullsend: pi extension "go-diagnostics" is missing or was modified`)

	require.NoError(t, os.RemoveAll(dir))
	code, out = run("sh")
	assert.Equal(t, piExtensionTamperedExit, code, "missing extension")
	assert.NotContains(t, out, "RAN")

	assert.Equal(t, "", piExtensionsGuard(nil), "no extensions, no guard")
}

func TestPiResolveRunPlugins(t *testing.T) {
	t.Parallel()
	dir := writeExtensionFixture(t, "go-diagnostics")
	sum, err := piExtensionTreeHash(dir)
	require.NoError(t, err)

	exts, err := piResolveRunPlugins([]PluginInput{
		{Path: dir, Kind: pluginformat.KindPi, PiArgs: []string{"--strict"}, Env: map[string]string{"GO_DIAG": "1"}},
		{Name: "explicit", Path: dir, Kind: pluginformat.KindPi},
		// A Claude plugin in the same list belongs to another runtime.
		{Name: "claude-one", Path: dir, Kind: pluginformat.KindClaude},
	})
	require.NoError(t, err)
	require.Len(t, exts, 2)
	assert.Equal(t, piManifestExtension{
		Name: "go-diagnostics", Path: "/sandbox/pi-config/extensions/go-diagnostics", SHA256: sum,
		Args: []string{"--strict"}, Env: map[string]string{"GO_DIAG": "1"},
	}, exts[0])
	assert.Equal(t, "explicit", exts[1].Name, "an explicit name wins over the basename")
	assert.Equal(t, "/sandbox/pi-config/extensions/explicit", exts[1].Path)

	_, err = piResolveRunPlugins(piPlugins(filepath.Join(t.TempDir(), "missing")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing")

	_, err = piResolveRunPlugins([]PluginInput{
		{Path: dir, Kind: pluginformat.KindPi},
		{Name: "go-diagnostics", Path: t.TempDir(), Kind: pluginformat.KindPi},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "two extension paths both resolve to the sandbox name")

	for _, reserved := range piReservedExtensionNames {
		_, err = piResolveRunPlugins([]PluginInput{{Name: reserved, Path: dir, Kind: pluginformat.KindPi}})
		require.Error(t, err, reserved)
		assert.Contains(t, err.Error(), "reserved")
	}

	got, err := piResolveRunPlugins(nil)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestBuildPiRunCommand_Extensions(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
	t.Setenv(piProviderEnv, "")
	m := &piManifest{AgentName: "code", Model: "opus", Tools: nil, Hooks: &piHooksManifest{}}
	params := piTestParams()
	params.HooksSettingsPath = "/sandbox/claude-config/hooks.json"
	exts := []piManifestExtension{
		{Name: "go-diagnostics", Path: "/sandbox/pi-config/extensions/go-diagnostics", SHA256: strings.Repeat("a", 64)},
		{Name: "pi-fff", Path: "/sandbox/pi-config/extensions/pi-fff", SHA256: strings.Repeat("b", 64),
			Args: []string{"--fff-mode", "over'ride"}, Env: map[string]string{"FFF_MULTIGREP": "1", "FFF_ROOT": "/sandbox/work space"}},
	}
	cmd := buildPiRunCommand(params, m, exts)

	// Preflight: after the pi pin and the hook guard, before .env is sourced.
	guard := piExtensionsGuard(exts)
	require.NotEmpty(t, guard)
	guardIdx := strings.Index(cmd, guard)
	hooksIdx := strings.Index(cmd, piHooksGuard("/sandbox/pi-config/fullsend-hooks.js", "/sandbox/pi-config/fullsend-manifest.json"))
	envIdx := strings.Index(cmd, ". '/sandbox/workspace/.env'")
	require.True(t, guardIdx > 0 && hooksIdx > 0 && envIdx > 0, cmd)
	assert.True(t, hooksIdx < guardIdx && guardIdx < envIdx, "hook guard, then extension guard, then .env: %s", cmd)
	assert.Contains(t, guard, strings.Repeat("a", 64))
	assert.Contains(t, guard, strings.Repeat("b", 64))
	assert.Contains(t, guard, "exit 96")

	// -e order: provider extension, hook adapter, then declared extensions
	// in harness order with their args quoted verbatim after the path.
	eList := `-e '/usr/local/share/pi-extensions/anthropic-vertex' -e '/sandbox/pi-config/fullsend-hooks.js' -e '/sandbox/pi-config/extensions/go-diagnostics' -e '/sandbox/pi-config/extensions/pi-fff' '--fff-mode' 'over'\''ride'`
	assert.Contains(t, cmd, eList, cmd)

	// env: exported right before pi, after the runtime's own exports, keys
	// sorted within an extension; values shell-quoted.
	envExports := `&& export FFF_MULTIGREP='1' && export FFF_ROOT='/sandbox/work space' && "$FULLSEND_PI_BIN" --print`
	assert.Contains(t, cmd, envExports, cmd)
	assert.Less(t, strings.Index(cmd, `export GOOGLE_CLOUD_PROJECT=`), strings.Index(cmd, "export FFF_MULTIGREP="), "runtime exports come first")

	// --tools is untouched by extensions: nil tools keeps pi's defaults.
	assert.NotContains(t, cmd, "--tools")
	assert.NotContains(t, cmd, "--no-builtin-tools")

	// Without extensions nothing is added, and a declared tools: list is
	// still rendered as before (extension tools are then hidden by pi).
	m.Tools = []string{"bash", "read"}
	plain := buildPiRunCommand(params, m, nil)
	assert.NotContains(t, plain, "pi-config/extensions/")
	assert.NotContains(t, plain, "exit 96")
	assert.Contains(t, plain, "--tools 'bash,read'")
	withTools := buildPiRunCommand(params, m, exts)
	assert.Contains(t, withTools, "--tools 'bash,read'")

	// Hooks disabled: extension guard still runs (it is independent of the
	// hook adapter) and the adapter is not loaded.
	params.HooksSettingsPath = ""
	noHooks := buildPiRunCommand(params, m, exts)
	assert.Contains(t, noHooks, guard)
	assert.NotContains(t, noHooks, "fullsend-hooks.js")
	assert.Contains(t, noHooks, `-e '/usr/local/share/pi-extensions/anthropic-vertex' -e '/sandbox/pi-config/extensions/go-diagnostics'`)
}

func TestPiRuntimeBootstrap_Extensions(t *testing.T) {
	work := t.TempDir()
	logPath := filepath.Join(work, "openshell.log")
	store := filepath.Join(work, "store")
	fakeOpenshellPi(t, logPath, store, "/dev/null")

	ext := writeExtensionFixture(t, "go-diagnostics")
	sum, err := piExtensionTreeHash(ext)
	require.NoError(t, err)

	h := &harness.Harness{Security: &harness.SecurityConfig{SandboxHooks: &harness.SandboxHooks{}}}
	in := piHooksBootstrapInput{
		bootstrapInput: bootstrapInput{
			sandboxName: "sb",
			agentPath:   writeAgentFile(t, "---\nname: code\n---\nBody"),
			agentName:   "code",
			plugins: []PluginInput{
				{Path: ext, Kind: pluginformat.KindPi, PiArgs: []string{"--strict"}, Env: map[string]string{"GO_DIAG": "1"}},
			},
		},
		hooks: security.SandboxHookConfigFromHarness(h),
	}
	stderr := captureStderr(t, func() {
		require.NoError(t, PiRuntime{}.Bootstrap(in))
	})
	assert.Contains(t, stderr, `Extension "go-diagnostics": uploaded to sandbox`)

	cfg := PiRuntime{}.ConfigDir()
	var m piManifest
	require.NoError(t, json.Unmarshal(storedUpload(t, store, cfg+"/fullsend-manifest.json"), &m))
	require.Len(t, m.Extensions, 1)
	assert.Equal(t, piManifestExtension{
		Name: "go-diagnostics", Path: cfg + "/extensions/go-diagnostics", SHA256: sum,
		Args: []string{"--strict"}, Env: map[string]string{"GO_DIAG": "1"},
	}, m.Extensions[0])
	assert.Nil(t, m.Tools, "extensions do not touch the tool allowlist")

	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	logStr := string(log)
	assert.Contains(t, logStr, "mkdir -p '"+cfg+"/skills' '"+cfg+"/extensions' ")
	// Directory uploads go through the tar path and land under extensions/<name>.
	assert.Contains(t, logStr, "mkdir -p '"+cfg+"/extensions/go-diagnostics'")

	// The manifest key is omitted entirely when there are no extensions.
	raw := storedUpload(t, store, cfg+"/fullsend-manifest.json")
	assert.Contains(t, string(raw), `"extensions"`)
	require.NoError(t, PiRuntime{}.Bootstrap(bootstrapInput{sandboxName: "sb", agentPath: in.agentPath, agentName: "code"}))
	raw = storedUpload(t, store, cfg+"/fullsend-manifest.json")
	assert.NotContains(t, string(raw), `"extensions"`)

	// Name collisions with the runner's own extensions and between entries
	// fail before anything is uploaded.
	other := writeExtensionFixture(t, "go-diagnostics")
	err = PiRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "sb", agentPath: in.agentPath, agentName: "code",
		plugins: piPlugins(ext, other),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "two extension paths both resolve to the sandbox name")
	err = PiRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "sb", agentPath: in.agentPath, agentName: "code",
		plugins: []PluginInput{{Name: "fullsend-hooks", Path: ext, Kind: pluginformat.KindPi}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reserved")
}

func TestPiRuntimeRun_ExtensionTamperedFailsClosed(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
	work := t.TempDir()
	store := filepath.Join(work, "store")
	fakeOpenshellPi(t, filepath.Join(work, "openshell.log"), store, "/dev/null")
	ext := writeExtensionFixture(t, "go-diagnostics")
	require.NoError(t, PiRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "sb", agentPath: writeAgentFile(t, "---\nname: code\n---\nBody"), agentName: "code",
		plugins: piPlugins(ext),
	}))
	// Replace the fake so the run command's extension guard fails the way
	// a modified or deleted extension directory would (exit 96).
	binDir := t.TempDir()
	script := `#!/bin/sh
if [ "$2" = "exec" ]; then
  for last; do :; done
  case "$last" in
    cat\ *) f=$(printf '%s' "${last#cat }" | tr -d "'" | tr '/' '_'); cat '` + store + `'/"$f"; exit $? ;;
    *"exit 96"*) echo 'fullsend: pi extension "go-diagnostics" is missing or was modified' >&2; exit 96 ;;
  esac
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	exit, err := PiRuntime{}.Run(context.Background(), RunParams{
		SandboxName: "sb", RepoDir: "/r", Timeout: 30 * time.Second,
		Plugins: piPlugins(ext),
		OnEvent: func(AgentEvent) {},
	}, ui.New(os.Stderr), time.Now(), &RunMetrics{})
	assert.Equal(t, piExtensionTamperedExit, exit)
	require.ErrorContains(t, err, "pi extension directory")
	require.ErrorContains(t, err, "missing or was modified")

	// A host directory that vanished between Bootstrap and Run is reported
	// before pi is started.
	require.NoError(t, os.RemoveAll(ext))
	exit, err = PiRuntime{}.Run(context.Background(), RunParams{
		SandboxName: "sb", RepoDir: "/r", Timeout: 30 * time.Second,
		Plugins: piPlugins(ext),
		OnEvent: func(AgentEvent) {},
	}, ui.New(os.Stderr), time.Now(), &RunMetrics{})
	assert.Equal(t, -1, exit)
	require.ErrorContains(t, err, "hashing pi extension")
}

// TestDummyRuntimeBootstrap_PluginsSkippedWithWarning is the dummy
// runtime's half of the same contract: BootstrapInput.Plugins() must
// never be dropped without a word. The exec is stubbed, so this needs no
// sandbox gateway.
func TestDummyRuntimeBootstrap_PluginsSkippedWithWarning(t *testing.T) {
	var execCalls int
	r := DummyRuntime{ExecFn: func(_, _ string, _ time.Duration) (string, string, int, error) {
		execCalls++
		return "", "", 0, nil
	}}
	ext := writeExtensionFixture(t, "go-diagnostics")
	stderr := captureStderr(t, func() {
		require.NoError(t, r.Bootstrap(bootstrapInput{
			sandboxName: "sb",
			plugins: []PluginInput{
				{Path: ext, Kind: pluginformat.KindPi},
				{Name: "named", Path: ext, Kind: pluginformat.KindPi},
				{Name: "a-claude-one", Path: ext, Kind: pluginformat.KindClaude},
				{Path: ""},
			},
		}))
	})
	assert.Contains(t, stderr, `Plugin "go-diagnostics" (pi): skipped — the dummy runtime loads no plugins (see docs/runtimes.md)`)
	assert.Contains(t, stderr, `Plugin "named" (pi): skipped`)
	assert.Contains(t, stderr, `Plugin "a-claude-one" (claude): skipped`)
	assert.Equal(t, 1, execCalls, "the skip loop does not stop the mkdir")

	// Nothing is printed when the harness declares none.
	stderr = captureStderr(t, func() {
		require.NoError(t, r.Bootstrap(bootstrapInput{sandboxName: "sb"}))
	})
	assert.NotContains(t, stderr, "Plugin")
}

func TestClaudeRuntimeBootstrap_PiPluginsSkippedWithWarning(t *testing.T) {
	work := t.TempDir()
	logPath := filepath.Join(work, "openshell.log")
	fakeOpenshellPi(t, logPath, filepath.Join(work, "store"), "/dev/null")
	ext := writeExtensionFixture(t, "go-diagnostics")
	agent := writeAgentFile(t, "---\nname: code\n---\nBody")
	stderr := captureStderr(t, func() {
		require.NoError(t, ClaudeRuntime{}.Bootstrap(bootstrapInput{
			sandboxName: "sb", agentPath: agent, agentName: "code",
			plugins: []PluginInput{
				{Path: ext, Kind: pluginformat.KindPi},
				{Name: "named", Path: ext, Kind: pluginformat.KindPi},
			},
		}))
	})
	assert.Contains(t, stderr, `Plugin "go-diagnostics": skipped — the Claude Code runtime does not load pi extensions (see docs/runtimes.md)`)
	assert.Contains(t, stderr, `Plugin "named": skipped`)
	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.NotContains(t, string(log), "extensions/", "nothing is uploaded for extensions on Claude Code")
}

// TestPiRuntimeEnvExports_DisablesJitiCache pins the loader-cache switch.
// pi loads every `-e` module through jiti 2.7.0 with fsCache on by default
// (createJiti in dist/core/extensions/loader.js passes no fsCache, and jiti
// resolves it from JITI_FS_CACHE then JITI_CACHE then true). The cache
// keys on a ` /* v9-<hash(source)> */` trailer only, so an entry whose body
// was rewritten with the trailer left intact is executed while the source
// file — and therefore the extension tree hash and the hook adapter's
// SHA-256 — is unchanged. Reproduced on pi 0.84.4; see
// testdata/pi/jiti-cache-check.sh.
func TestPiRuntimeEnvExports_DisablesJitiCache(t *testing.T) {
	t.Parallel()
	exports := PiRuntime{}.EnvExports()
	assert.Contains(t, exports, "export JITI_FS_CACHE=false",
		"pi's module loader must not read a transpile cache the agent can write")

	// The export has to survive `. .env`: buildPiRunCommand re-emits
	// EnvExports() after sourcing it, so the agent cannot turn the cache
	// back on for the next iteration.
	cmd := buildPiRunCommand(RunParams{RepoDir: "/sandbox/workspace/repo"}, &piManifest{}, nil)
	env := strings.Index(cmd, ". '/sandbox/workspace/.env'")
	jiti := strings.Index(cmd, "export JITI_FS_CACHE=false")
	require.GreaterOrEqual(t, env, 0)
	require.GreaterOrEqual(t, jiti, 0)
	assert.Greater(t, jiti, env, "JITI_FS_CACHE must be re-exported after .env is sourced")
}
