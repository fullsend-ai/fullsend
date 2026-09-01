package harness

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/pluginformat"
)

func TestExtensionSpec_UnmarshalStringForm(t *testing.T) {
	t.Parallel()
	var h Harness
	require.NoError(t, yaml.Unmarshal([]byte(`
agent: agents/code.md
role: code
extensions:
  - extensions/go-diagnostics
`), &h))
	require.Len(t, h.Extensions, 1)
	assert.Equal(t, "extensions/go-diagnostics", h.Extensions[0].Path)
	assert.Nil(t, h.Extensions[0].Args)
	assert.Nil(t, h.Extensions[0].Env)
	assert.Equal(t, "go-diagnostics", h.Extensions[0].Name())
}

func TestExtensionSpec_UnmarshalObjectForm(t *testing.T) {
	t.Parallel()
	var h Harness
	require.NoError(t, yaml.Unmarshal([]byte(`
agent: agents/code.md
role: code
extensions:
  - extensions/go-diagnostics
  - path: extensions/pi-fff
    args: ["--fff-mode", "override"]
    env:
      FFF_MULTIGREP: "1"
`), &h))
	require.Len(t, h.Extensions, 2)
	assert.Equal(t, "extensions/pi-fff", h.Extensions[1].Path)
	assert.Equal(t, []string{"--fff-mode", "override"}, h.Extensions[1].Args)
	assert.Equal(t, map[string]string{"FFF_MULTIGREP": "1"}, h.Extensions[1].Env)
}

func TestExtensionSpec_UnmarshalRejectsBadShapes(t *testing.T) {
	t.Parallel()
	for name, doc := range map[string]string{
		"unknown key":      "extensions:\n  - path: extensions/x\n    arg: [--x]\n",
		"missing path":     "extensions:\n  - args: [--x]\n",
		"args not a list":  "extensions:\n  - path: extensions/x\n    args: --x\n",
		"env not a map":    "extensions:\n  - path: extensions/x\n    env: [A=1]\n",
		"sequence entry":   "extensions:\n  - [extensions/x]\n",
		"path not scalar":  "extensions:\n  - path: [a]\n",
		"env value nested": "extensions:\n  - path: extensions/x\n    env:\n      A: {b: 1}\n",
	} {
		t.Run(name, func(t *testing.T) {
			var h Harness
			err := yaml.Unmarshal([]byte(doc), &h)
			require.Error(t, err, doc)
			assert.Contains(t, err.Error(), "extension")
		})
	}
}

func TestExtensionSpec_MarshalRoundTrip(t *testing.T) {
	t.Parallel()
	in := Harness{Extensions: []ExtensionSpec{
		{Path: "extensions/plain"},
		{Path: "extensions/flagged", Args: []string{"--fff-mode", "override"}, Env: map[string]string{"FFF_MULTIGREP": "1"}},
	}}
	out, err := yaml.Marshal(in)
	require.NoError(t, err)
	assert.Contains(t, string(out), "- extensions/plain\n", "string form round-trips as a plain string")
	assert.Contains(t, string(out), "path: extensions/flagged")

	var back Harness
	require.NoError(t, yaml.Unmarshal(out, &back))
	assert.Equal(t, in.Extensions, back.Extensions)
}

func validExtHarness(exts ...ExtensionSpec) *Harness {
	return &Harness{Agent: "agents/code.md", Role: "code", Extensions: exts}
}

func TestValidate_ExtensionsValid(t *testing.T) {
	t.Parallel()
	h := validExtHarness(
		ExtensionSpec{Path: "extensions/go-diagnostics"},
		ExtensionSpec{Path: "extensions/pi_fff-2", Args: []string{"--fff-mode", "override"}, Env: map[string]string{"FFF_MULTIGREP": "1", "X_Y9": "v"}},
		// Already resolved by compose/ResolveRelativeTo: absolute paths are
		// only basename-checked, like skill overrides and providers.
		ExtensionSpec{Path: "/cache/abc/content/vendored-ext"},
	)
	require.NoError(t, h.Validate())
}

