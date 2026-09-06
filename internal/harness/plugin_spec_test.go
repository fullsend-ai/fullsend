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

func TestPluginSpec_UnmarshalStringForm(t *testing.T) {
	t.Parallel()
	var h Harness
	require.NoError(t, yaml.Unmarshal([]byte(`
agent: agents/code.md
role: code
plugins:
  - plugins/gopls-lsp
`), &h))
	require.Len(t, h.Plugins, 1)
	assert.Equal(t, "plugins/gopls-lsp", h.Plugins[0].Path)
	assert.Nil(t, h.Plugins[0].Env)
	assert.Nil(t, h.Plugins[0].Pi)
	assert.Nil(t, h.Plugins[0].PiArgs())
	assert.Equal(t, "gopls-lsp", h.Plugins[0].Name())
}

func TestPluginSpec_UnmarshalObjectForm(t *testing.T) {
	t.Parallel()
	var h Harness
	require.NoError(t, yaml.Unmarshal([]byte(`
agent: agents/code.md
role: code
plugins:
  - plugins/gopls-lsp
  - path: extensions/pi-fff
    env:
      FFF_MULTIGREP: "1"
    pi:
      args: ["--fff-mode", "override"]
`), &h))
	require.Len(t, h.Plugins, 2)
	assert.Equal(t, "extensions/pi-fff", h.Plugins[1].Path)
	assert.Equal(t, map[string]string{"FFF_MULTIGREP": "1"}, h.Plugins[1].Env)
	assert.Equal(t, []string{"--fff-mode", "override"}, h.Plugins[1].PiArgs())

	// An object entry with only a path is the string form spelled out.
	var bare Harness
	require.NoError(t, yaml.Unmarshal([]byte("plugins:\n  - path: plugins/p\n"), &bare))
	require.Len(t, bare.Plugins, 1)
	assert.Nil(t, bare.Plugins[0].Pi)
}

func TestPluginSpec_UnmarshalRejectsBadShapes(t *testing.T) {
	t.Parallel()
	for name, doc := range map[string]string{
		"unknown key":         "plugins:\n  - path: plugins/x\n    environment: {A: '1'}\n",
		"args at entry level": "plugins:\n  - path: plugins/x\n    args: [--x]\n",
		"missing path":        "plugins:\n  - env: {A: '1'}\n",
		"env not a map":       "plugins:\n  - path: plugins/x\n    env: [A=1]\n",
		"sequence entry":      "plugins:\n  - [plugins/x]\n",
		"path not scalar":     "plugins:\n  - path: [a]\n",
		"env value nested":    "plugins:\n  - path: plugins/x\n    env:\n      A: {b: 1}\n",
		"pi not a map":        "plugins:\n  - path: plugins/x\n    pi: [--x]\n",
		"pi unknown key":      "plugins:\n  - path: plugins/x\n    pi:\n      flags: [--x]\n",
		"pi args not a list":  "plugins:\n  - path: plugins/x\n    pi:\n      args: --x\n",
	} {
		t.Run(name, func(t *testing.T) {
			var h Harness
			err := yaml.Unmarshal([]byte(doc), &h)
			require.Error(t, err, doc)
			assert.Contains(t, err.Error(), "plugin")
		})
	}
}

func TestPluginSpec_MarshalRoundTrip(t *testing.T) {
	t.Parallel()
	in := Harness{Plugins: []PluginSpec{
		{Path: "plugins/plain"},
		{
			Path: "extensions/flagged",
			Env:  map[string]string{"FFF_MULTIGREP": "1"},
			Pi:   &PiPluginOptions{Args: []string{"--fff-mode", "override"}},
		},
	}}
	out, err := yaml.Marshal(in)
	require.NoError(t, err)
	assert.Contains(t, string(out), "- plugins/plain\n", "string form round-trips as a plain string")
	assert.Contains(t, string(out), "path: extensions/flagged")

	var back Harness
	require.NoError(t, yaml.Unmarshal(out, &back))
	assert.Equal(t, in.Plugins, back.Plugins)
}

func validPluginHarness(plugins ...PluginSpec) *Harness {
	return &Harness{Agent: "agents/code.md", Role: "code", Plugins: plugins}
}

