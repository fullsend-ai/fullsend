package repos

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/preset"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizeManifestSource(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"github raw", "https://raw.githubusercontent.com/acme/infra/main/repos.yaml", "https://raw.githubusercontent.com/acme/infra/main/repos.yaml"},
		{"github raw nested path", "https://raw.githubusercontent.com/acme/infra/main/conf/repos.yaml", "https://raw.githubusercontent.com/acme/infra/main/conf/repos.yaml"},
		{"github raw pinned 40-hex commit", "https://raw.githubusercontent.com/acme/infra/0123456789abcdef0123456789abcdef01234567/repos.yaml", "https://raw.githubusercontent.com/acme/infra/0123456789abcdef0123456789abcdef01234567/repos.yaml"},
		{"github.com raw", "https://github.com/acme/infra/raw/main/repos.yaml", "https://github.com/acme/infra/raw/main/repos.yaml"},
		{"gitlab raw", "https://gitlab.com/acme/infra/-/raw/main/repos.yaml", "https://gitlab.com/acme/infra/-/raw/main/repos.yaml"},
		{"gitlab raw subgroup", "https://gitlab.com/acme/team/infra/-/raw/main/conf/repos.yaml", "https://gitlab.com/acme/team/infra/-/raw/main/conf/repos.yaml"},
		{"strips userinfo", "https://user:s3cret@raw.githubusercontent.com/acme/infra/main/repos.yaml", "https://raw.githubusercontent.com/acme/infra/main/repos.yaml"},
		{"strips query", "https://raw.githubusercontent.com/acme/infra/main/repos.yaml?token=abc", "https://raw.githubusercontent.com/acme/infra/main/repos.yaml"},
		{"strips fragment", "https://raw.githubusercontent.com/acme/infra/main/repos.yaml#L10", "https://raw.githubusercontent.com/acme/infra/main/repos.yaml"},
		{"uppercase scheme and host", "HTTPS://RAW.GITHUBUSERCONTENT.COM/acme/infra/main/repos.yaml", "https://raw.githubusercontent.com/acme/infra/main/repos.yaml"},
		{"omits unrecognized host", "https://example.com/org/infra/repos.yaml", ""},
		{"omits short path token", "https://example.com/t/abc123/repos.yaml", ""},
		{"omits 40-hex bearer token on unrecognized host", "https://example.com/t/0123456789abcdef0123456789abcdef01234567/repos.yaml", ""},
		{"omits 64-hex bearer token on unrecognized host", "https://example.com/t/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef/repos.yaml", ""},
		{"omits unrecognized host with port", "https://example.com:8443/repos.yaml", ""},
		{"omits recognized host with port", "https://raw.githubusercontent.com:8443/acme/infra/main/repos.yaml", ""},
		{"omits lookalike host", "https://raw.githubusercontent.com.evil.example/acme/infra/main/repos.yaml", ""},
		{"omits github raw too short", "https://raw.githubusercontent.com/acme/repos.yaml", ""},
		{"omits github.com non-raw", "https://github.com/acme/infra/blob/main/repos.yaml", ""},
		{"omits gitlab non-raw", "https://gitlab.com/acme/infra/-/blob/main/repos.yaml", ""},
		{"omits percent-encoded segment", "https://raw.githubusercontent.com/acme/infra/main/%61bc/repos.yaml", ""},
		{"omits path parameter token", "https://raw.githubusercontent.com/acme/infra/main/repos.yaml;token=abc", ""},
		{"omits encoded newline", "https://raw.githubusercontent.com/acme/infra/a%0Ab/repos.yaml", ""},
		{"omits dot-dot segment github raw", "https://raw.githubusercontent.com/acme/infra/main/../../other/repos.yaml", ""},
		{"omits dot-dot segment github.com raw", "https://github.com/acme/infra/raw/../../other/repos.yaml", ""},
		{"omits dot-dot segment gitlab raw", "https://gitlab.com/acme/infra/-/raw/../../uploads/abc/repos.yaml", ""},
		{"omits single-dot segment github raw", "https://raw.githubusercontent.com/acme/infra/main/./repos.yaml", ""},
		{"omits single-dot segment github.com raw", "https://github.com/acme/infra/raw/main/./repos.yaml", ""},
		{"omits single-dot segment gitlab raw", "https://gitlab.com/acme/infra/-/raw/main/./repos.yaml", ""},
		{"omits empty segment", "https://raw.githubusercontent.com/acme//main/repos.yaml", ""},
		{"empty", "", ""},
		{"local relative path", "repos.yaml", ""},
		{"local absolute path", "/home/user/infra/repos.yaml", ""},
		{"http rejected", "http://raw.githubusercontent.com/acme/infra/main/repos.yaml", ""},
		{"no host", "https:///repos.yaml", ""},
		{"unparseable", "https://exa mple.com/%zz", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeManifestSource(tt.raw)
			assert.Equal(t, tt.want, got)
			assert.NotContains(t, got, "\n")
		})
	}
}