func TestValidate_ExtensionsRejected(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		spec ExtensionSpec
		want string
	}{
		{"empty path", ExtensionSpec{}, "extensions[0]: path is required"},
		{"url", ExtensionSpec{Path: "https://github.com/org/repo/tree/main/ext"}, "must be a path inside the harness repository, not a URL"},
		{"npm source", ExtensionSpec{Path: "npm:pi-fff"}, "must be a path inside the harness repository, not an npm:/git:/ssh: source"},
		{"git source", ExtensionSpec{Path: "git:github.com/org/ext"}, "npm:/git:/ssh: source"},
		{"ssh source", ExtensionSpec{Path: "ssh://git@github.com/org/ext"}, "npm:/git:/ssh: source"},
		{"traversal", ExtensionSpec{Path: "../shared/ext"}, "must not contain path traversal segments"},
		{"traversal inside", ExtensionSpec{Path: "extensions/../../ext"}, "must not contain path traversal segments"},
		{"bad basename", ExtensionSpec{Path: "extensions/my ext"}, "contains invalid characters"},
		{"bad basename abs", ExtensionSpec{Path: "/tmp/bad;name"}, "contains invalid characters"},
		{"null byte", ExtensionSpec{Path: "extensions/a\x00b"}, "must not contain null bytes"},
		{"arg newline", ExtensionSpec{Path: "extensions/x", Args: []string{"--a\nb"}}, "args[0] must not contain newlines"},
		{"arg empty", ExtensionSpec{Path: "extensions/x", Args: []string{""}}, "args[0] must be non-empty"},
		{"arg first not a flag", ExtensionSpec{Path: "extensions/x", Args: []string{"override"}}, `args[0] "override" must be a --flag`},
		// pi parses every element positionally, so a later element that
		// looks like an option is one.
		{"arg single dash", ExtensionSpec{Path: "extensions/x", Args: []string{"--x", "-e", "/sandbox/workspace/.pi/evil.js"}}, `args[1] "-e" must be --flag or --flag=value`},
		{"arg bare dash", ExtensionSpec{Path: "extensions/x", Args: []string{"--x", "-"}}, `args[1] "-" must be --flag or --flag=value`},
		{"arg bare double dash", ExtensionSpec{Path: "extensions/x", Args: []string{"--x", "--"}}, `args[1] "--" must be --flag or --flag=value`},
		{"arg pi option approve", ExtensionSpec{Path: "extensions/x", Args: []string{"--x", "--approve"}}, `args[1] "--approve" is one of pi's own options`},
		{"arg pi option extension", ExtensionSpec{Path: "extensions/x", Args: []string{"--extension", "/tmp/e.js"}}, `args[0] "--extension" is one of pi's own options`},
		{"arg pi option with value", ExtensionSpec{Path: "extensions/x", Args: []string{"--x", "--model=evil"}}, `args[1] "--model" is one of pi's own options`},
		{"arg value at-prefixed", ExtensionSpec{Path: "extensions/x", Args: []string{"--x", "@/etc/passwd"}}, `args[1] "@/etc/passwd" must not start with '@'`},
		{"env key lowercase", ExtensionSpec{Path: "extensions/x", Env: map[string]string{"fff_mode": "1"}}, `env key "fff_mode" must match ^[A-Z_][A-Z0-9_]*$`},
		{"env key digit first", ExtensionSpec{Path: "extensions/x", Env: map[string]string{"1X": "1"}}, `env key "1X" must match`},
		{"env value newline", ExtensionSpec{Path: "extensions/x", Env: map[string]string{"A": "1\n2"}}, `env["A"] must not contain newlines`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validExtHarness(tc.spec).Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "extensions[0]")
			assert.Contains(t, err.Error(), tc.want)
		})
	}

	// The index in the message names the offending entry.
	err := validExtHarness(ExtensionSpec{Path: "extensions/ok"}, ExtensionSpec{Path: "npm:x"}).Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "extensions[1]")
}

