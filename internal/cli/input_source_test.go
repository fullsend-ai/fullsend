package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/resolve"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// mapLookup is an os.LookupEnv stand-in over a fixed map.
func mapLookup(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}
}

func TestResolveInputSource(t *testing.T) {
	for _, tc := range []struct {
		name    string
		field   string
		env     map[string]string
		want    inputSource
		wantErr string
	}{
		{name: "neither", want: inputSource{"maintainer", "default"}},
		{name: "field only", field: "external", want: inputSource{"external", "harness"}},
		{name: "field maintainer", field: "maintainer", want: inputSource{"maintainer", "harness"}},
		{name: "variable only", env: map[string]string{"FULLSEND_INPUT_SOURCE": "external"}, want: inputSource{"external", "env"}},
		{name: "empty variable is unset", env: map[string]string{"FULLSEND_INPUT_SOURCE": ""}, want: inputSource{"maintainer", "default"}},
		{name: "field overrides variable", field: "maintainer", env: map[string]string{"FULLSEND_INPUT_SOURCE": "external"}, want: inputSource{"maintainer", "harness"}},
		{name: "invalid field", field: "outside",
			wantErr: `input_source "outside" is not valid (allowed: external, maintainer)`},
		{name: "invalid variable", env: map[string]string{"FULLSEND_INPUT_SOURCE": "EXTERNAL"},
			wantErr: `FULLSEND_INPUT_SOURCE="EXTERNAL" is not valid (allowed: external, maintainer)`},
		{name: "invalid variable fails with a field", field: "external", env: map[string]string{"FULLSEND_INPUT_SOURCE": "yes"},
			wantErr: `FULLSEND_INPUT_SOURCE="yes" is not valid (allowed: external, maintainer)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveInputSource(&harness.Harness{InputSource: tc.field}, mapLookup(tc.env))
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Equal(t, tc.wantErr, err.Error())
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestApplyDryRunDefault(t *testing.T) {
	external := inputSource{"external", "harness"}
	maintainer := inputSource{"maintainer", "default"}
	for _, tc := range []struct {
		name      string
		src       inputSource
		runnerEnv map[string]string
		env       map[string]string
		want      string
		wantValue string // FULLSEND_DRY_RUN in RunnerEnv afterwards; "" = absent
	}{
		{name: "external, unset", src: external, want: "on", wantValue: "1"},
		{name: "external, process env 0", src: external, env: map[string]string{"FULLSEND_DRY_RUN": "0"}, want: "off (FULLSEND_DRY_RUN is set)", wantValue: "0"},
		{name: "external, process env 1 survives an empty env.runner entry", src: external, env: map[string]string{"FULLSEND_DRY_RUN": "1"}, runnerEnv: map[string]string{"FULLSEND_DRY_RUN": ""}, want: "off (FULLSEND_DRY_RUN is set)", wantValue: "1"},
		{name: "external, process env empty counts as unset", src: external, env: map[string]string{"FULLSEND_DRY_RUN": ""}, want: "on", wantValue: "1"},
		{name: "external, env.runner empty counts as unset", src: external, runnerEnv: map[string]string{"FULLSEND_DRY_RUN": ""}, want: "on", wantValue: "1"},
		{name: "external, env.runner 0", src: external, runnerEnv: map[string]string{"FULLSEND_DRY_RUN": "0"}, want: "off (FULLSEND_DRY_RUN is set)", wantValue: "0"},
		{name: "maintainer, unset", src: maintainer, want: "off"},
		{name: "maintainer, process env 1", src: maintainer, env: map[string]string{"FULLSEND_DRY_RUN": "1"}, want: "off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &harness.Harness{RunnerEnv: tc.runnerEnv}
			assert.Equal(t, tc.want, applyDryRunDefault(h, tc.src, mapLookup(tc.env)))
			got, ok := h.RunnerEnv["FULLSEND_DRY_RUN"]
			if tc.wantValue == "" {
				assert.False(t, ok, "FULLSEND_DRY_RUN must not be added")
				return
			}
			assert.Equal(t, tc.wantValue, got)
		})
	}
}

// The dry-run default reaches the pre-script and post-script environments.
func TestDryRunDefaultReachesScriptEnv(t *testing.T) {
	t.Setenv("FULLSEND_DRY_RUN", "")
	require.NoError(t, os.Unsetenv("FULLSEND_DRY_RUN"))
	h := &harness.Harness{RunnerEnv: map[string]string{}}
	require.Equal(t, "on", applyDryRunDefault(h, inputSource{"external", "env"}, os.LookupEnv))
	assert.Contains(t, childScriptEnv(h.RunnerEnv, ""), "FULLSEND_DRY_RUN=1")
	assert.Contains(t, postScriptEnv(h, ""), "FULLSEND_DRY_RUN=1")
}

// writeProviderDef writes providers/<name>.yaml under dir.
func writeProviderDef(t *testing.T, dir, name, typ string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "providers"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "providers", name+".yaml"),
		[]byte("name: "+name+"\ntype: "+typ+"\ncredentials:\n  DUMMY_TOKEN: dummy\n"), 0o644))
}

func TestApplyInputSource_ForgeWriteGate(t *testing.T) {
	dir := t.TempDir()
	writeProviderDef(t, dir, "gitlab", "fullsend-gitlab-forge")
	writeProviderDef(t, dir, "notes", "fullsend-notes")
	writeProviderDef(t, dir, "gitlab-rw", "fullsend-gitlab-rw")
	writeProviderDef(t, dir, "gitlab-ro", "fullsend-gitlab-ro")
	const advice = "switch to github-ro (type fullsend-github-ro), or set allow_forge_write: true in the harness"
	const gitlabForgeAdvice = "remove the provider, or set allow_forge_write: true in the harness"
	for _, tc := range []struct {
		name       string
		providers  []string
		resolved   []resolve.ResolvedProvider
		source     string
		allowWrite bool
		wantErr    string
		wantLine   string
	}{
		{name: "github, external, no opt-in", providers: []string{"github"}, source: "external",
			wantErr: "input_source is external (harness), but these providers can write to the forge:\n    provider \"github\" (type fullsend-github): " + advice},
		{name: "github, external, opt-in", providers: []string{"github"}, source: "external", allowWrite: true,
			wantLine: "input_source: external (harness); dry run default: on; forge write: github; durable host files: none"},
		{name: "github-ro, external", providers: []string{"github-ro"}, source: "external",
			wantLine: "input_source: external (harness); dry run default: on; forge write: github-ro; durable host files: none"},
		{name: "gitlab forge, external, no opt-in", providers: []string{"gitlab"}, source: "external",
			wantErr: `provider "gitlab" (type fullsend-gitlab-forge): ` + gitlabForgeAdvice},
		{name: "gitlab-rw, external, no opt-in", providers: []string{"gitlab-rw"}, source: "external",
			wantErr: `provider "gitlab-rw" (type fullsend-gitlab-rw): switch to gitlab-ro (type fullsend-gitlab-ro), or set allow_forge_write: true in the harness`},
		{name: "gitlab-ro, external", providers: []string{"gitlab-ro"}, source: "external",
			wantLine: "input_source: external (harness); dry run default: on; forge write: gitlab-ro; durable host files: none"},
		{name: "gitlab forge, external, opt-in", providers: []string{"gitlab"}, source: "external", allowWrite: true,
			wantLine: "input_source: external (harness); dry run default: on; forge write: gitlab; durable host files: none"},
		{name: "two write providers are both named", providers: []string{"github", "gitlab"}, source: "external",
			wantErr: "forge:\n    provider \"gitlab\" (type fullsend-gitlab-forge): " + gitlabForgeAdvice + "\n    provider \"github\" (type fullsend-github): " + advice},
		{name: "URL-resolved github provider", source: "external",
			resolved: []resolve.ResolvedProvider{{Def: harness.ProviderDef{Name: "org-github", Type: "fullsend-github"}}},
			wantErr:  `provider "org-github" (type fullsend-github): ` + advice},
		{name: "undefined provider, external, no opt-in fails closed", providers: []string{"org-github"}, source: "external",
			wantErr: `input_source is external (harness), but provider "org-github" has no definition, so the run cannot check whether it writes to the forge; add a definition under providers/, or set allow_forge_write: true in the harness`},
		{name: "undefined provider, external, opt-in", providers: []string{"org-github"}, source: "external", allowWrite: true,
			wantLine: "input_source: external (harness); dry run default: on; forge write: none; durable host files: none"},
		{name: "undefined provider, maintainer", providers: []string{"org-github"}, source: "maintainer",
			wantLine: "input_source: maintainer (harness); dry run default: off; forge write: none; durable host files: none"},
		{name: "other provider types are unaffected", providers: []string{"notes"}, source: "external",
			wantLine: "input_source: external (harness); dry run default: on; forge write: none; durable host files: none"},
		{name: "github, maintainer", providers: []string{"github"}, source: "maintainer",
			wantLine: "input_source: maintainer (harness); dry run default: off; forge write: github; durable host files: none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &harness.Harness{Providers: tc.providers, InputSource: tc.source, AllowForgeWrite: tc.allowWrite}
			line, err := applyInputSource(h, dir, tc.resolved, mapLookup(nil))
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				_, set := h.RunnerEnv["FULLSEND_DRY_RUN"]
				assert.False(t, set, "a failed gate sets no default")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantLine, line)
		})
	}
}

func TestApplyInputSource_Line(t *testing.T) {
	sa := writeHostFileFixture(t, "service_account")
	plain := writeHostFileFixture(t, "plain text")
	for _, tc := range []struct {
		name     string
		h        harness.Harness
		env      map[string]string
		wantLine string
	}{
		{name: "maintainer default, nothing attached",
			wantLine: "input_source: maintainer (default); dry run default: off; forge write: none; durable host files: none"},
		{name: "external from the variable",
			env:      map[string]string{"FULLSEND_INPUT_SOURCE": "external"},
			h:        harness.Harness{Providers: []string{"github-ro"}},
			wantLine: "input_source: external (env); dry run default: on; forge write: github-ro; durable host files: none"},
		{name: "external with an explicit FULLSEND_DRY_RUN",
			env:      map[string]string{"FULLSEND_DRY_RUN": "0"},
			h:        harness.Harness{InputSource: "external"},
			wantLine: "input_source: external (harness); dry run default: off (FULLSEND_DRY_RUN is set); forge write: none; durable host files: none"},
		{name: "durable host files are listed by dest",
			h: harness.Harness{InputSource: "maintainer", HostFiles: []harness.HostFile{
				{Src: sa, Dest: "/tmp/.gcp-credentials.json", AllowDurable: true},
				{Src: plain, Dest: "/sandbox/workspace/.env.d/agent.env"},
				{Src: sa, Dest: "/tmp/second.json", AllowDurable: true},
				{Src: "/nonexistent/key.json", Dest: "/tmp/missing.json", Optional: true},
			}},
			wantLine: "input_source: maintainer (harness); dry run default: off; forge write: none; durable host files: /tmp/.gcp-credentials.json, /tmp/second.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.h
			line, err := applyInputSource(&h, t.TempDir(), nil, mapLookup(tc.env))
			require.NoError(t, err)
			assert.Equal(t, tc.wantLine, line)
		})
	}
}

// A malformed provider file fails the check the same way the provider step
// would, but only when the run attaches a provider.
func TestApplyInputSource_ProviderLoadError(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "providers"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "providers", "broken.yaml"), []byte("type: x\n"), 0o644))

	_, err := applyInputSource(&harness.Harness{}, dir, nil, mapLookup(nil))
	require.NoError(t, err, "a run with no provider does not read providers/")

	_, err = applyInputSource(&harness.Harness{Providers: []string{"github"}}, dir, nil, mapLookup(nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading provider definitions")
}

// runAgent applies input_source before the pre-script: the gate fails
// before it runs, and an external run's pre-script sees FULLSEND_DRY_RUN=1.
func TestRunAgent_InputSource(t *testing.T) {
	for _, tc := range []struct {
		name          string
		extra         string
		env           map[string]string // "" value = unset
		wantErr       string
		wantPreScript string // FULLSEND_DRY_RUN as the pre-script saw it; "" = pre-script must not run
		wantLine      string
	}{
		{name: "external field turns the dry-run default on",
			extra:         "input_source: external\n",
			env:           map[string]string{"FULLSEND_DRY_RUN": "", "FULLSEND_INPUT_SOURCE": ""},
			wantPreScript: "1",
			wantLine:      "input_source: external (harness); dry run default: on; forge write: none; durable host files: none"},
		{name: "explicit FULLSEND_DRY_RUN=0 is kept",
			env:           map[string]string{"FULLSEND_DRY_RUN": "0", "FULLSEND_INPUT_SOURCE": "external"},
			wantPreScript: "0",
			wantLine:      "input_source: external (env); dry run default: off (FULLSEND_DRY_RUN is set); forge write: none; durable host files: none"},
		{name: "maintainer keeps today's behaviour",
			env:           map[string]string{"FULLSEND_DRY_RUN": "", "FULLSEND_INPUT_SOURCE": ""},
			wantPreScript: "unset",
			wantLine:      "input_source: maintainer (default); dry run default: off; forge write: none; durable host files: none"},
		{name: "invalid variable fails before the pre-script",
			env:     map[string]string{"FULLSEND_INPUT_SOURCE": "outsider"},
			wantErr: `FULLSEND_INPUT_SOURCE="outsider" is not valid (allowed: external, maintainer)`},
		{name: "forge-write provider fails before the pre-script",
			extra:   "input_source: external\nproviders:\n  - github\n",
			env:     map[string]string{"FULLSEND_INPUT_SOURCE": ""},
			wantErr: `provider "github" (type fullsend-github): switch to github-ro`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usePreScriptStub(t)
			t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
			t.Setenv("FULLSEND_RUNTIME", "")
			for k, v := range tc.env {
				t.Setenv(k, v)
				if v == "" {
					require.NoError(t, os.Unsetenv(k))
				}
			}
			marker := filepath.Join(t.TempDir(), "pre-script-ran")
			dir := newVertexChildDir(t, marker, "dummy", "openai/gpt-5.6-luna", tc.extra, "")
			harnessPath := filepath.Join(dir, "harness", "code.yaml")
			data, err := os.ReadFile(harnessPath)
			require.NoError(t, err)
			pre := writePreScript(t, `printf '%s' "${FULLSEND_DRY_RUN-unset}" > `+marker+"\n")
			data = []byte(regexp.MustCompile(`(?m)^pre_script: .*$`).ReplaceAllString(string(data), "pre_script: "+pre))
			require.NoError(t, os.WriteFile(harnessPath, data, 0o644))

			var out bytes.Buffer
			err = runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "",
				resolveFlags{maxDepth: 10, maxResources: 50}, statusOpts{}, ui.New(&out), false, runOverrideFlags{})
			require.Error(t, err, "the stub never creates a sandbox")
			if tc.wantErr != "" {
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.NoFileExists(t, marker)
				return
			}
			assert.NotContains(t, err.Error(), "input_source")
			got, readErr := os.ReadFile(marker)
			require.NoError(t, readErr, "the pre-script must run")
			assert.Equal(t, tc.wantPreScript, string(got))
			assert.Equal(t, 1, strings.Count(out.String(), tc.wantLine+"\n"), out.String())
		})
	}
}