func TestValidate_PluginsValid(t *testing.T) {
	t.Parallel()
	h := validPluginHarness(
		PluginSpec{Path: "plugins/gopls-lsp"},
		PluginSpec{
			Path: "extensions/pi_fff-2",
			Env:  map[string]string{"FFF_MULTIGREP": "1", "X_Y9": "v"},
			Pi:   &PiPluginOptions{Args: []string{"--fff-mode", "override"}},
		},
		// Already resolved by compose/ResolveRelativeTo: absolute paths are
		// only basename-checked, like skill overrides and providers.
		PluginSpec{Path: "/cache/abc/content/vendored-ext"},
		// A pinned forge tree URL, the same sourcing rule as skills:.
		PluginSpec{Path: "https://github.com/org/repo/tree/main/plugins/remote#sha256=" + hex64},
	)
	require.NoError(t, h.Validate())
}

const hex64 = "0000000000000000000000000000000000000000000000000000000000000000"

func TestValidate_PluginsRejected(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		spec PluginSpec
		want string
	}{
		{"empty path", PluginSpec{}, "plugins[0]: path is required"},
		{"url without hash", PluginSpec{Path: "https://github.com/org/repo/tree/main/ext"}, "URL must include #sha256=... integrity hash"},
		{"npm source", PluginSpec{Path: "npm:pi-fff"}, "must be a path inside the harness repository or a pinned forge URL, not an npm:/git:/ssh: source"},
		{"git source", PluginSpec{Path: "git:github.com/org/ext"}, "npm:/git:/ssh: source"},
		{"ssh source", PluginSpec{Path: "ssh://git@github.com/org/ext"}, "npm:/git:/ssh: source"},
		{"traversal", PluginSpec{Path: "../shared/ext"}, "must not contain path traversal segments"},
		{"traversal inside", PluginSpec{Path: "plugins/../../ext"}, "must not contain path traversal segments"},
		{"bad basename", PluginSpec{Path: "plugins/my ext"}, "contains invalid characters"},
		{"bad basename abs", PluginSpec{Path: "/tmp/bad;name"}, "contains invalid characters"},
		{"null byte", PluginSpec{Path: "plugins/a\x00b"}, "must not contain null bytes"},
		{"arg newline", piSpec("plugins/x", "--a\nb"), "pi.args[0] must not contain newlines"},
		{"arg empty", piSpec("plugins/x", ""), "pi.args[0] must be non-empty"},
		{"arg first not a flag", piSpec("plugins/x", "override"), `pi.args[0] "override" must be a --flag`},
		// pi parses every element positionally, so a later element that
		// looks like an option is one.
		{"arg single dash", piSpec("plugins/x", "--x", "-e", "/sandbox/workspace/.pi/evil.js"), `args[1] "-e" must be --flag or --flag=value`},
		{"arg bare dash", piSpec("plugins/x", "--x", "-"), `args[1] "-" must be --flag or --flag=value`},
		{"arg bare double dash", piSpec("plugins/x", "--x", "--"), `args[1] "--" must be --flag or --flag=value`},
		{"arg pi option approve", piSpec("plugins/x", "--x", "--approve"), `args[1] "--approve" is one of pi's own options`},
		{"arg pi option extension", piSpec("plugins/x", "--extension", "/tmp/e.js"), `args[0] "--extension" is one of pi's own options`},
		{"arg pi option with value", piSpec("plugins/x", "--x", "--model=evil"), `args[1] "--model" is one of pi's own options`},
		{"arg value at-prefixed", piSpec("plugins/x", "--x", "@/etc/passwd"), `args[1] "@/etc/passwd" must not start with '@'`},
		{"env key lowercase", PluginSpec{Path: "plugins/x", Env: map[string]string{"fff_mode": "1"}}, `env key "fff_mode" must match ^[A-Z_][A-Z0-9_]*$`},
		{"env key digit first", PluginSpec{Path: "plugins/x", Env: map[string]string{"1X": "1"}}, `env key "1X" must match`},
		{"env value newline", PluginSpec{Path: "plugins/x", Env: map[string]string{"A": "1\n2"}}, `env["A"] must not contain newlines`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validPluginHarness(tc.spec).Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "plugins[0]")
			assert.Contains(t, err.Error(), tc.want)
		})
	}

	// The index in the message names the offending entry.
	err := validPluginHarness(PluginSpec{Path: "plugins/ok"}, PluginSpec{Path: "npm:x"}).Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plugins[1]")
}