func TestManagedConfigHeader(t *testing.T) {
	assert.Equal(t, managedConfigMarker, managedConfigHeader(""))
	assert.Equal(t, managedConfigMarker, managedConfigHeader("/local/repos.yaml"))
	assert.Equal(t,
		managedConfigMarker+"# Source manifest: https://raw.githubusercontent.com/acme/infra/main/repos.yaml\n",
		managedConfigHeader("https://tok@raw.githubusercontent.com/acme/infra/main/repos.yaml?x=1"))
	assert.Equal(t, managedConfigMarker, managedConfigHeader("https://example.com/repos.yaml"))
}

// sourcedManifest returns a converge manifest for acme/api that records the
// given HTTPS location, as LoadManifest does for a remote manifest.
func sourcedManifest(t *testing.T, rawURL string) *Manifest {
	t.Helper()
	m := newConvergeManifest("acme/api")
	m.Defaults.Config = mustManagedConfig(t, "kill_switch: true\n")
	m.sourceRemote = true
	m.sourceURL = sanitizeManifestSource(rawURL)
	return m
}

// TestDesiredManagedConfig_RecordsManifestSource: a manifest loaded from an
// HTTPS URL is named on a line after the unchanged two-line ownership
// marker, with credentials and query parameters removed (#8627).
func TestDesiredManagedConfig_RecordsManifestSource(t *testing.T) {
	m := sourcedManifest(t, "https://bot:ghp_secret@raw.githubusercontent.com/acme/infra/main/repos.yaml?token=abc")
	cfg, ok := m.ResolveConfig("acme", "api")
	require.True(t, ok)
	assert.Equal(t, "https://raw.githubusercontent.com/acme/infra/main/repos.yaml", cfg.ManifestSource)

	body, managed, err := desiredManagedConfig(cfg)
	require.NoError(t, err)
	require.True(t, managed)
	assert.True(t, hasManagedConfigMarker(body))
	assert.Equal(t,
		managedConfigMarker+
			"# Source manifest: https://raw.githubusercontent.com/acme/infra/main/repos.yaml\n"+
			"kill_switch: true\n",
		string(body))
	assert.NotContains(t, string(body), "ghp_secret")
	assert.NotContains(t, string(body), "token=")
}

// TestDesiredManagedConfig_LocalManifestOmitsSource: a manifest loaded from
// a local path or built in memory records no source line, so its
// machine-specific location is neither disclosed nor a cause of drift
// between operators.
func TestDesiredManagedConfig_LocalManifestOmitsSource(t *testing.T) {
	m := newConvergeManifest("acme/api")
	m.Defaults.Config = mustManagedConfig(t, "kill_switch: true\n")
	m.sourceDir = "/home/user/infra"
	body := mustDesiredManaged(t, m, "acme", "api")
	assert.Equal(t, managedConfigMarker+"kill_switch: true\n", string(body))
	assert.NotContains(t, string(body), managedConfigSourcePrefix)
}

