package config

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/vaultsecrets"
)

// Secret connection kinds and the reserved native connection (ADR 0155).
const (
	// SecretConnectionKindVault selects HashiCorp Vault KV v2 through the
	// job's workload identity.
	SecretConnectionKindVault = "vault"
	// NativeSecretConnection is the built-in connection name that selects
	// the forge-native source. It cannot be declared under connections.
	NativeSecretConnection = "native"
)

// VaultEligibleSecretNames returns the runner secrets that may select a
// non-native connection (ADR 0155): user-managed values whose lifecycle
// Fullsend does not create or rotate.
func VaultEligibleSecretNames() []string {
	return []string{
		"FULLSEND_GITHUB_RUNNER_SECRETS",
		"JIRA_TOKEN",
		"OTEL_EXPORTER_OTLP_HEADERS",
		"OTEL_EXPORTER_OTLP_TRACES_HEADERS",
	}
}

// SecretConnection is one entry of the top-level connections mapping: a
// named connection to a secret service. Kind is required; the vault_*
// fields are valid only with kind: vault.
type SecretConnection struct {
	Kind           string `yaml:"kind"`
	VaultAddress   string `yaml:"vault_address,omitempty"`
	VaultNamespace string `yaml:"vault_namespace,omitempty"`
	VaultAuthPath  string `yaml:"vault_auth_path,omitempty"`
	VaultRole      string `yaml:"vault_role,omitempty"`
	VaultAudience  string `yaml:"vault_audience,omitempty"`
}

// SecretSource is one entry of the top-level secrets mapping: the
// connection a registered runner secret uses and, for Vault, its ordered
// KV v2 read paths.
type SecretSource struct {
	// Connection names an entry of connections, or "native". Empty selects
	// the forge-native source.
	Connection string `yaml:"connection,omitempty"`
	// VaultSecrets is the ordered list of KV v2 API read paths, relative to
	// /v1/ and including the mount's /data/ segment.
	VaultSecrets []string `yaml:"vault_secrets,omitempty"`
}

// SecretSourcesReader exposes the layered connections and secrets
// mappings. It is embedded in PerRepoConfigReader; callers holding only a
// ConfigReader type-assert to it.
type SecretSourcesReader interface {
	// ConfigConnections returns the effective connections mapping, merged
	// by connection name through the parent chain. A more-specific layer
	// replaces a whole entry.
	ConfigConnections() map[string]SecretConnection
	// ConfigSecrets returns the effective secrets mapping, merged by secret
	// name through the parent chain. A more-specific layer replaces a whole
	// entry, including its vault_secrets list.
	ConfigSecrets() map[string]SecretSource
	// ValidateSecretSources checks the effective mappings.
	ValidateSecretSources() error
}

// connectionNameRe restricts connection aliases to simple identifiers so
// a name is safe to print in logs and errors.
var connectionNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)

// ConfigConnections implements SecretSourcesReader.
func (c *perRepoConfig) ConfigConnections() map[string]SecretConnection {
	var parent map[string]SecretConnection
	if c.parent != nil {
		parent = c.parent.ConfigConnections()
	}
	return mergeByName(parent, c.Connections)
}

// ConfigSecrets implements SecretSourcesReader.
func (c *perRepoConfig) ConfigSecrets() map[string]SecretSource {
	var parent map[string]SecretSource
	if c.parent != nil {
		parent = c.parent.ConfigSecrets()
	}
	merged := mergeByName(parent, c.Secrets)
	for k, v := range merged {
		v.VaultSecrets = slices.Clone(v.VaultSecrets)
		merged[k] = v
	}
	return merged
}

// mergeByName returns parent with each local entry replacing the parent
// entry of the same name, or nil when both are empty.
func mergeByName[T any](parent, local map[string]T) map[string]T {
	if len(parent) == 0 && len(local) == 0 {
		return nil
	}
	merged := make(map[string]T, len(parent)+len(local))
	for k, v := range parent {
		merged[k] = v
	}
	for k, v := range local {
		merged[k] = v
	}
	return merged
}

// ValidateSecretSources implements SecretSourcesReader: it checks the
// effective connections and secrets, including that every selected
// connection is declared.
func (c *perRepoConfig) ValidateSecretSources() error {
	return validateSecretSources(c.ConfigConnections(), c.ConfigSecrets(), true)
}

// ValidateRuntimeSecretSources validates the connections and secrets of a
// config loaded with LoadConfig. Readers without the mappings pass.
func ValidateRuntimeSecretSources(r ConfigReader) error {
	if v, ok := r.(SecretSourcesReader); ok {
		return v.ValidateSecretSources()
	}
	return nil
}

// validateSecretSources checks connections and secrets. With
// checkRefs false it skips the check that a selected connection is
// declared, so a single layer can be validated before its parents are
// known.
func validateSecretSources(conns map[string]SecretConnection, secrets map[string]SecretSource, checkRefs bool) error {
	for _, name := range sortedKeys(conns) {
		if err := validateSecretConnection(name, conns[name]); err != nil {
			return err
		}
	}
	eligible := VaultEligibleSecretNames()
	for _, name := range sortedKeys(secrets) {
		s := secrets[name]
		if !slices.Contains(eligible, name) {
			return fmt.Errorf("secrets: %q is not a secret that supports source selection: must be one of %s", name, strings.Join(eligible, ", "))
		}
		kind := ""
		switch {
		case s.Connection == "" || s.Connection == NativeSecretConnection:
		case !checkRefs:
			if conn, ok := conns[s.Connection]; ok {
				kind = conn.Kind
			} else {
				// The connection may come from another layer; only the
				// vault_secrets shape can be checked here.
				kind = SecretConnectionKindVault
			}
		default:
			conn, ok := conns[s.Connection]
			if !ok {
				return fmt.Errorf("secrets.%s: connection %q is not declared under connections", name, s.Connection)
			}
			kind = conn.Kind
		}
		if kind != SecretConnectionKindVault {
			if len(s.VaultSecrets) > 0 {
				return fmt.Errorf("secrets.%s: vault_secrets is valid only when the selected connection has kind: vault", name)
			}
			continue
		}
		if len(s.VaultSecrets) == 0 {
			return fmt.Errorf("secrets.%s: vault_secrets must list at least one Vault KV v2 path", name)
		}
		for i, p := range s.VaultSecrets {
			if err := vaultsecrets.ValidateTemplate(p); err != nil {
				return fmt.Errorf("secrets.%s.vault_secrets[%d]: %w", name, i, err)
			}
		}
	}
	return nil
}

// validateSecretConnection checks one connections entry.
func validateSecretConnection(name string, c SecretConnection) error {
	if name == NativeSecretConnection {
		return fmt.Errorf("connections: %q is a reserved built-in connection name and cannot be declared", name)
	}
	if !connectionNameRe.MatchString(name) {
		return fmt.Errorf("connections: invalid connection name %q", name)
	}
	switch c.Kind {
	case "":
		return fmt.Errorf("connections.%s: kind is required", name)
	case SecretConnectionKindVault:
	default:
		return fmt.Errorf("connections.%s: unknown kind %q: must be %s", name, c.Kind, SecretConnectionKindVault)
	}
	if c.VaultAddress == "" {
		return fmt.Errorf("connections.%s: vault_address is required for kind: vault", name)
	}
	if err := vaultsecrets.ValidateAddress(c.VaultAddress); err != nil {
		return fmt.Errorf("connections.%s: %w", name, err)
	}
	if err := vaultsecrets.ValidateAuthPath(c.VaultAuthPath); err != nil {
		return fmt.Errorf("connections.%s: %w", name, err)
	}
	return nil
}
