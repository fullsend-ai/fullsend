package runtime

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/ui"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPiExactNativeTools(t *testing.T) {
	t.Parallel()
	tools, invalid := piToolsFor([]string{"inspect_record", "Read", "inspect_record", "native*", "bad,name", "bad/name"})
	assert.Equal(t, []string{"inspect_record", "read"}, tools)
	assert.Equal(t, []string{"native*", "bad,name", "bad/name"}, invalid)
	assert.Equal(t, []string{"inspect_record"}, piNativeToolsFor([]string{"Bash", "Skill", "Agent", "inspect_record", "inspect_record", "native*"}))
	child, unsupported := piPersonaTools([]string{"Read", "inspect_record"})
	assert.Equal(t, []string{"read", "inspect_record"}, child)
	assert.Empty(t, unsupported)
}

func TestPiToolSelectorBootstrap(t *testing.T) {
	work := t.TempDir()
	store := filepath.Join(work, "store")
	fakeOpenshellPi(t, filepath.Join(work, "openshell.log"), store, "/dev/null")
	input := bootstrapInput{sandboxName: "native-tools-test", agentName: "assess", agentPath: writeAgentFile(t, "---\nname: assess\ntools: [inspect_record]\n---\nAssess the admitted target.")}
	require.NoError(t, PiRuntime{}.Bootstrap(input))
	var manifest piManifest
	require.NoError(t, json.Unmarshal(storedUpload(t, store, PiRuntime{}.piManifestPath()), &manifest))
	assert.True(t, manifest.StrictTools)
	assert.Equal(t, []string{"inspect_record"}, manifest.NativeTools)
	assert.Equal(t, []string{"inspect_record"}, manifest.Tools)
	assert.Equal(t, piToolSelectionExtensionJS, storedUpload(t, store, PiRuntime{}.ConfigDir()+"/"+piToolSelectionExtensionFile))
	input.agentPath = writeAgentFile(t, "---\nname: assess\ntools: ['native*']\n---\nAssess.")
	require.ErrorContains(t, PiRuntime{}.Bootstrap(input), "exact names")
}

func TestPiToolSelectorGuard(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "selector.js")
	guard := piToolSelectionGuard(file)
	for _, bytes := range [][]byte{[]byte("modified"), piToolSelectionExtensionJS} {
		require.NoError(t, os.WriteFile(file, bytes, 0o600))
		err := exec.Command("sh", "-c", guard).Run()
		if string(bytes) == string(piToolSelectionExtensionJS) {
			require.NoError(t, err)
		} else {
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit)
			assert.Equal(t, piToolSelectionExit, exit.ExitCode())
		}
	}
	require.NoError(t, os.Remove(file))
	require.Error(t, exec.Command("sh", "-c", guard).Run())
}

func TestBuildPiRunCommand_StrictNativeSelection(t *testing.T) {
	// Activation derives from the explicit list, not an editable boolean.
	m := &piManifest{NativeTools: []string{"inspect_record"}, Tools: []string{"inspect_record"}}
	ext := piManifestExtension{Name: "native", Path: "/sandbox/pi-config/extensions/native"}
	cmd := buildPiRunCommand(piTestParams(), m, []piManifestExtension{ext}, "manifest-digest")
	assert.Contains(t, cmd, "--tools 'inspect_record'")
	assert.Contains(t, cmd, piToolSelectionGuard(PiRuntime{}.ConfigDir()+"/"+piToolSelectionExtensionFile))
	assert.Greater(t, strings.Index(cmd, "-e '/sandbox/pi-config/fullsend-tools.js'"), strings.Index(cmd, "-e '/sandbox/pi-config/extensions/native'"))
	assert.NotContains(t, cmd, "--no-builtin-tools")
}

func TestPiRuntimeRun_TamperedToolSelectorFailsClosed(t *testing.T) {
	t.Setenv("FULLSEND_PI_MODEL", "")
	forgetPiManifestHash(t, "sb")
	work := t.TempDir()
	store := filepath.Join(work, "store")
	fakeOpenshellPi(t, filepath.Join(work, "openshell.log"), store, "/dev/null")
	// A selector failure must end the run rather than trigger model fallback.
	require.NoError(t, PiRuntime{}.Bootstrap(bootstrapInput{
		sandboxName: "sb", agentPath: writeAgentFile(t, "---\nname: review\nmodel: opus\ntools: [inspect_record]\n---\nReview the PR."), agentName: "review",
	}))
	binDir := t.TempDir()
	script := `#!/bin/sh
if [ "$2" = "exec" ]; then
  for last; do :; done
  case "$last" in
    cat\ *) f=$(printf '%s' "${last#cat }" | tr -d "'" | tr '/' '_'); cat '` + store + `'/"$f"; exit $? ;;
    *"exit 92"*) echo 'fullsend: pi tool selector missing or modified' >&2; exit 92 ;;
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
	assert.Equal(t, piToolSelectionExit, exit)
	require.ErrorContains(t, err, "strict tool selection failed")
	assert.NotContains(t, err.Error(), "Agent extension")
}