// TestConvergeManagedConfigFiles_SourceLineIdempotent: a second install from
// the same manifest finds nothing to change.
func TestConvergeManagedConfigFiles_SourceLineIdempotent(t *testing.T) {
	m := sourcedManifest(t, "https://raw.githubusercontent.com/acme/infra/main/repos.yaml")
	desired := mustDesiredManaged(t, m, "acme", "api")

	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/"+preset.OverlayPath] = desired
	resolved := managedResolved(t, fc, m, "acme", "api")

	files, actions := convergeManagedConfigFiles(context.Background(), resolved, desired, false, noopProgress)
	assert.Empty(t, files)
	assert.Empty(t, actions)

	status := RepoStatus{Installed: true}
	checkManagedConfigDrift(context.Background(), resolved, &status)
	assert.Empty(t, status.Drifts)
}

// TestConvergeManagedConfigFiles_MarkerOnlyFileWithURLManifestIsCurrent: a
// file installed from a local manifest carries the ownership marker but no
// source line. Loading the same manifest from a URL renders a source line,
// but that informational difference alone is neither drift nor a rewrite,
// so a local-path and a URL invocation cannot keep reversing each other
// (#8785).
func TestConvergeManagedConfigFiles_MarkerOnlyFileWithURLManifestIsCurrent(t *testing.T) {
	m := sourcedManifest(t, "https://raw.githubusercontent.com/acme/infra/main/repos.yaml")
	desired := mustDesiredManaged(t, m, "acme", "api")
	old := []byte(managedConfigMarker + "kill_switch: true\n")
	require.Contains(t, string(desired), managedConfigSourcePrefix)

	fc := forge.NewFakeClient()
	fc.FileContents["acme/api/"+preset.OverlayPath] = old
	resolved := managedResolved(t, fc, m, "acme", "api")

	status := RepoStatus{Installed: true}
	checkManagedConfigDrift(context.Background(), resolved, &status)
	assert.Empty(t, status.Error)
	assert.Empty(t, status.Drifts)

	files, actions := convergeManagedConfigFiles(context.Background(), resolved, desired, false, noopProgress)
	assert.Empty(t, files)
	assert.Empty(t, actions)

	require.NoError(t, checkEstablishedOverlayGate(context.Background(), resolved, desired))
}

// TestManagedConfigSourceLine_DriftMatrix: for every combination of
// installed header (marker only, marker and source line, marker and a
// different source line) and desired header (with or without a URL source),
// an identical body is current and a differing body is drift that converge
// rewrites. A file without the ownership marker still requires adoption
// whatever the source line says (#8785).
func TestManagedConfigSourceLine_DriftMatrix(t *testing.T) {
	const (
		desiredURL = "https://raw.githubusercontent.com/acme/infra/main/repos.yaml"
		otherURL   = "https://gitlab.com/acme/infra/-/raw/main/repos.yaml"
	)
	installedHeaders := map[string]string{
		"marker only":                 managedConfigMarker,
		"marker and source":           managedConfigMarker + managedConfigSourcePrefix + desiredURL + "\n",
		"marker and different source": managedConfigMarker + managedConfigSourcePrefix + otherURL + "\n",
	}
	desiredManifests := map[string]func(t *testing.T) *Manifest{
		"URL manifest": func(t *testing.T) *Manifest { return sourcedManifest(t, desiredURL) },
		"local manifest": func(t *testing.T) *Manifest {
			m := newConvergeManifest("acme/api")
			m.Defaults.Config = mustManagedConfig(t, "kill_switch: true\n")
			return m
		},
	}
	for installedName, header := range installedHeaders {
		for desiredName, newManifest := range desiredManifests {
			t.Run(installedName+"/"+desiredName, func(t *testing.T) {
				m := newManifest(t)
				desired := mustDesiredManaged(t, m, "acme", "api")

				t.Run("identical body", func(t *testing.T) {
					fc := forge.NewFakeClient()
					fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(header + "kill_switch: true\n")
					resolved := managedResolved(t, fc, m, "acme", "api")

					status := RepoStatus{Installed: true}
					checkManagedConfigDrift(context.Background(), resolved, &status)
					assert.Empty(t, status.Error)
					assert.Empty(t, status.Drifts)

					files, actions := convergeManagedConfigFiles(context.Background(), resolved, desired, false, noopProgress)
					assert.Empty(t, files)
					assert.Empty(t, actions)
				})

				t.Run("differing body", func(t *testing.T) {
					fc := forge.NewFakeClient()
					fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(header + "kill_switch: false\n")
					resolved := managedResolved(t, fc, m, "acme", "api")

					status := RepoStatus{Installed: true}
					checkManagedConfigDrift(context.Background(), resolved, &status)
					assert.Empty(t, status.Error)
					require.Len(t, status.Drifts, 1)
					assert.Equal(t, "managed configuration", status.Drifts[0].Expected)
					assert.Equal(t, "installed content differs", status.Drifts[0].Actual)

					files, actions := convergeManagedConfigFiles(context.Background(), resolved, desired, false, noopProgress)
					require.Len(t, files, 1)
					assert.Equal(t, desired, files[0].Content)
					require.Len(t, actions, 1)
					assert.Equal(t, "update", actions[0].Action)
				})
			})
		}
	}

	t.Run("markerless file with source line requires adoption", func(t *testing.T) {
		m := sourcedManifest(t, desiredURL)
		desired := mustDesiredManaged(t, m, "acme", "api")
		fc := forge.NewFakeClient()
		fc.FileContents["acme/api/"+preset.OverlayPath] = []byte(managedConfigSourcePrefix + desiredURL + "\nkill_switch: true\n")
		resolved := managedResolved(t, fc, m, "acme", "api")

		status := RepoStatus{Installed: true}
		checkManagedConfigDrift(context.Background(), resolved, &status)
		require.Len(t, status.Drifts, 1)
		assert.Contains(t, status.Drifts[0].Expected, "adoption required")

		files, actions := convergeManagedConfigFiles(context.Background(), resolved, desired, false, noopProgress)
		assert.Empty(t, files)
		require.Len(t, actions, 1)
		assert.Equal(t, ActionAdoptionRequired, actions[0].Action)
	})
}