// TestValidate_ExtensionsReservedEnv pins the deny-list. Extension env is
// exported last and inherited by pi and by every hook script it spawns, so
// the list has to cover the interpreter environment and every
// credential-shaped family, not just the five names the runtime pins.
func TestValidate_ExtensionsReservedEnv(t *testing.T) {
	t.Parallel()
	reserved := []string{
		// Shell and interpreter environment.
		"PATH", "HOME", "TMPDIR", "ENV", "BASH_ENV", "SHELL",
		"LD_PRELOAD", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES",
		"PYTHONPATH", "PYTHONSTARTUP", "NODE_OPTIONS", "NODE_PATH",
		"SSL_CERT_FILE", "REQUESTS_CA_BUNDLE_TOKEN",
		// Proxies and credential shapes, whatever the vendor.
		"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY",
		"SOME_VENDOR_API_KEY", "GH_TOKEN", "MY_SECRET_VALUE", "CLIENT_SECRET",
		// The runner, pi and the providers.
		"PI_OFFLINE", "PI_CODING_AGENT_DIR", "PI_CODING_AGENT_SESSION_DIR",
		"PI_TELEMETRY", "PI_ANYTHING_ELSE",
		"FULLSEND_RUNTIME", "FULLSEND_PI_MANIFEST",
		"GOOGLE_CLOUD_PROJECT", "GCLOUD_PROJECT", "CLOUD_ML_REGION",
		"ANTHROPIC_API_KEY", "XAI_API_KEY", "OPENAI_BASE_URL",
		"AZURE_OPENAI_API_KEY", "AWS_ACCESS_KEY_ID",
		// Loader and trust-store steering the interpreter families above
		// do not cover: pi loads every -e module through jiti, whose
		// transpile cache is a code-execution path of its own, and the
		// hook scripts pi spawns are python/git/curl.
		"JITI_FS_CACHE", "JITI_CACHE", "TIRITH_POLICY", "IFS", "HOSTALIASES",
		"OPENSSL_CONF", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE",
		"GOPROXY", "GOFLAGS", "CLOUDSDK_CONFIG", "CLOUDSDK_CORE_PROJECT",
		"GIT_SSL_CAINFO", "GIT_SSL_NO_VERIFY", "GIT_CONFIG", "GIT_CONFIG_GLOBAL",
		// Everything internal/sandbox reservedCredentialKeys refuses as a
		// provider credential key must be refused here too: extension env
		// reaches the same processes by a different door.
		"GIT_SSH_COMMAND", "GIT_PROXY_COMMAND", "GIT_ASKPASS", "GIT_EXEC_PATH",
		"GIT_TEMPLATE_DIR", "GIT_ANY_FUTURE_NAME",
		"CDPATH", "PROMPT_COMMAND", "JAVA_TOOL_OPTIONS", "RUBYOPT", "PERL5OPT",
		// SSLKEYLOGFILE has no underscore, so the SSL_ prefix misses it —
		// and it writes the session keys of every TLS connection the hook
		// scripts make to a file the agent chooses.
		"SSLKEYLOGFILE",
	}
	for _, key := range reserved {
		t.Run(key, func(t *testing.T) {
			err := validExtHarness(ExtensionSpec{Path: "extensions/x", Env: map[string]string{key: "v"}}).Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), `env key "`+key+`" is reserved`)
		})
	}

	// An extension's own settings still go through.
	for _, key := range []string{"FFF_MULTIGREP", "GO_DIAG_LEVEL", "X_Y9", "DIAGNOSTICS_MODE"} {
		t.Run("allowed/"+key, func(t *testing.T) {
			require.NoError(t, validExtHarness(ExtensionSpec{Path: "extensions/x", Env: map[string]string{key: "v"}}).Validate())
		})
	}
}

// TestValidate_ExtensionsDuplicates covers the base+child collision: two
// entries that upload as the same sandbox name would silently replace one
// another, so harness load rejects them.
func TestValidate_ExtensionsDuplicates(t *testing.T) {
	t.Parallel()
	err := validExtHarness(
		ExtensionSpec{Path: "extensions/go-diagnostics"},
		ExtensionSpec{Path: "extensions/go-diagnostics"},
	).Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "extensions[1]")
	assert.Contains(t, err.Error(), "already listed as extensions[0]")

	// Base contributes vendor/go-diagnostics, the child extensions/go-diagnostics.
	err = validExtHarness(
		ExtensionSpec{Path: "vendor/go-diagnostics"},
		ExtensionSpec{Path: "extensions/go-diagnostics"},
	).Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "extensions[1]")
	assert.Contains(t, err.Error(), `both load as extension "go-diagnostics"`)

	require.NoError(t, validExtHarness(
		ExtensionSpec{Path: "extensions/go-diagnostics"},
		ExtensionSpec{Path: "extensions/pi-fff"},
	).Validate())
}

