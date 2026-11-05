package repos

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/preset"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testPresetYAML = `version: "1"
runtime: claude
`

func writePresetFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "preset.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestResolveConfig_ConfigPresetCascade(t *testing.T) {
	path := writePresetFile(t, testPresetYAML)
	override := writePresetFile(t, "version: \"1\"\nruntime: pi\n")

	m := &Manifest{
		Version: 1,
		Defaults: DefaultsConfig{
			ConfigBase: ConfigBase{
				Source: path,
				SHA256: sha256Hex(testPresetYAML),
			},
		},
		GitHub: &PlatformConfig{
			MintURL: "https://mint.example.com",
			Repos: []RepoEntry{
				{Name: "acme/inherit"},
				{Name: "acme/override", ConfigBase: ConfigBase{Source: override}},
				{Name: "acme/disabled", ConfigBase: ConfigBase{Source: NoneSentinel}},
			},
		},
	}
	require.NoError(t, m.Validate())

	inherited, ok := m.ResolveConfig("acme", "inherit")
	require.True(t, ok)
	assert.Equal(t, path, inherited.Config)
	assert.Equal(t, sha256Hex(testPresetYAML), inherited.ConfigHash)

	overridden, ok := m.ResolveConfig("acme", "override")
	require.True(t, ok)
	assert.Equal(t, override, overridden.Config)
	assert.Equal(t, sha256Hex(testPresetYAML), overridden.ConfigHash, "hash still inherits unless cleared")

	disabled, ok := m.ResolveConfig("acme", "disabled")
	require.True(t, ok)
	assert.Empty(t, disabled.Config)
	assert.Empty(t, disabled.ConfigHash, "disabled source drops inherited hash")
}

func TestResolveConfig_ConfigHashNone(t *testing.T) {
	path := writePresetFile(t, testPresetYAML)
	m := &Manifest{
		Version: 1,
		Defaults: DefaultsConfig{
			ConfigBase: ConfigBase{
				Source: path,
				SHA256: sha256Hex(testPresetYAML),
			},
		},
		GitHub: &PlatformConfig{
			MintURL: "https://mint.example.com",
			Repos: []RepoEntry{
				{Name: "acme/app", ConfigBase: ConfigBase{SHA256: NoneSentinel}},
			},
		},
	}
	require.NoError(t, m.Validate())
	cfg, ok := m.ResolveConfig("acme", "app")
	require.True(t, ok)
	assert.Equal(t, path, cfg.Config)
	assert.Empty(t, cfg.ConfigHash)
}

func TestLoadConfigPreset_HashMismatchFails(t *testing.T) {
	path := writePresetFile(t, testPresetYAML)
	_, err := loadConfigPreset(context.Background(), path, strings.Repeat("0", 64))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "preset hash mismatch")
}

func TestLoadConfigPreset_InvalidYAMLFails(t *testing.T) {
	path := writePresetFile(t, ":\n- not yaml")
	_, err := loadConfigPreset(context.Background(), path, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid YAML")
}

func TestLoadConfigPreset_OrgConfigRejected(t *testing.T) {
	path := writePresetFile(t, "version: \"1\"\ndefaults:\n  runtime: claude\n")
	_, err := loadConfigPreset(context.Background(), path, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a per-repo configuration")
}

func TestLoadConfigPreset_InvalidPerRepoRejected(t *testing.T) {
	path := writePresetFile(t, "version: \"1\"\nruntime: nope\n")
	_, err := loadConfigPreset(context.Background(), path, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid preset")
}

func TestValidatePresetAsPerRepo_ParseError(t *testing.T) {
	// Valid YAML and not an org config, but runtime is a sequence so
	// ParsePerRepoConfigWriter cannot unmarshal into the per-repo struct.
	err := validatePresetAsPerRepo([]byte("version: \"1\"\nruntime: [claude]\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing preset")
}

func TestPresetCache_EmptySource(t *testing.T) {
	data, err := newPresetCache().Load(context.Background(), "", "")
	require.NoError(t, err)
	assert.Nil(t, data)
}

func TestLoadConfigPreset_MissingFileFails(t *testing.T) {
	_, err := loadConfigPreset(context.Background(), "/nonexistent/preset.yaml", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading preset file")
}

func TestPresetCache_ReusesFetch(t *testing.T) {
	path := writePresetFile(t, testPresetYAML)
	store := newPresetCache()
	first, err := store.Load(context.Background(), path, sha256Hex(testPresetYAML))
	require.NoError(t, err)
	require.NoError(t, os.Remove(path))
	second, err := store.Load(context.Background(), path, sha256Hex(testPresetYAML))
	require.NoError(t, err)
	assert.Equal(t, first, second)
}

func TestConvergePresetFiles_IdempotentWhenUnchanged(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/"+preset.BasePath] = []byte(testPresetYAML)
	resolved := ResolvedConfig{
		Owner: "acme",
		Repo:  "api",
		ForgeConfig: ForgeConfig{
			Client: fc,
		},
	}
	files, actions := convergePresetFiles(context.Background(), resolved, []byte(testPresetYAML), false, noopProgress)
	assert.Empty(t, files)
	assert.Empty(t, actions)
}

func TestConvergePresetFiles_ReplacesChangedBase(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/"+preset.BasePath] = []byte("version: \"1\"\nruntime: pi\n")
	fc.FileContents["acme/api/"+preset.OverlayPath] = []byte("version: \"1\"\n# overlay comment\n")
	resolved := ResolvedConfig{
		Owner: "acme",
		Repo:  "api",
		ForgeConfig: ForgeConfig{
			Client: fc,
		},
	}
	files, actions := convergePresetFiles(context.Background(), resolved, []byte(testPresetYAML), false, noopProgress)
	require.Len(t, files, 1)
	assert.Equal(t, preset.BasePath, files[0].Path)
	assert.Equal(t, testPresetYAML, string(files[0].Content))
	require.Len(t, actions, 1)
	assert.Equal(t, "update", actions[0].Action)
	_, overlayPresent := fc.FileContents["acme/api/"+preset.OverlayPath]
	assert.True(t, overlayPresent, "overlay must not be rewritten by preset apply")
}

func TestConvergePresetFiles_AddsMissingBase(t *testing.T) {
	fc := forge.NewFakeClient()
	resolved := ResolvedConfig{
		Owner: "acme",
		Repo:  "api",
		ForgeConfig: ForgeConfig{
			Client: fc,
		},
	}
	files, actions := convergePresetFiles(context.Background(), resolved, []byte(testPresetYAML), false, noopProgress)
	require.Len(t, files, 1)
	assert.Equal(t, "add", actions[0].Action)
}

func TestConvergePresetFiles_DryRunAddAndUpdate(t *testing.T) {
	fc := forge.NewFakeClient()
	resolved := ResolvedConfig{
		Owner: "acme",
		Repo:  "api",
		ForgeConfig: ForgeConfig{
			Client: fc,
		},
	}
	files, actions := convergePresetFiles(context.Background(), resolved, []byte(testPresetYAML), true, noopProgress)
	assert.Empty(t, files)
	require.Len(t, actions, 1)
	assert.Equal(t, "add", actions[0].Action)
	assert.Contains(t, actions[0].Detail, "would add")

	fc.FileContents["acme/api/"+preset.BasePath] = []byte("version: \"1\"\nruntime: pi\n")
	files, actions = convergePresetFiles(context.Background(), resolved, []byte(testPresetYAML), true, noopProgress)
	assert.Empty(t, files)
	require.Len(t, actions, 1)
	assert.Equal(t, "update", actions[0].Action)
	assert.Contains(t, actions[0].Detail, "would update")
}

func TestConvergePresetFiles_NoPresetIsNoOp(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/"+preset.BasePath] = []byte(testPresetYAML)
	resolved := ResolvedConfig{
		Owner: "acme",
		Repo:  "api",
		ForgeConfig: ForgeConfig{
			Client: fc,
		},
	}
	files, actions := convergePresetFiles(context.Background(), resolved, nil, false, noopProgress)
	assert.Empty(t, files)
	assert.Empty(t, actions)
}

func TestConvergePresetFiles_ReadError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.GetFileContentErrors = map[string]error{
		"acme/api/" + preset.BasePath: fmt.Errorf("boom"),
	}
	resolved := ResolvedConfig{
		Owner: "acme",
		Repo:  "api",
		ForgeConfig: ForgeConfig{
			Client: fc,
		},
	}
	files, actions := convergePresetFiles(context.Background(), resolved, []byte(testPresetYAML), false, noopProgress)
	assert.Empty(t, files)
	require.Len(t, actions, 1)
	assert.Equal(t, "error", actions[0].Action)
}

func TestCheckPresetDrift_ReportsMissingAndChanged(t *testing.T) {
	path := writePresetFile(t, testPresetYAML)
	store := newPresetCache()

	fc := forge.NewFakeClient()
	status := RepoStatus{}
	cfg := ResolvedConfig{
		Owner:  "acme",
		Repo:   "api",
		Config: path,
		ForgeConfig: ForgeConfig{
			Client: fc,
		},
	}
	status.Installed = true
	checkPresetDrift(context.Background(), cfg, store, &status)
	require.Len(t, status.Drifts, 1)
	assert.Equal(t, preset.BasePath, status.Drifts[0].Field)
	assert.Equal(t, "missing", status.Drifts[0].Actual)

	fc.FileContents["acme/api/"+preset.BasePath] = []byte("version: \"1\"\nruntime: pi\n")
	status = RepoStatus{}
	status.Installed = true
	checkPresetDrift(context.Background(), cfg, store, &status)
	require.Len(t, status.Drifts, 1)
	assert.Equal(t, "installed content differs", status.Drifts[0].Actual)

	fc.FileContents["acme/api/"+preset.BasePath] = []byte(testPresetYAML)
	status = RepoStatus{}
	status.Installed = true
	checkPresetDrift(context.Background(), cfg, store, &status)
	assert.Empty(t, status.Drifts)
}

func TestCheckPresetDrift_NoPresetExistingBaseIsDrift(t *testing.T) {
	for _, installed := range []bool{true, false} {
		fc := forge.NewFakeClient()
		fc.FileContents["acme/api/"+preset.BasePath] = []byte(testPresetYAML)
		status := RepoStatus{}
		cfg := ResolvedConfig{
			Owner: "acme",
			Repo:  "api",
			ForgeConfig: ForgeConfig{
				Client: fc,
			},
		}
		status.Installed = installed
		checkPresetDrift(context.Background(), cfg, newPresetCache(), &status)
		require.Len(t, status.Drifts, 1, "installed=%v", installed)
		assert.Equal(t, preset.BasePath, status.Drifts[0].Field)
		assert.Equal(t, undeclaredBaseExpected, status.Drifts[0].Expected)
		assert.Contains(t, status.Drifts[0].Actual, "declare a config_base source")
		assert.Empty(t, status.Error)
	}
}

func TestCheckPresetDrift_NoPresetNoBaseInSync(t *testing.T) {
	status := RepoStatus{}
	cfg := ResolvedConfig{
		Owner:       "acme",
		Repo:        "api",
		ForgeConfig: ForgeConfig{Client: forge.NewFakeClient()},
	}
	status.Installed = true
	checkPresetDrift(context.Background(), cfg, newPresetCache(), &status)
	assert.Empty(t, status.Drifts)
	assert.Empty(t, status.Error)
}

func TestCheckPresetDrift_NoPresetReadError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.GetFileContentErrors = map[string]error{
		"acme/api/" + preset.BasePath: fmt.Errorf("boom"),
	}
	status := RepoStatus{}
	cfg := ResolvedConfig{
		Owner:       "acme",
		Repo:        "api",
		ForgeConfig: ForgeConfig{Client: fc},
	}
	status.Installed = true
	checkPresetDrift(context.Background(), cfg, newPresetCache(), &status)
	assert.Contains(t, status.Error, "reading "+preset.BasePath)
	assert.Empty(t, status.Drifts)
}

func TestCheckPresetDrift_NotInstalledSkipsMissingButReportsDiffering(t *testing.T) {
	path := writePresetFile(t, testPresetYAML)
	fc := forge.NewFakeClient()
	cfg := ResolvedConfig{
		Owner:       "acme",
		Repo:        "api",
		Config:      path,
		ForgeConfig: ForgeConfig{Client: fc},
	}
	status := RepoStatus{}
	checkPresetDrift(context.Background(), cfg, newPresetCache(), &status)
	assert.Empty(t, status.Drifts, "missing base before install is not drift")

	fc.FileContents["acme/api/"+preset.BasePath] = []byte("version: \"1\"\nruntime: pi\n")
	status = RepoStatus{}
	checkPresetDrift(context.Background(), cfg, newPresetCache(), &status)
	require.Len(t, status.Drifts, 1)
	assert.Equal(t, "installed content differs", status.Drifts[0].Actual)
}

func TestCheckUndeclaredBase(t *testing.T) {
	path := writePresetFile(t, testPresetYAML)
	tests := []struct {
		name    string
		config  string
		files   map[string][]byte
		readErr error
		wantErr string
	}{
		{name: "no preset and no base", config: ""},
		{name: "declared preset ignores existing base", config: path,
			files: map[string][]byte{"acme/api/" + preset.BasePath: []byte("runtime: pi\n")}},
		{name: "no preset with existing base", config: "",
			files:   map[string][]byte{"acme/api/" + preset.BasePath: []byte(testPresetYAML)},
			wantErr: "no config_base preset is declared"},
		{name: "read error", config: "", readErr: fmt.Errorf("boom"), wantErr: "reading " + preset.BasePath},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := forge.NewFakeClient()
			maps.Copy(fc.FileContents, tt.files)
			if tt.readErr != nil {
				fc.GetFileContentErrors = map[string]error{"acme/api/" + preset.BasePath: tt.readErr}
			}
			err := checkUndeclaredBase(context.Background(), ResolvedConfig{
				Owner:       "acme",
				Repo:        "api",
				Config:      tt.config,
				ForgeConfig: ForgeConfig{Client: fc},
			})
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			if tt.readErr == nil {
				assert.Contains(t, err.Error(), "acme/api")
			}
		})
	}
}

func TestCheckPresetDrift_InvalidSourceErrors(t *testing.T) {
	status := RepoStatus{}
	cfg := ResolvedConfig{
		Owner:  "acme",
		Repo:   "api",
		Config: "/nonexistent/preset.yaml",
		ForgeConfig: ForgeConfig{
			Client: forge.NewFakeClient(),
		},
	}
	status.Installed = true
	checkPresetDrift(context.Background(), cfg, newPresetCache(), &status)
	require.NotEmpty(t, status.Error)
	assert.Contains(t, status.Error, "loading config preset")
}

func TestCheckPresetDrift_ReadError(t *testing.T) {
	path := writePresetFile(t, testPresetYAML)
	fc := forge.NewFakeClient()
	fc.GetFileContentErrors = map[string]error{
		"acme/api/" + preset.BasePath: fmt.Errorf("boom"),
	}
	status := RepoStatus{}
	cfg := ResolvedConfig{
		Owner:  "acme",
		Repo:   "api",
		Config: path,
		ForgeConfig: ForgeConfig{
			Client: fc,
		},
	}
	status.Installed = true
	checkPresetDrift(context.Background(), cfg, newPresetCache(), &status)
	require.NotEmpty(t, status.Error)
	assert.Contains(t, status.Error, "reading")
}