func TestStripManagedConfigSource(t *testing.T) {
	const (
		srcURL = managedConfigSourcePrefix + "https://raw.githubusercontent.com/acme/infra/main/repos.yaml"
		src    = srcURL + "\n"
	)
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"marker only", managedConfigMarker + "kill_switch: true\n", managedConfigMarker + "kill_switch: true\n"},
		{"marker and source", managedConfigMarker + src + "kill_switch: true\n", managedConfigMarker + "kill_switch: true\n"},
		{"marker and source without body", managedConfigMarker + src, managedConfigMarker},
		{"unterminated source line", managedConfigMarker + "# Source manifest: https://x", managedConfigMarker + "# Source manifest: https://x"},
		{"source line that is not a sanitized URL", managedConfigMarker + "# Source manifest: https://x\nkill_switch: true\n", managedConfigMarker + "# Source manifest: https://x\nkill_switch: true\n"},
		{"empty source line", managedConfigMarker + managedConfigSourcePrefix + "\nkill_switch: true\n", managedConfigMarker + managedConfigSourcePrefix + "\nkill_switch: true\n"},
		{"bare CR in source line", managedConfigMarker + srcURL + "\rkill_switch: false\n" + "kill_switch: true\n", managedConfigMarker + srcURL + "\rkill_switch: false\n" + "kill_switch: true\n"},
		{"NEL in source line", managedConfigMarker + srcURL + "\u0085kill_switch: false\nkill_switch: true\n", managedConfigMarker + srcURL + "\u0085kill_switch: false\nkill_switch: true\n"},
		{"LS in source line", managedConfigMarker + srcURL + "\u2028kill_switch: false\nkill_switch: true\n", managedConfigMarker + srcURL + "\u2028kill_switch: false\nkill_switch: true\n"},
		{"PS in source line", managedConfigMarker + srcURL + "\u2029kill_switch: false\nkill_switch: true\n", managedConfigMarker + srcURL + "\u2029kill_switch: false\nkill_switch: true\n"},
		{"source line not after marker", managedConfigMarker + "kill_switch: true\n" + src, managedConfigMarker + "kill_switch: true\n" + src},
		{"no marker", src + "kill_switch: true\n", src + "kill_switch: true\n"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := []byte(tt.in)
			orig := string(in)
			assert.Equal(t, tt.want, string(stripManagedConfigSource(in)))
			assert.Equal(t, orig, string(in), "input must not be modified")
		})
	}
}

