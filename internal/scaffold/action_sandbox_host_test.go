package scaffold

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// actionStep mirrors just enough of a composite action step to check its
// condition and run its script in isolation.
type actionStep struct {
	Name string `yaml:"name"`
	ID   string `yaml:"id"`
	If   string `yaml:"if"`
	Run  string `yaml:"run"`
}

func loadRootActionSteps(t *testing.T) map[string]actionStep {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "action.yml"))
	require.NoError(t, err)
	var action struct {
		Runs struct {
			Steps []actionStep `yaml:"steps"`
		} `yaml:"runs"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &action))
	steps := make(map[string]actionStep, len(action.Runs.Steps))
	for _, st := range action.Runs.Steps {
		steps[st.Name] = st
	}
	return steps
}

// runDetectSandboxHost runs the "Detect sandbox host" step script with only
// pathDir on PATH and returns the outputs it wrote to GITHUB_OUTPUT. When
// endpoint is nil, OPENSHELL_GATEWAY_ENDPOINT is left unset.
func runDetectSandboxHost(t *testing.T, script string, endpoint *string, pathDir string) map[string]string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	require.NoError(t, err)

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "step.sh")
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o600))
	outputPath := filepath.Join(dir, "github_output")
	require.NoError(t, os.WriteFile(outputPath, nil, 0o600))

	env := []string{"PATH=" + pathDir, "GITHUB_OUTPUT=" + outputPath}
	if endpoint != nil {
		env = append(env, "OPENSHELL_GATEWAY_ENDPOINT="+*endpoint)
	}
	cmd := exec.Command(bash, scriptPath)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	raw, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	outputs := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			outputs[k] = v
		}
	}
	return outputs
}

func TestActionDetectSandboxHostStep(t *testing.T) {
	steps := loadRootActionSteps(t)
	detect, ok := steps["Detect sandbox host"]
	require.True(t, ok, "action.yml must have a Detect sandbox host step")
	require.Equal(t, "sandbox-host", detect.ID)
	require.NotEmpty(t, detect.Run)

	emptyPath := t.TempDir()
	withCLI := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(withCLI, "openshell"), []byte("#!/bin/sh\nexit 0\n"), 0o700))

	empty := ""
	remote := "https://gateway.example:8080"

	cases := []struct {
		name             string
		endpoint         *string
		pathDir          string
		wantLocal        string
		wantInstallShell string
	}{
		{name: "endpoint unset sets up a local sandbox host", pathDir: withCLI, wantLocal: "true", wantInstallShell: "true"},
		{name: "empty endpoint sets up a local sandbox host", endpoint: &empty, pathDir: withCLI, wantLocal: "true", wantInstallShell: "true"},
		{name: "endpoint set with preinstalled CLI skips all local setup", endpoint: &remote, pathDir: withCLI, wantLocal: "false", wantInstallShell: "false"},
		{name: "endpoint set without CLI still installs the CLI", endpoint: &remote, pathDir: emptyPath, wantLocal: "false", wantInstallShell: "true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outputs := runDetectSandboxHost(t, detect.Run, tc.endpoint, tc.pathDir)
			assert.Equal(t, tc.wantLocal, outputs["local"])
			assert.Equal(t, tc.wantInstallShell, outputs["install-openshell"])
		})
	}
}

func TestActionLocalSandboxHostStepsAreGated(t *testing.T) {
	steps := loadRootActionSteps(t)
	for _, name := range []string{"Install Podman", "Configure rootless Podman", "Configure OpenShell gateway"} {
		st, ok := steps[name]
		require.True(t, ok, "action.yml must have a %s step", name)
		assert.Equal(t, "steps.sandbox-host.outputs.local == 'true'", st.If, name)
	}

	install, ok := steps["Install OpenShell CLI"]
	require.True(t, ok, "action.yml must have an Install OpenShell CLI step")
	assert.Equal(t, "steps.sandbox-host.outputs.install-openshell == 'true'", install.If)

	run, ok := steps["Run fullsend"]
	require.True(t, ok, "action.yml must have a Run fullsend step")
	assert.NotContains(t, run.If, "sandbox-host", "Run fullsend must run whichever sandbox host the runner uses")
}

// runInstallValidationDeps runs the "Install validation dependencies" step
// script with a fake python3 on PATH. The fake runs `-c` probes with the real
// interpreter against a stub jsonschema distribution of version installed (an
// empty installed makes the import fail), refuses `-m pip install`
// without --break-system-packages when externallyManaged is true, and fails
// every pip install when pipBroken is true. It returns the pip invocations and
// the script's error.
func runInstallValidationDeps(t *testing.T, script, installed string, externallyManaged, pipBroken bool) ([]string, error) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	require.NoError(t, err)
	realPython, err := exec.LookPath("python3")
	require.NoError(t, err)

	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	require.NoError(t, os.Mkdir(binDir, 0o700))
	logPath := filepath.Join(dir, "pip.log")
	require.NoError(t, os.WriteFile(logPath, nil, 0o600))

	sitePath := filepath.Join(dir, "site")
	require.NoError(t, os.Mkdir(sitePath, 0o700))
	// The stub package shadows any jsonschema on the host interpreter: it
	// imports when a version is given and raises ImportError otherwise.
	pkgDir := filepath.Join(sitePath, "jsonschema")
	require.NoError(t, os.Mkdir(pkgDir, 0o700))
	initSrc := ""
	if installed == "" {
		initSrc = "raise ImportError('jsonschema is not installed')\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "__init__.py"), []byte(initSrc), 0o600))
	if installed != "" {
		distDir := filepath.Join(sitePath, "jsonschema-"+installed+".dist-info")
		require.NoError(t, os.Mkdir(distDir, 0o700))
		meta := "Metadata-Version: 2.1\nName: jsonschema\nVersion: " + installed + "\n"
		require.NoError(t, os.WriteFile(filepath.Join(distDir, "METADATA"), []byte(meta), 0o600))
	}

	fake := `#!/bin/sh
if [ "$1" = "-c" ]; then
  PYTHONPATH="$FAKE_SITE" exec "$FAKE_REAL_PYTHON" "$@"
fi
if [ "$1" = "-m" ] && [ "$2" = "pip" ]; then
  echo "$*" >> "$FAKE_PIP_LOG"
  [ "$FAKE_PIP_BROKEN" = "1" ] && exit 1
  if [ "$FAKE_EXTERNALLY_MANAGED" = "1" ]; then
    case "$*" in
      *--break-system-packages*) exit 0 ;;
      *) echo "error: externally-managed-environment" >&2; exit 1 ;;
    esac
  fi
  exit 0
