package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestPiEditRepairEnabled(t *testing.T) {
	t.Parallel()
	assert.True(t, piEditRepairEnabled(nil), "nil is pi's default set, which has edit")
	assert.False(t, piEditRepairEnabled([]string{}), "an empty list is --no-builtin-tools: loading it would grant edit")
	assert.False(t, piEditRepairEnabled([]string{"read", "grep"}))
	assert.True(t, piEditRepairEnabled([]string{"read", "edit"}))
	assert.Contains(t, piDefaultTools, piEditToolName, "the nil case relies on the default set carrying edit")
}

// TestBuildPiRunCommand_EditRepairGate covers when Run loads the extension:
// only for a tool list that grants edit, with its guard before the
// agent-writable .env and its -e before the declared extensions.
func TestBuildPiRunCommand_EditRepairGate(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
	t.Setenv(piProviderEnv, "")
	ext := PiRuntime{}.ConfigDir() + "/" + piEditRepairExtensionFile
	declared := []piManifestExtension{{Name: "go-diagnostics", Path: "/sandbox/pi-config/extensions/go-diagnostics", SHA256: strings.Repeat("a", 64)}}

	cmd := buildPiRunCommand(piTestParams(), &piManifest{AgentName: "code"}, declared, "")
	guard := piEditRepairGuard(ext)
	guardIdx := strings.Index(cmd, guard)
	envIdx := strings.Index(cmd, ". '/sandbox/workspace/.env'")
	require.True(t, guardIdx > 0 && envIdx > 0, cmd)
	assert.Less(t, guardIdx, envIdx, "the guard runs before .env can shadow its tools")
	assert.Contains(t, cmd, "-e '"+ext+"' -e '/sandbox/pi-config/extensions/go-diagnostics'",
		"runner-owned before declared; a declared extension must not also register edit, since pi rejects two extensions that register the same tool name")

	cmd = buildPiRunCommand(piTestParams(), &piManifest{AgentName: "code", Tools: []string{}}, nil, "")
	assert.Contains(t, cmd, "--no-builtin-tools")
	assert.NotContains(t, cmd, piEditRepairExtensionFile, "under --no-builtin-tools the extension's edit would not be filtered")

	cmd = buildPiRunCommand(piTestParams(), &piManifest{AgentName: "triage", Tools: []string{"bash", "read"}}, nil, "")
	assert.NotContains(t, cmd, piEditRepairExtensionFile)

	cmd = buildPiRunCommand(piTestParams(), &piManifest{AgentName: "code", Tools: []string{"read", "edit"}}, nil, "")
	assert.Contains(t, cmd, "--tools 'read,edit'")
	assert.Contains(t, cmd, "-e '"+ext+"'")
	assert.Contains(t, cmd, guard)
}

// TestPiEditRepairGuard runs the rendered guard under a real sh: it must stop
// before pi starts when the file is missing or is not the embedded copy, and
// the tools it uses must not be shadowable from the agent-writable .env.
func TestPiEditRepairGuard(t *testing.T) {
	t.Parallel()
	if err := exec.Command("sh", "-c", "command -p sha256sum /dev/null >/dev/null && command -p cut -d' ' -f1 /dev/null").Run(); err != nil {
		t.Skip("sha256sum/cut not on the default PATH (stock macOS); the sandbox image has coreutils")
	}
	ext := filepath.Join(t.TempDir(), piEditRepairExtensionFile)
	run := func(prefix string) (int, string) {
		out, err := exec.Command("sh", "-c", prefix+piEditRepairGuard(ext)+" && echo RAN").CombinedOutput()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), string(out)
		}
		require.NoError(t, err, string(out))
		return 0, string(out)
	}

	code, out := run("")
	assert.Equal(t, piEditRepairTamperedExit, code, "missing extension")
	assert.NotContains(t, out, "RAN")
	assert.Contains(t, out, "edit-repair extension missing or modified")

	require.NoError(t, os.WriteFile(ext, append(append([]byte{}, piEditRepairExtensionJS...), []byte("\n// tampered\n")...), 0o644))
	code, out = run("")
	assert.Equal(t, piEditRepairTamperedExit, code, "modified extension")
	assert.NotContains(t, out, "RAN")

	require.NoError(t, os.WriteFile(ext, piEditRepairExtensionJS, 0o644))
	code, out = run("")
	assert.Equal(t, 0, code)
	assert.Contains(t, out, "RAN")

	require.NoError(t, os.WriteFile(ext, []byte("// tampered\n"), 0o644))
	sum := sha256.Sum256(piEditRepairExtensionJS)
	hexSum := hex.EncodeToString(sum[:])
	shadowDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(shadowDir, "sha256sum"), []byte("#!/bin/sh\necho '"+hexSum+"  x'\n"), 0o755))
	code, out = run("sha256sum() { echo '" + hexSum + "  x'; }; cut() { echo '" + hexSum + "'; }; PATH=" + shellQuote(shadowDir) + ":$PATH; ")
	assert.Equal(t, piEditRepairTamperedExit, code, "shadowed sha256sum")
	assert.NotContains(t, out, "RAN")
}

