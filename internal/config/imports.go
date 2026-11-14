package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/urlutil"
)

const (
	maxConfigImportBytes = 1 << 20
	maxConfigImportCount = 32
	maxConfigImportDepth = 16
	configImportTimeout  = 2 * time.Minute
	configImportFetchTTL = 30 * time.Second
)

var errImportNeedsCommit = errors.New("URL path must include a 40- or 64-character immutable commit SHA")

type configImportFetch func(context.Context, string) ([]byte, error)

type configImportResolver struct {
	ctx     context.Context
	fetch   configImportFetch
	active  map[string]struct{}
	applied map[string]struct{}
	count   int
}

func newConfigImportResolver(ctx context.Context, fetcher configImportFetch) *configImportResolver {
	if fetcher == nil {
		fetcher = fetchRemoteConfig
	}
	return &configImportResolver{
		ctx:     ctx,
		fetch:   fetcher,
		active:  make(map[string]struct{}),
		applied: make(map[string]struct{}),
	}
}

// loadPerRepoLayersWithImports resolves base imports, base values, overlay
// imports, and overlay values in that order. The same resolver tracks pinned
// documents across both layers so a shared dependency is applied only once.
func loadPerRepoLayersWithImports(overlayData []byte, haveOverlay bool, baseData []byte, haveBase bool, fetcher configImportFetch) (*perRepoConfig, error) {
	ctx, cancel := context.WithTimeout(context.Background(), configImportTimeout)
	defer cancel()
	resolver := newConfigImportResolver(ctx, fetcher)

	var parent PerRepoConfigReader = &perRepoDefaults{}
	if haveBase {
		base, err := resolver.loadLayer(baseData, BaseConfigFile, parent, "")
		if err != nil {
			return nil, fmt.Errorf("parsing base config: %w", err)
		}
		parent = base
	}

	if !haveOverlay {
		return &perRepoConfig{parent: parent}, nil
	}
	overlay, err := resolver.loadLayer(overlayData, OverlayConfigFile, parent, "")
	if err != nil {
		return nil, fmt.Errorf("parsing per-repo config: %w", err)
	}
	return overlay, nil
}

func loadStandalonePerRepoConfig(data []byte, fetcher configImportFetch) (*perRepoConfig, error) {
	ctx, cancel := context.WithTimeout(context.Background(), configImportTimeout)
	defer cancel()
	resolver := newConfigImportResolver(ctx, fetcher)
	return resolver.loadLayer(data, OverlayConfigFile, &perRepoDefaults{}, "")
}

// loadLayer resolves a document's imports before returning the document as
// the next layer. identity is empty for a local root and otherwise identifies
// a fetched document by its URL and verified content pin.
func (r *configImportResolver) loadLayer(data []byte, source string, parent PerRepoConfigReader, identity string) (*perRepoConfig, error) {
	if identity != "" {
		r.active[identity] = struct{}{}
		defer delete(r.active, identity)
	}

	var layer perRepoConfig
	if err := unmarshalPerRepo(data, &layer); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	if identity != "" && !IsPerRepoYAML(data) {
		return nil, fmt.Errorf("%s: %w", source, errPerOrgConfig)
	}
	if identity != "" && layer.legacyCreateIssues {
		return nil, fmt.Errorf("%s: %w", source, errLegacyCreateIssues)
	}

	for i, rawURL := range layer.Imports {
		imported, err := r.loadImport(rawURL, source, i, parent)
		if err != nil {
			return nil, err
		}
		if imported != nil {
			parent = imported
		}
	}
	layer.parent = parent
	if identity != "" {
		r.applied[identity] = struct{}{}
	}
	return &layer, nil
}

func (r *configImportResolver) loadImport(rawURL, source string, index int, parent PerRepoConfigReader) (*perRepoConfig, error) {
	cleanURL, expectedHash, host, err := parseConfigImportURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%s imports[%d]: %w", source, index, err)
	}
	identity := cleanURL + "#sha256=" + expectedHash
	if _, ok := r.active[identity]; ok {
		return nil, fmt.Errorf("%s imports[%d]: configuration import cycle detected at %s", source, index, host)
	}
	if _, ok := r.applied[identity]; ok {
		return nil, nil
	}
	if len(r.active) >= maxConfigImportDepth {
		return nil, fmt.Errorf("%s imports[%d]: configuration imports exceed maximum nesting depth (%d)", source, index, maxConfigImportDepth)
	}
	if r.count >= maxConfigImportCount {
		return nil, fmt.Errorf("%s imports[%d]: configuration imports exceed maximum count (%d)", source, index, maxConfigImportCount)
	}
	r.count++

	fetchCtx, cancel := context.WithTimeout(r.ctx, configImportFetchTTL)
	data, err := r.fetch(fetchCtx, cleanURL)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("%s imports[%d]: unable to fetch pinned config from %s", source, index, host)
	}
	if len(data) > maxConfigImportBytes {
		return nil, fmt.Errorf("%s imports[%d]: config from %s exceeds %d bytes", source, index, host, maxConfigImportBytes)
	}
	actual := sha256.Sum256(data)
	if got := hex.EncodeToString(actual[:]); got != expectedHash {
		return nil, fmt.Errorf("%s imports[%d]: SHA-256 mismatch for config from %s", source, index, host)
	}

	remoteSource := "remote config from " + host
	imported, err := r.loadLayer(data, remoteSource, parent, identity)
	if err != nil {
		return nil, fmt.Errorf("%s imports[%d]: %w", source, index, err)
	}
	if err := imported.Validate(); err != nil {
		return nil, fmt.Errorf("%s: invalid imported config: %w", remoteSource, err)
	}
	return imported, nil
}

func parseConfigImportURL(rawURL string) (cleanURL, expectedHash, host string, err error) {
	cleanURL, expectedHash, ok := urlutil.ParseIntegrityHash(rawURL)
	if !ok {
		return "", "", "", fmt.Errorf("URL must include a #sha256= fragment with a 64-character hexadecimal digest")
	}
	if !urlutil.IsURL(cleanURL) {
		return "", "", "", fmt.Errorf("URL must be an absolute HTTPS URL without userinfo")
	}
	parsed, parseErr := url.Parse(cleanURL)
	if parseErr != nil || parsed.Hostname() == "" {
		return "", "", "", fmt.Errorf("URL must include a valid host")
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return "", "", "", fmt.Errorf("URL must use HTTPS port 443")
	}
	if !hasCommitSHA(parsed.Path) {
		return "", "", "", errImportNeedsCommit
	}
	return cleanURL, expectedHash, strings.ToLower(parsed.Hostname()), nil
}

func hasCommitSHA(path string) bool {
	for _, segment := range strings.Split(path, "/") {
		if len(segment) != 40 && len(segment) != 64 {
			continue
		}
		if _, err := hex.DecodeString(segment); err == nil && strings.ToLower(segment) == segment {
			return true
		}
	}
	return false
}

func fetchRemoteConfig(ctx context.Context, cleanURL string) ([]byte, error) {
	parsed, err := url.Parse(cleanURL)
	if err != nil {
		return nil, err
	}
	policy := fetch.FetchPolicy{
		AllowedDomains: []string{strings.ToLower(parsed.Hostname())},
		MaxSizeBytes:   maxConfigImportBytes,
		Timeout:        configImportFetchTTL,
	}
	return fetch.FetchURL(ctx, cleanURL, policy)
}
