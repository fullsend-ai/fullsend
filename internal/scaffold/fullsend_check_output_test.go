package scaffold

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const outputCheckJSONSchemaStub = `class ValidationError(Exception):
    def __init__(self, message):
        super().__init__(message)
        self.message = message
        self.path = ["ok"]
        self.context = []

class SchemaError(Exception):
    def __init__(self, message):
        super().__init__(message)
        self.message = message

def validate(instance, schema):
    if not isinstance(schema, dict) or schema.get("type") != "object":
        raise SchemaError("schema type must be object")
    for name in schema.get("required", []):
        if name not in instance:
            raise ValidationError("'" + name + "' is a required property")
`

const outputCheckPythonWrapper = `#!/bin/sh
printf '%s\n' "$FAKE_PYTHON_MODE" >> "$FAKE_PYTHON_LOG"
case "$FAKE_PYTHON_MODE" in
  mounted)
    [ "${PYTHONHOME-}" = "$FAKE_MOUNTED_HOME" ] || exit 41
    [ "${PYTHONPATH-}" = "$FAKE_MOUNTED_PATH" ] || exit 42
    [ "${PYTHONNOUSERSITE-}" = 1 ] || exit 43
    if [ "${FAKE_MOUNT_BROKEN:-0}" = 1 ]; then exit 44; fi
    ;;
  legacy)
    [ -z "${PYTHONHOME+x}" ] || exit 45
    [ -z "${PYTHONPATH+x}" ] || exit 46
    [ -z "${PYTHONUSERBASE+x}" ] || exit 47
    [ -z "${PYTHONSTARTUP+x}" ] || exit 48
    [ -z "${PYTHONINSPECT+x}" ] || exit 49
    ;;
esac
unset PYTHONHOME PYTHONPATH PYTHONNOUSERSITE PYTHONUSERBASE PYTHONSTARTUP PYTHONINSPECT
PYTHONPATH="$FAKE_JSONSCHEMA_SITE" exec "$FAKE_REAL_PYTHON" "$@"
`

type outputCheckFixture struct {
	root        string
	script      string
	bin         string
	mountRoot   string
	site        string
	log         string
	realPython  string
	schema      string
	validOutput string
}

func newOutputCheckFixture(t *testing.T, installSchema, mountedPresent bool) outputCheckFixture {
	t.Helper()
	_, testFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	realPython, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required for output validation fixtures")
	}

	root := t.TempDir()
	contents, err := os.ReadFile(filepath.Join(filepath.Dir(testFile), "fullsend-repo", "scripts", "fullsend-check-output"))
	require.NoError(t, err)
	mountRoot := filepath.Join(root, "mount", "opt", "fullsend")
	script := filepath.Join(root, "fullsend-check-output")
	// Relocate only the fixed production mount path inside this private test
	// copy so tests never write into /opt or require a production override.
	contents = []byte(strings.ReplaceAll(string(contents), "/opt/fullsend", mountRoot))
	require.NoError(t, os.WriteFile(script, contents, 0o700))

	bin := filepath.Join(root, "bin")
	mountedBin := filepath.Join(mountRoot, "usr", "bin")
	site := filepath.Join(root, "site")
	dirs := []string{bin, site}
	if mountedPresent {
		dirs = append(dirs, mountedBin)
	}
	for _, dir := range dirs {
		require.NoError(t, os.MkdirAll(dir, 0o700))
	}
	wrapper := []byte(outputCheckPythonWrapper)
	require.NoError(t, os.WriteFile(filepath.Join(bin, "python3"), wrapper, 0o700))
	if mountedPresent {
		require.NoError(t, os.WriteFile(filepath.Join(mountedBin, "python3"), wrapper, 0o700))
	}
	if installSchema {
		pkg := filepath.Join(site, "jsonschema")
		require.NoError(t, os.MkdirAll(pkg, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(pkg, "__init__.py"), []byte(outputCheckJSONSchemaStub), 0o600))
	} else {
		pkg := filepath.Join(site, "jsonschema")
		require.NoError(t, os.MkdirAll(pkg, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(pkg, "__init__.py"), []byte("raise ImportError('jsonschema is not installed')\n"), 0o600))
	}

	schema := filepath.Join(root, "schema.json")
	validOutput := filepath.Join(root, "agent-result.json")
	require.NoError(t, os.WriteFile(schema, []byte(`{"type":"object","required":["ok"]}`), 0o600))
	require.NoError(t, os.WriteFile(validOutput, []byte(`{"ok":true}`), 0o600))
	return outputCheckFixture{
		root: root, script: script, bin: bin, mountRoot: mountRoot, site: site,
		log: filepath.Join(root, "python.log"), realPython: realPython,
		schema: schema, validOutput: validOutput,
	}
}