func TestResolveRelativeTo_Extensions(t *testing.T) {
	t.Parallel()
	h := &Harness{Agent: "agents/test.md", Extensions: []ExtensionSpec{{Path: "extensions/x", Args: []string{"--a"}}}}
	require.NoError(t, h.ResolveRelativeTo("/base/dir"))
	assert.Equal(t, "/base/dir/extensions/x", h.Extensions[0].Path)
	assert.Equal(t, []string{"--a"}, h.Extensions[0].Args, "args survive resolution")

	h = &Harness{Agent: "agents/test.md", Extensions: []ExtensionSpec{{Path: "../outside"}}}
	err := h.ResolveRelativeTo("/base/dir")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "extensions[0]")
}

func TestExtensionPaths(t *testing.T) {
	t.Parallel()
	assert.Nil(t, ExtensionPaths(nil))
	assert.Equal(t, []string{"a", "b"}, ExtensionPaths([]ExtensionSpec{{Path: "a"}, {Path: "b"}}))
}

// TestValidateFilesExist_ExtensionDirRules covers the harness half of the
// directory check: the stat rules it owns, and that a directory
// pluginformat refuses is reported against the offending entry. The format
// rule itself is pinned in internal/pluginformat.
func TestValidateFilesExist_ExtensionDirRules(t *testing.T) {
	t.Parallel()
	agent := filepath.Join(t.TempDir(), "code.md")
	require.NoError(t, os.WriteFile(agent, []byte("# agent"), 0o644))
	extDir := func(t *testing.T, files map[string]string) string {
		t.Helper()
		dir := filepath.Join(t.TempDir(), "my-ext")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		for name, content := range files {
			require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
		}
		return dir
	}

	t.Run("loadable", func(t *testing.T) {
		h := &Harness{Agent: agent, Extensions: []ExtensionSpec{{Path: extDir(t, map[string]string{"index.js": "//"})}}}
		require.NoError(t, h.ValidateFilesExist())
	})

	t.Run("not loadable", func(t *testing.T) {
		dir := extDir(t, map[string]string{"README.md": "#"})
		h := &Harness{Agent: agent, Extensions: []ExtensionSpec{{Path: dir}}}
		err := h.ValidateFilesExist()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "extensions[0] \""+dir+"\"")
		assert.Contains(t, err.Error(), "not a pi extension")
	})

	t.Run("Claude plugin under extensions", func(t *testing.T) {
		dir := extDir(t, map[string]string{"plugin.json": `{"name":"x"}`})
		h := &Harness{Agent: agent, Extensions: []ExtensionSpec{{Path: dir}}}
		err := h.ValidateFilesExist()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "it is a Claude plugin")
	})

	t.Run("missing", func(t *testing.T) {
		h := &Harness{Agent: agent, Extensions: []ExtensionSpec{{Path: filepath.Join(t.TempDir(), "missing")}}}
		err := h.ValidateFilesExist()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "extensions[0]")
	})

	t.Run("file instead of a directory", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "ext.js")
		require.NoError(t, os.WriteFile(file, []byte("//"), 0o644))
		h := &Harness{Agent: agent, Extensions: []ExtensionSpec{{Path: file}}}
		err := h.ValidateFilesExist()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be a directory")
	})
}

// TestValidate_ExtensionsReservedNames covers the sandbox names the runner
// owns. piResolveRunExtensions refuses them at bootstrap, but a harness
// author should learn at load which entry is the problem.
func TestValidate_ExtensionsReservedNames(t *testing.T) {
	t.Parallel()
	for _, name := range pluginformat.PiReservedExtensionNames {
		t.Run(name, func(t *testing.T) {
			err := validExtHarness(ExtensionSpec{Path: "extensions/" + name}).Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), `"`+name+`" is a name the runner owns`)
			assert.Contains(t, err.Error(), "extensions[0]")
		})
	}
	require.NoError(t, validExtHarness(ExtensionSpec{Path: "extensions/fullsend-hooks-extra"}).Validate())
}
