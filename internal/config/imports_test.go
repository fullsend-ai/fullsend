package config

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/urlutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func pinnedConfigURL(name, body string) string {
	return pinnedConfigURLRef(name, strings.Repeat("a", 40), body)
}

func pinnedConfigURLRef(name, ref, body string) string {
	digest := sha256.Sum256([]byte(body))
	return fmt.Sprintf("https://configs.example/%s/%s/config.yaml#sha256=%x", name, ref, digest)
}

func TestLoadPerRepoLayersWithImportsOrderAndDedup(t *testing.T) {
	baseImport := "runtime: pi\nroles: [coder]\n"
	overlayImport := "runtime: claude\nroles: [review]\n"
	baseURL := pinnedConfigURL("base", baseImport)
	overlayURL := pinnedConfigURL("overlay", overlayImport)
	baseClean, _, _ := urlutil.ParseIntegrityHash(baseURL)
	overlayClean, _, _ := urlutil.ParseIntegrityHash(overlayURL)
	fetches := map[string]int{}
	fetcher := func(_ context.Context, cleanURL string) ([]byte, error) {
		fetches[cleanURL]++
		switch cleanURL {
		case baseClean:
			return []byte(baseImport), nil
		case overlayClean:
			return []byte(overlayImport), nil
		default:
			return nil, fmt.Errorf("unexpected URL %q", cleanURL)
		}
	}

	base := []byte("imports:\n  - " + baseURL + "\nruntime: opencode\nroles: [triage]\n")
	overlay := []byte("imports:\n  - " + overlayURL + "\n  - " + baseURL + "\nruntime: codex\n")
	cfg, err := loadPerRepoLayersWithImports(overlay, true, base, true, fetcher)
	require.NoError(t, err)
	assert.Equal(t, "codex", cfg.ConfigRuntime())
	assert.Equal(t, []string{"review"}, cfg.ConfigRoles())
	assert.Equal(t, 1, fetches[baseClean], "the same pinned document is applied only once across layers")
	assert.Equal(t, 1, fetches[overlayClean])
	assert.Equal(t, []string{overlayURL, baseURL}, cfg.Imports, "local import directives are preserved for writeback")
}

func TestLoadPerRepoConfigImportsRecursively(t *testing.T) {
	shared := "runtime: pi\n"
	parent := "imports:\n  - " + pinnedConfigURL("shared", shared) + "\nroles: [coder]\n"
	parentURL := pinnedConfigURL("parent", parent)
	sharedURL := pinnedConfigURL("shared", shared)
	parentClean, _, _ := urlutil.ParseIntegrityHash(parentURL)
	sharedClean, _, _ := urlutil.ParseIntegrityHash(sharedURL)
	fetcher := func(_ context.Context, cleanURL string) ([]byte, error) {
		switch cleanURL {
		case parentClean:
			return []byte(parent), nil
		case sharedClean:
			return []byte(shared), nil
		default:
			return nil, fmt.Errorf("unexpected URL %q", cleanURL)
		}
	}

	cfg, err := loadStandalonePerRepoConfig([]byte("imports:\n  - "+parentURL+"\n"), fetcher)
	require.NoError(t, err)
	assert.Equal(t, "pi", cfg.ConfigRuntime())
	assert.Equal(t, []string{"coder"}, cfg.ConfigRoles())
}