func (f outputCheckFixture) run(t *testing.T, mounted, brokenMounted bool, candidate string) (string, error, []string) {
	t.Helper()
	require.NoError(t, os.WriteFile(f.log, nil, 0o600))
	mode := "legacy"
	if mounted {
		mode = "mounted"
	}
	cmd := exec.Command("bash", f.script, candidate)
	cmd.Env = []string{
		"PATH=" + f.bin + ":/usr/bin:/bin",
		"FULLSEND_OUTPUT_SCHEMA=" + f.schema,
		"FAKE_PYTHON_MODE=" + mode,
		"FAKE_PYTHON_LOG=" + f.log,
		"FAKE_REAL_PYTHON=" + f.realPython,
		"FAKE_JSONSCHEMA_SITE=" + f.site,
		"FAKE_MOUNTED_HOME=" + filepath.Join(f.mountRoot, "usr"),
		"FAKE_MOUNTED_PATH=" + filepath.Join(f.mountRoot, "usr", "local", "share", "fullsend", "python", "site-packages"),
		"FAKE_MOUNT_BROKEN=" + map[bool]string{true: "1", false: "0"}[brokenMounted],
		"PYTHONHOME=/untrusted/python-home",
		"PYTHONPATH=/untrusted/python-path",
		"PYTHONUSERBASE=/untrusted/user-base",
		"PYTHONSTARTUP=/untrusted/startup",
		"PYTHONINSPECT=1",
	}
	output, err := cmd.CombinedOutput()

	rawLog, readErr := os.ReadFile(f.log)
	require.NoError(t, readErr)
	var calls []string
	for _, call := range strings.Split(strings.TrimSpace(string(rawLog)), "\n") {
		if call != "" {
			calls = append(calls, call)
		}
	}
	return string(output), err, calls
}

func TestFullsendCheckOutput_MountedAndLegacyPython(t *testing.T) {
	for _, mounted := range []bool{true, false} {
		mode := "legacy"
		if mounted {
			mode = "mounted"
		}
		t.Run(mode, func(t *testing.T) {
			f := newOutputCheckFixture(t, true, mounted)

			t.Run("valid output", func(t *testing.T) {
				out, err, calls := f.run(t, mounted, false, f.validOutput)
				require.NoError(t, err, out)
				assert.Contains(t, out, "PASS: output validated against schema")
				assert.NotEmpty(t, calls)
				for _, call := range calls {
					assert.Equal(t, mode, call)
				}
			})

			t.Run("schema-invalid output", func(t *testing.T) {
				candidate := filepath.Join(f.root, "agent-result.json")
				require.NoError(t, os.WriteFile(candidate, []byte(`{"wrong":true}`), 0o600))
				out, err, _ := f.run(t, mounted, false, candidate)
				require.Error(t, err)
				assert.Contains(t, out, "FAIL: 'ok' is a required property")
			})

			t.Run("malformed JSON", func(t *testing.T) {
				candidate := filepath.Join(f.root, "agent-result.json")
				require.NoError(t, os.WriteFile(candidate, []byte("{"), 0o600))
				out, err, _ := f.run(t, mounted, false, candidate)
				require.Error(t, err)
				assert.Contains(t, out, "is not valid JSON")
			})

			t.Run("missing jsonschema reports selected interpreter", func(t *testing.T) {
				f := newOutputCheckFixture(t, false, mounted)
				out, err, calls := f.run(t, mounted, false, f.validOutput)
				require.Error(t, err)
				if mounted {
					assert.Contains(t, out, "mounted Fullsend Python or its jsonschema dependencies are unavailable")
				} else {
					assert.Contains(t, out, "python3 jsonschema package is not installed")
				}
				for _, call := range calls {
					assert.Equal(t, mode, call)
				}
			})

			t.Run("filename check is unchanged", func(t *testing.T) {
				candidate := filepath.Join(f.root, "wrong-name.json")
				require.NoError(t, os.WriteFile(candidate, []byte(`{"ok":true}`), 0o600))
				out, err, calls := f.run(t, mounted, false, candidate)
				require.Error(t, err)
				assert.Contains(t, out, "filename is 'wrong-name.json' but harness expects 'agent-result.json'")
				assert.Empty(t, calls, "filename validation precedes interpreter invocation")
			})
		})
	}
}

func TestFullsendCheckOutput_BrokenMountedPythonDoesNotFallback(t *testing.T) {
	f := newOutputCheckFixture(t, true, true)
	out, err, calls := f.run(t, true, true, f.validOutput)
	require.Error(t, err)
	assert.Contains(t, out, "mounted Fullsend Python or its jsonschema dependencies are unavailable")
	assert.NotContains(t, calls, "legacy", "a present but broken mounted interpreter must not fall back")
}