func TestManagedConfigEquivalent(t *testing.T) {
	local := []byte(managedConfigMarker + "kill_switch: true\n")
	url := []byte(managedConfigMarker + managedConfigSourcePrefix + "https://raw.githubusercontent.com/acme/infra/main/repos.yaml\nkill_switch: true\n")
	assert.True(t, managedConfigEquivalent(local, url))
	assert.True(t, managedConfigEquivalent(url, local))
	assert.False(t, managedConfigEquivalent(local, []byte(managedConfigMarker+"kill_switch: false\n")))
	assert.False(t, managedConfigEquivalent(nil, local))
	assert.False(t, managedConfigEquivalent([]byte("kill_switch: true\n"), local))
}

// TestManagedConfigSourceLine_LineBreakSmuggling: a YAML line break other
// than LF inside the installed source line (bare CR, NEL, LS, PS) must not
// let the stripper discard configuration that the YAML loader still reads.
// Such a file is drift, and converge rewrites it to the desired bytes.
func TestManagedConfigSourceLine_LineBreakSmuggling(t *testing.T) {
	const srcURL = "https://raw.githubusercontent.com/acme/infra/main/repos.yaml"
	const body = "mint_url: https://other-mint.example.com\n"
	m := newConvergeManifest("acme/api")
	m.Defaults.Config = mustManagedConfig(t, body)
	desired := mustDesiredManaged(t, m, "acme", "api")
	require.Equal(t, managedConfigMarker+body, string(desired))

	for name, sep := range map[string]string{
		"bare CR": "\r",
		"NEL":     "\u0085",
		"LS":      "\u2028",
		"PS":      "\u2029",
	} {
		t.Run(name, func(t *testing.T) {
			installed := []byte(managedConfigMarker + managedConfigSourcePrefix + srcURL +
				sep + "kill_switch: false\n" + body)
			assert.False(t, managedConfigEquivalent(installed, desired))

			fc := forge.NewFakeClient()
			fc.FileContents["acme/api/"+preset.OverlayPath] = installed
			resolved := managedResolved(t, fc, m, "acme", "api")

			status := RepoStatus{Installed: true}
			checkManagedConfigDrift(context.Background(), resolved, &status)
			assert.Empty(t, status.Error)
			require.Len(t, status.Drifts, 1)

			files, actions := convergeManagedConfigFiles(context.Background(), resolved, desired, false, noopProgress)
			require.Len(t, files, 1)
			assert.Equal(t, desired, files[0].Content)
			require.Len(t, actions, 1)
			assert.Equal(t, "update", actions[0].Action)
		})
	}
}

// TestLoadManifest_LocalPathRecordsNoSource: a manifest loaded from disk
// through LoadManifest records no source URL, so the rendered managed file
// carries only the ownership marker and neither the file's location nor a
// source line leaks into the target repository (#8627).
func TestLoadManifest_LocalPathRecordsNoSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repos.yaml")
	require.NoError(t, os.WriteFile(path, []byte(layeredPrecedenceManifest), 0o644))
	m, err := LoadManifest(context.Background(), path)
	require.NoError(t, err)
	require.NoError(t, m.Validate())

	assert.False(t, m.sourceRemote)
	assert.Empty(t, m.sourceURL)

	cfg, ok := m.ResolveConfig("acme", "inherits")
	require.True(t, ok)
	assert.Empty(t, cfg.ManifestSource)

	body := mustDesiredManaged(t, m, "acme", "inherits")
	assert.True(t, strings.HasPrefix(string(body), managedConfigMarker))
	assert.NotContains(t, string(body), managedConfigSourcePrefix)
	assert.NotContains(t, string(body), filepath.Dir(path))
}

// TestResolveConfig_WiresManifestSource: ResolveConfig copies the manifest's
// recorded source URL onto the resolved configuration that
// desiredManagedConfig renders from, for every repository the manifest
// selects.
func TestResolveConfig_WiresManifestSource(t *testing.T) {
	m := loadLayeredPrecedenceManifest(t)
	m.sourceURL = "https://raw.githubusercontent.com/acme/infra/main/repos.yaml"

	cfg, ok := m.ResolveConfig("acme", "inherits")
	require.True(t, ok)
	assert.Equal(t, m.sourceURL, cfg.ManifestSource)

	body := mustDesiredManaged(t, m, "acme", "inherits")
	assert.True(t, strings.HasPrefix(string(body),
		managedConfigMarker+managedConfigSourcePrefix+m.sourceURL+"\n"))
}