func piSpec(path string, args ...string) PluginSpec {
	return PluginSpec{Path: path, Pi: &PiPluginOptions{Args: args}}
}

// TestValidate_PluginsReservedEnv pins the deny-list. Plugin env is
// exported last and inherited by the runtime and by every hook script it
// spawns, so the list has to cover the interpreter environment and every
// credential-shaped family, not just the five names the runtime pins.
func TestValidate_PluginsReservedEnv(t *testing.T) {
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
		// provider credential key must be refused here too: plugin env
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
			err := validPluginHarness(PluginSpec{Path: "plugins/x", Env: map[string]string{key: "v"}}).Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), `env key "`+key+`" is reserved`)
		})
	}

	// A plugin's own settings still go through.
	for _, key := range []string{"FFF_MULTIGREP", "GO_DIAG_LEVEL", "X_Y9", "DIAGNOSTICS_MODE"} {
		t.Run("allowed/"+key, func(t *testing.T) {
			require.NoError(t, validPluginHarness(PluginSpec{Path: "plugins/x", Env: map[string]string{key: "v"}}).Validate())
		})
	}
}

// TestValidate_PluginsDuplicates covers the base+child collision: two
// entries that upload as the same sandbox name would silently replace one
// another, so harness load rejects them.
func TestValidate_PluginsDuplicates(t *testing.T) {
	t.Parallel()
	err := validPluginHarness(
		PluginSpec{Path: "plugins/gopls-lsp"},
		PluginSpec{Path: "plugins/gopls-lsp"},
	).Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plugins[1]")
	assert.Contains(t, err.Error(), "already listed as plugins[0]")

	// Base contributes vendor/go-diagnostics, the child extensions/go-diagnostics.
	err = validPluginHarness(
		PluginSpec{Path: "vendor/go-diagnostics"},
		PluginSpec{Path: "extensions/go-diagnostics"},
	).Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plugins[1]")
	assert.Contains(t, err.Error(), `both load as plugin "go-diagnostics"`)

	require.NoError(t, validPluginHarness(
		PluginSpec{Path: "plugins/go-diagnostics"},
		PluginSpec{Path: "extensions/pi-fff"},
	).Validate())
}

func TestResolveRelativeTo_PluginOptions(t *testing.T) {
	t.Parallel()
	h := &Harness{Agent: "agents/test.md", Plugins: []PluginSpec{piSpec("extensions/x", "--a")}}
	require.NoError(t, h.ResolveRelativeTo("/base/dir"))
	assert.Equal(t, "/base/dir/extensions/x", h.Plugins[0].Path)
	assert.Equal(t, []string{"--a"}, h.Plugins[0].PiArgs(), "pi args survive resolution")

	h = &Harness{Agent: "agents/test.md", Plugins: []PluginSpec{{Path: "../outside"}}}
	err := h.ResolveRelativeTo("/base/dir")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plugins[0]")
}