fi
exit 2
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "python3"), []byte(fake), 0o700))
	scriptPath := filepath.Join(dir, "step.sh")
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o600))

	flag := func(b bool) string {
		if b {
			return "1"
		}
		return "0"
	}
	cmd := exec.Command(bash, scriptPath)
	cmd.Env = []string{
		"PATH=" + binDir,
		"FAKE_PIP_LOG=" + logPath,
		"FAKE_SITE=" + sitePath,
		"FAKE_REAL_PYTHON=" + realPython,
		"FAKE_EXTERNALLY_MANAGED=" + flag(externallyManaged),
		"FAKE_PIP_BROKEN=" + flag(pipBroken),
	}
	_, runErr := cmd.CombinedOutput()

	raw, err := os.ReadFile(logPath)
	require.NoError(t, err)
	var calls []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		if line != "" {
			calls = append(calls, line)
		}
	}
	return calls, runErr
}

func TestActionInstallValidationDependenciesStep(t *testing.T) {
	steps := loadRootActionSteps(t)
	step, ok := steps["Install validation dependencies"]
	require.True(t, ok, "action.yml must have an Install validation dependencies step")
	require.NotEmpty(t, step.Run)

	t.Run("already installed at the floor skips pip", func(t *testing.T) {
		calls, err := runInstallValidationDeps(t, step.Run, "4.18.0", true, false)
		require.NoError(t, err)
		assert.Empty(t, calls)
	})

	t.Run("already installed above the floor skips pip", func(t *testing.T) {
		calls, err := runInstallValidationDeps(t, step.Run, "4.26.0", true, false)
		require.NoError(t, err)
		assert.Empty(t, calls)
	})

	t.Run("importable but outdated package triggers install", func(t *testing.T) {
		calls, err := runInstallValidationDeps(t, step.Run, "4.17.3", false, false)
		require.NoError(t, err)
		require.Len(t, calls, 1)
		assert.Contains(t, calls[0], "jsonschema>=4.18.0")
	})

	t.Run("plain install succeeds without the override flag", func(t *testing.T) {
		calls, err := runInstallValidationDeps(t, step.Run, "", false, false)
		require.NoError(t, err)
		require.Len(t, calls, 1)
		assert.NotContains(t, calls[0], "--break-system-packages")
		assert.Contains(t, calls[0], "jsonschema>=4.18.0")
	})

	t.Run("externally managed environment retries with the override flag", func(t *testing.T) {
		calls, err := runInstallValidationDeps(t, step.Run, "", true, false)
		require.NoError(t, err)
		require.Len(t, calls, 2)
		assert.NotContains(t, calls[0], "--break-system-packages")
		assert.Contains(t, calls[1], "--break-system-packages")
		assert.Contains(t, calls[1], "jsonschema>=4.18.0")
	})

	t.Run("a pip that always fails fails the step", func(t *testing.T) {
		_, err := runInstallValidationDeps(t, step.Run, "", false, true)
		require.Error(t, err)
	})
}