// fakeManifestFetch replaces the remote fetch seam for the test, serving
// layeredPrecedenceManifest and recording the URL requested. LoadManifest's
// HTTPS branch, URL sanitization and everything after the fetch run as in
// production.
func fakeManifestFetch(t *testing.T) *string {
	t.Helper()
	var fetched string
	orig := manifestFetcher
	manifestFetcher = func(_ context.Context, rawURL string, _ bool) ([]byte, error) {
		fetched = rawURL
		return []byte(layeredPrecedenceManifest), nil
	}
	t.Cleanup(func() { manifestFetcher = orig })
	return &fetched
}

// TestLoadManifest_RemoteRecordsSanitizedSource: a manifest loaded through
// LoadManifest's HTTPS branch records its sanitized location and renders it
// after the ownership marker, while the fetch itself uses the URL as given.
func TestLoadManifest_RemoteRecordsSanitizedSource(t *testing.T) {
	const given = "https://bot:ghp_secret@raw.githubusercontent.com/acme/infra/main/repos.yaml?token=abc#frag"
	fetched := fakeManifestFetch(t)
	m, err := LoadManifest(context.Background(), given)
	require.NoError(t, err)
	require.NoError(t, m.Validate())
	assert.Equal(t, given, *fetched)

	assert.True(t, m.sourceRemote)
	assert.Equal(t, "https://raw.githubusercontent.com/acme/infra/main/repos.yaml", m.sourceURL)

	cfg, ok := m.ResolveConfig("acme", "inherits")
	require.True(t, ok)
	assert.Equal(t, m.sourceURL, cfg.ManifestSource)

	body := mustDesiredManaged(t, m, "acme", "inherits")
	assert.True(t, strings.HasPrefix(string(body),
		managedConfigMarker+"# Source manifest: https://raw.githubusercontent.com/acme/infra/main/repos.yaml\n"))
	assert.NotContains(t, string(body), "ghp_secret")
	assert.NotContains(t, string(body), "token=")
}

// TestLoadManifest_RemotePathTokenNeverReachesManagedFile: a manifest URL
// whose path embeds a credential, however short or hex-shaped, records no
// source unless the URL has a recognized source-host structure, so the token
// cannot reach the managed file (and the target repository's history).
func TestLoadManifest_RemotePathTokenNeverReachesManagedFile(t *testing.T) {
	for _, raw := range []string{
		"https://example.com/t/abc123/repos.yaml",
		"https://example.com/t/0123456789abcdef0123456789abcdef01234567/repos.yaml",
		"https://example.com/t/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef/repos.yaml",
		"https://example.com/t/ghp1234567890abcdefgh/repos.yaml",
		"https://example.com/t/AbCdEfGhIjKlMnOpQrStUvWxYzAbCdEf/repos.yaml",
		"https://example.com/t/%67hp_token/repos.yaml",
		"https://example.com/repos.yaml;token=secret",
	} {
		t.Run(raw, func(t *testing.T) {
			fakeManifestFetch(t)
			m, err := LoadManifest(context.Background(), raw)
			require.NoError(t, err)
			require.NoError(t, m.Validate())
			assert.Empty(t, m.sourceURL)

			body := mustDesiredManaged(t, m, "acme", "inherits")
			assert.Equal(t, managedConfigMarker, string(body[:len(managedConfigMarker)]))
			assert.NotContains(t, string(body), managedConfigSourcePrefix)
			assert.NotContains(t, string(body), "token")
			assert.NotContains(t, string(body), "abc123")
			assert.NotContains(t, string(body), "0123456789abcdef")
			assert.NotContains(t, string(body), "ghp1234567890abcdefgh")
			assert.NotContains(t, string(body), "AbCdEfGh")
		})
	}
}