func TestLoadPerRepoConfigImportCycle(t *testing.T) {
	url := pinnedConfigURL("a", "roles: [coder]\n")
	cleanURL, digest, _, err := parseConfigImportURL(url)
	require.NoError(t, err)
	identity := cleanURL + "#sha256=" + digest
	fetcher := func(context.Context, string) ([]byte, error) {
		t.Fatal("cycle detection should happen before fetching")
		return nil, nil
	}
	resolver := newConfigImportResolver(context.Background(), fetcher)
	resolver.active[identity] = struct{}{}
	_, err = resolver.loadImport(url, "config.yaml", 0, &perRepoDefaults{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cycle detected")
}

func TestConfigImportLimitsAndFetchErrors(t *testing.T) {
	url := pinnedConfigURL("config", "runtime: pi\n")

	t.Run("count", func(t *testing.T) {
		resolver := newConfigImportResolver(context.Background(), func(context.Context, string) ([]byte, error) {
			t.Fatal("import count should be checked before fetching")
			return nil, nil
		})
		resolver.count = maxConfigImportCount
		_, err := resolver.loadImport(url, "config.yaml", 0, &perRepoDefaults{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "maximum count")
	})

	t.Run("depth", func(t *testing.T) {
		resolver := newConfigImportResolver(context.Background(), func(context.Context, string) ([]byte, error) {
			t.Fatal("import depth should be checked before fetching")
			return nil, nil
		})
		for i := 0; i < maxConfigImportDepth; i++ {
			resolver.active[fmt.Sprint(i)] = struct{}{}
		}
		_, err := resolver.loadImport(url, "config.yaml", 0, &perRepoDefaults{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "maximum nesting depth")
	})

	t.Run("fetch error", func(t *testing.T) {
		resolver := newConfigImportResolver(context.Background(), func(context.Context, string) ([]byte, error) {
			return nil, fmt.Errorf("sensitive query data should not be exposed")
		})
		_, err := resolver.loadImport(url, "config.yaml", 0, &perRepoDefaults{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unable to fetch")
		assert.NotContains(t, err.Error(), "sensitive query data")
	})

	t.Run("size", func(t *testing.T) {
		resolver := newConfigImportResolver(context.Background(), func(context.Context, string) ([]byte, error) {
			return make([]byte, maxConfigImportBytes+1), nil
		})
		_, err := resolver.loadImport(url, "config.yaml", 0, &perRepoDefaults{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exceeds")
	})

	t.Run("invalid imported config", func(t *testing.T) {
		body := "runtime: unknown-runtime\n"
		remoteURL := pinnedConfigURL("invalid", body)
		remoteClean, _, _ := urlutil.ParseIntegrityHash(remoteURL)
		resolver := newConfigImportResolver(context.Background(), func(_ context.Context, requestedURL string) ([]byte, error) {
			if requestedURL != remoteClean {
				t.Fatalf("fetch requested %q, want %q", requestedURL, remoteClean)
			}
			return []byte(body), nil
		})
		_, err := resolver.loadImport(remoteURL, "config.yaml", 0, &perRepoDefaults{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid imported config")
	})

	for name, body := range map[string]string{
		"per-org config": "dispatch:\n  platform: github-actions\n",
		"legacy config":  "create_issues: {}\n",
		"malformed yaml": ":\n- bad",
	} {
		t.Run(name, func(t *testing.T) {
			remoteURL := pinnedConfigURL("invalid", body)
			resolver := newConfigImportResolver(context.Background(), func(context.Context, string) ([]byte, error) {
				return []byte(body), nil
			})
			_, err := resolver.loadImport(remoteURL, "config.yaml", 0, &perRepoDefaults{})
			require.Error(t, err)
		})
	}

}

func TestParseConfigImportURL(t *testing.T) {
	body := "runtime: pi\n"
	valid := pinnedConfigURL("valid", body)
	tests := []struct {
		name    string
		url     string
		wantErr string
	}{
		{name: "valid", url: valid},
		{name: "valid sha256 commit", url: pinnedConfigURLRef("valid", strings.Repeat("b", 64), body)},
		{name: "missing digest", url: "https://configs.example/a/" + strings.Repeat("a", 40) + "/config.yaml", wantErr: "sha256"},
		{name: "non https", url: strings.Replace(valid, "https://", "http://", 1), wantErr: "HTTPS"},
		{name: "missing commit", url: pinnedConfigURLRef("branch-name", "main", body), wantErr: "commit SHA"},
		{name: "wrong port", url: strings.Replace(valid, "configs.example/", "configs.example:444/", 1), wantErr: "port 443"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := parseConfigImportURL(tc.url)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestConfigImportDigestMismatch(t *testing.T) {
	url := pinnedConfigURL("config", "runtime: pi\n")
	fetcher := func(_ context.Context, _ string) ([]byte, error) {
		return []byte("runtime: codex\n"), nil
	}
	_, err := loadStandalonePerRepoConfig([]byte("imports:\n  - "+url+"\n"), fetcher)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SHA-256 mismatch")
}

func TestPerRepoConfigMarshalPreservesImports(t *testing.T) {
	url := pinnedConfigURL("config", "runtime: pi\n")
	cfg := &perRepoConfig{Imports: []string{url}}
	data, err := cfg.Marshal()
	require.NoError(t, err)
	assert.Contains(t, string(data), url)
}

func TestMergeManagedAppendsImportsInLayerOrder(t *testing.T) {
	parentURL := pinnedConfigURL("parent", "runtime: pi\n")
	childURL := pinnedConfigURL("child", "runtime: codex\n")
	merged := MergeManaged(
		&perRepoConfig{Imports: []string{parentURL}},
		&perRepoConfig{Imports: []string{childURL}},
	)
	assert.Equal(t, []string{parentURL, childURL}, asPerRepo(merged).Imports)
}

func TestManagedConfigAcceptsImports(t *testing.T) {
	url := pinnedConfigURL("config", "runtime: pi\n")
	var managed ManagedConfig
	require.NoError(t, yaml.Unmarshal([]byte("imports:\n  - "+url+"\n"), &managed))
	require.NotNil(t, managed.Writer())
	assert.Equal(t, []string{url}, asPerRepo(managed.Writer()).Imports)
}

func TestFetchRemoteConfigRejectsHTTPBeforeNetwork(t *testing.T) {
	_, err := fetchRemoteConfig(context.Background(), "http://example.com/config.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "https")
}