// TestValidateFilesExist_PluginDirRules covers the checks that need the
// directory on disk: the stat rules, the format verdict (reported against
// the offending entry — the rule itself is pinned in
// internal/pluginformat), and the two checks that depend on which format
// the entry turned out to be in.
func TestValidateFilesExist_PluginDirRules(t *testing.T) {
	t.Parallel()
	agent := filepath.Join(t.TempDir(), "code.md")
	require.NoError(t, os.WriteFile(agent, []byte("# agent"), 0o644))
	dirNamed := func(t *testing.T, name string, files map[string]string) string {
		t.Helper()
		dir := filepath.Join(t.TempDir(), name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		for file, content := range files {
			require.NoError(t, os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644))
		}
		return dir
	}
	pluginDir := func(t *testing.T, files map[string]string) string {
		return dirNamed(t, "my-plugin", files)
	}
	validate := func(t *testing.T, specs ...PluginSpec) error {
		t.Helper()
		return (&Harness{Agent: agent, Plugins: specs}).ValidateFilesExist()
	}

	t.Run("pi extension", func(t *testing.T) {
		require.NoError(t, validate(t, PluginSpec{Path: pluginDir(t, map[string]string{"index.js": "//"})}))
	})

	t.Run("claude plugin", func(t *testing.T) {
		require.NoError(t, validate(t, PluginSpec{Path: pluginDir(t, map[string]string{"plugin.json": `{"name":"x"}`})}))
	})

	t.Run("neither format", func(t *testing.T) {
		dir := pluginDir(t, map[string]string{"README.md": "#"})
		err := validate(t, PluginSpec{Path: dir})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `plugins[0] "`+dir+`"`)
		assert.Contains(t, err.Error(), "not a Claude plugin (no plugin.json or .claude-plugin/plugin.json) and not a pi extension")
	})

	// A Claude plugin is claimed by its marker without a tree walk, so the
	// no-symlink rule has to be applied to it here — the injection scan
	// that refuses the same entry only runs with security enabled.
	t.Run("symlink inside a claude plugin", func(t *testing.T) {
		dir := pluginDir(t, map[string]string{"plugin.json": `{"name":"x"}`})
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "commands"), 0o755))
		require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(dir, "commands", "go.md")))
		err := validate(t, PluginSpec{Path: dir})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "commands/go.md")
	})

	// Validate() cannot compare a URL entry's basename with a local one; by
	// ValidateFilesExist every entry is a local directory, so two entries
	// that would upload to the same sandbox name are refused here.
	t.Run("same basename after resolution", func(t *testing.T) {
		a := pluginDir(t, map[string]string{"index.js": "//"})
		b := pluginDir(t, map[string]string{"plugin.json": `{"name":"x"}`})
		require.NotEqual(t, a, b)
		err := validate(t, PluginSpec{Path: a}, PluginSpec{Path: b})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `both load as plugin "my-plugin"`)
		require.NoError(t, validate(t, PluginSpec{Path: a}, PluginSpec{Path: a}), "the same resolved path twice is a resolve-side dedup, not a collision")
	})

	// env and pi: are options for a runtime that loads the entry as code.
	// On a Claude plugin they would be silently dropped, so they are a
	// validation error instead.
	t.Run("env on a claude plugin", func(t *testing.T) {
		dir := pluginDir(t, map[string]string{"plugin.json": `{"name":"x"}`})
		err := validate(t, PluginSpec{Path: dir, Env: map[string]string{"A": "1"}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "env/pi options apply to plugins the runtime loads as code")
	})

	t.Run("pi block on a claude plugin", func(t *testing.T) {
		dir := pluginDir(t, map[string]string{"plugin.json": `{"name":"x"}`})
		err := validate(t, PluginSpec{Path: dir, Pi: &PiPluginOptions{Args: []string{"--x"}}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is a Claude plugin")
	})

	t.Run("env and pi on a pi extension", func(t *testing.T) {
		dir := pluginDir(t, map[string]string{"index.js": "//"})
		require.NoError(t, validate(t, PluginSpec{
			Path: dir,
			Env:  map[string]string{"FFF_MULTIGREP": "1"},
			Pi:   &PiPluginOptions{Args: []string{"--fff-mode", "override"}},
		}))
	})

	// The sandbox names the runner owns are only pi's to reserve: a Claude
	// plugin called fullsend-hooks lands somewhere else entirely.
	t.Run("reserved pi names", func(t *testing.T) {
		for _, name := range pluginformat.PiReservedExtensionNames {
			t.Run(name, func(t *testing.T) {
				dir := dirNamed(t, name, map[string]string{"index.js": "//"})
				err := validate(t, PluginSpec{Path: dir})
				require.Error(t, err)
				assert.Contains(t, err.Error(), `"`+name+`" is a name the runner owns`)

				claude := dirNamed(t, name, map[string]string{"plugin.json": `{"name":"x"}`})
				require.NoError(t, validate(t, PluginSpec{Path: claude}))
			})
		}
		require.NoError(t, validate(t, PluginSpec{
			Path: dirNamed(t, "fullsend-hooks-extra", map[string]string{"index.js": "//"}),
		}))
	})

	t.Run("missing", func(t *testing.T) {
		err := validate(t, PluginSpec{Path: filepath.Join(t.TempDir(), "missing")})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "plugins[0]")
	})

	t.Run("file instead of a directory", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "ext.js")
		require.NoError(t, os.WriteFile(file, []byte("//"), 0o644))
		err := validate(t, PluginSpec{Path: file})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be a directory")
	})

	// An unresolved URL entry is the caller's ordering bug: it is skipped
	// here rather than stat'd, the same defence the other fields apply.
	t.Run("url entry is skipped", func(t *testing.T) {
		require.NoError(t, validate(t, PluginSpec{Path: "https://github.com/org/repo/tree/main/plugins/p#sha256=" + hex64}))
	})
}