func TestPiEditRepairGuard_OwnExitCode(t *testing.T) {
	t.Parallel()
	for _, other := range []int{piHooksMissingExit, piAgentTamperedExit, piManifestTamperedExit, piExtensionTamperedExit, piConfigTamperedExit} {
		assert.NotEqual(t, other, piEditRepairTamperedExit, "each runner-owned artifact has its own code so Run can name it")
	}
	guard := piEditRepairGuard("/x/" + piEditRepairExtensionFile)
	assert.Contains(t, guard, fmt.Sprintf("exit %d", piEditRepairTamperedExit))
	sum := sha256.Sum256(piEditRepairExtensionJS)
	assert.Contains(t, guard, hex.EncodeToString(sum[:]), "the guard pins the embedded copy's hash")
}

func TestPiAgentExtensionDigests(t *testing.T) {
	t.Parallel()
	hooksSum := sha256.Sum256(piHooksExtensionJS)
	editSum := sha256.Sum256(piEditRepairExtensionJS)
	assert.Nil(t, piAgentExtensionDigests("/c/h.js", false, "/c/e.js", false))
	assert.Equal(t, map[string]string{"/c/e.js": hex.EncodeToString(editSum[:])},
		piAgentExtensionDigests("/c/h.js", false, "/c/e.js", true), "the edit repair is re-checked even with hooks off")
	assert.Equal(t, map[string]string{"/c/h.js": hex.EncodeToString(hooksSum[:])},
		piAgentExtensionDigests("/c/h.js", true, "/c/e.js", false), "a sub-agent whose tools: omits edit still gets its hook digest with security on")
	assert.Equal(t, map[string]string{"/c/h.js": hex.EncodeToString(hooksSum[:]), "/c/e.js": hex.EncodeToString(editSum[:])},
		piAgentExtensionDigests("/c/h.js", true, "/c/e.js", true))
}

// TestPiRuntimeRun_TamperedEditRepairExtensionFailsClosed: the guard's code
// reaches Run, which names the extension rather than another artifact.
func TestPiRuntimeRun_TamperedEditRepairExtensionFailsClosed(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
	forgetPiManifestHash(t, "sb")
	work := t.TempDir()
	store := filepath.Join(work, "store")
	fakeOpenshellPi(t, filepath.Join(work, "openshell.log"), store, "/dev/null")
	// No tools: frontmatter means the default set, which has edit.
	require.NoError(t, PiRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "sb", agentPath: writeAgentFile(t, "---\nname: review\nmodel: opus\n---\nReview the PR."), agentName: "review",
	}))
	binDir := t.TempDir()
	script := `#!/bin/sh
if [ "$2" = "exec" ]; then
  for last; do :; done
  case "$last" in
    cat\ *) f=$(printf '%s' "${last#cat }" | tr -d "'" | tr '/' '_'); cat '` + store + `'/"$f"; exit $? ;;
    *"exit 93"*) echo 'fullsend: pi edit-repair extension missing or modified' >&2; exit 93 ;;
  esac
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	exit, err := PiRuntime{}.Run(context.Background(), RunParams{
		SandboxName: "sb", RepoDir: "/r", Timeout: 30 * time.Second,
		OnEvent: func(AgentEvent) {},
	}, ui.New(os.Stderr), time.Now(), &RunMetrics{})
	assert.Equal(t, piEditRepairTamperedExit, exit)
	require.ErrorContains(t, err, "edit-repair extension fullsend-edit-repair.js missing or modified")
	assert.NotContains(t, err.Error(), "Agent extension")
}

// TestPiRuntimeRun_Exit93WithoutTheGateIsNotRelabelled: 93 is claimed only
// when the gate is on. An agent whose tools omit edit never gets the guard
// written, so a 93 from inside the sandbox is the agent's own exit code and
// must reach the caller unchanged rather than be reported as tampering.
func TestPiRuntimeRun_Exit93WithoutTheGateIsNotRelabelled(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
	forgetPiManifestHash(t, "sb")
	work := t.TempDir()
	store := filepath.Join(work, "store")
	fakeOpenshellPi(t, filepath.Join(work, "openshell.log"), store, "/dev/null")
	require.NoError(t, PiRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "sb",
		agentPath:   writeAgentFile(t, "---\nname: triage\nmodel: opus\ntools: Read, Grep\n---\nTriage the issue."),
		agentName:   "triage",
	}))
	binDir := t.TempDir()
	// Every pi invocation exits 93, as an agent-run command could.
	script := `#!/bin/sh
if [ "$2" = "exec" ]; then
  for last; do :; done
  case "$last" in
    cat\ *) f=$(printf '%s' "${last#cat }" | tr -d "'" | tr '/' '_'); cat '` + store + `'/"$f"; exit $? ;;
    *"--print --mode json"*) exit 93 ;;
  esac
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	exit, err := PiRuntime{}.Run(context.Background(), RunParams{
		SandboxName: "sb", RepoDir: "/r", Timeout: 30 * time.Second,
		OnEvent: func(AgentEvent) {},
	}, ui.New(os.Stderr), time.Now(), &RunMetrics{})
	assert.Equal(t, piEditRepairTamperedExit, exit, "the agent's own exit code passes through")
	if err != nil {
		assert.NotContains(t, err.Error(), "edit-repair extension", "no guard was written, so nothing to blame on it")
	}
}
