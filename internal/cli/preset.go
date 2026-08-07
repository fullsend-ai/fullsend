package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	presetFetchTimeout = 30 * time.Second
	presetMaxSize      = 1 << 20 // 1 MiB
)

// stubConfigYAML is the stub overlay committed as .fullsend/config.yaml
// when a preset base is provided via --config. It contains comments
// explaining the layered relationship and minimal empty override fields
// for the adopter to customize.
const stubConfigYAML = `# fullsend per-repo configuration (overlay)
# https://github.com/fullsend-ai/fullsend
#
# This file is the per-repo overlay for fullsend configuration.
# Base settings are provided by config.base.yaml (vendor preset).
# Values set here override the base layer. Omitted fields inherit
# from config.base.yaml, then from compiled-in code defaults.
#
# See ADR 0069 for the layered configuration model.

# Uncomment and customize fields as needed:
# roles: []
# runtime: ""
# agents: []
# kill_switch: false
`

// fetchPreset reads a preset configuration from a local file path or
// an HTTPS URL. Returns the raw content bytes.
func fetchPreset(source string) ([]byte, error) {
	if strings.HasPrefix(strings.ToLower(source), "https://") {
		return fetchPresetHTTPS(source)
	}
	if strings.Contains(source, "://") {
		return nil, fmt.Errorf("unsupported URL scheme in --config %q: only local paths and https:// URLs are supported", source)
	}
	return fetchPresetLocal(source)
}

// fetchPresetLocal reads a preset from the local filesystem.
func fetchPresetLocal(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading preset file %q: %w", path, err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("preset file %q is empty", path)
	}
	if len(data) > presetMaxSize {
		return nil, fmt.Errorf("preset file %q exceeds maximum size (%d bytes)", path, presetMaxSize)
	}
	return data, nil
}

// fetchPresetHTTPS fetches a preset from an HTTPS URL.
func fetchPresetHTTPS(rawURL string) ([]byte, error) {
	client := &http.Client{
		Timeout: presetFetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" {
				return fmt.Errorf("preset redirect to non-HTTPS URL %q is not allowed", req.URL.Redacted())
			}
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	resp, err := client.Get(rawURL) //nolint:gosec // URL is user-provided via --config flag
	if err != nil {
		return nil, fmt.Errorf("fetching preset from %q: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching preset from %q: HTTP %d", rawURL, resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, presetMaxSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading preset from %q: %w", rawURL, err)
	}
	if len(data) > presetMaxSize {
		return nil, fmt.Errorf("preset from %q exceeds maximum size (%d bytes)", rawURL, presetMaxSize)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("preset from %q is empty", rawURL)
	}
	return data, nil
}

// isRemotePreset returns true if the source string is a URL (not a local path).
func isRemotePreset(source string) bool {
	u, err := url.Parse(source)
	if err != nil {
		return false
	}
	return u.Scheme != ""
}

// validatePresetYAML checks that data is syntactically valid YAML.
func validatePresetYAML(data []byte) error {
	var probe any
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return fmt.Errorf("preset is not valid YAML: %w", err)
	}
	return nil
}

// validatePresetHash checks that the SHA-256 hash of data matches the
// expected hex-encoded hash string. Returns nil on match.
func validatePresetHash(data []byte, expectedHex string) error {
	expectedHex = strings.ToLower(strings.TrimSpace(expectedHex))
	if len(expectedHex) != 64 {
		return fmt.Errorf("--config-hash must be a 64-character hex-encoded SHA-256 hash, got %d characters", len(expectedHex))
	}
	if _, err := hex.DecodeString(expectedHex); err != nil {
		return fmt.Errorf("--config-hash is not valid hex: %w", err)
	}

	actual := sha256.Sum256(data)
	actualHex := hex.EncodeToString(actual[:])

	if actualHex != expectedHex {
		return fmt.Errorf("preset hash mismatch: expected %s, got %s", expectedHex, actualHex)
	}
	return nil
}
