package config

import (
	"strings"
	"testing"
)

const vaultBase = `
connections:
  vault:
    kind: vault
    vault_address: https://vault.example.com
  other:
    kind: vault
    vault_address: https://other.example.com
    vault_role: base-role
secrets:
  JIRA_TOKEN:
    connection: vault
    vault_secrets:
      - kv/data/fullsend/repositories/{{repo.host}}/{{repo.full_name}}/jira
      - kv/data/fullsend/organizations/{{repo.host}}/{{org}}/shared/jira
  OTEL_EXPORTER_OTLP_HEADERS:
    connection: vault
    vault_secrets: [kv/data/otel]
`

func loadLayered(t *testing.T, overlay, base string) PerRepoConfigWriter {
	t.Helper()
	var baseData []byte
	if base != "" {
		baseData = []byte(base)
	}
	cfg, err := ParsePerRepoConfigWriterLayered([]byte(overlay), baseData)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return cfg
}

func TestSecretSourcesLayering(t *testing.T) {
	cfg := loadLayered(t, `
connections:
  other:
    kind: vault
    vault_address: https://override.example.com
secrets:
  JIRA_TOKEN:
    connection: other
    vault_secrets: [kv/data/overlay/jira]
  OTEL_EXPORTER_OTLP_TRACES_HEADERS:
    connection: native
`, vaultBase)
	r := cfg.(SecretSourcesReader)
	if err := r.ValidateSecretSources(); err != nil {
		t.Fatalf("ValidateSecretSources: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	conns := r.ConfigConnections()
	if got := conns["other"]; got.VaultAddress != "https://override.example.com" || got.VaultRole != "" {
		t.Fatalf("other connection = %+v: overlay must replace the whole entry", got)
	}
	if conns["vault"].VaultAddress != "https://vault.example.com" {
		t.Fatalf("vault connection not inherited: %+v", conns["vault"])
	}
	secrets := r.ConfigSecrets()
	if got := secrets["JIRA_TOKEN"]; got.Connection != "other" || len(got.VaultSecrets) != 1 || got.VaultSecrets[0] != "kv/data/overlay/jira" {
		t.Fatalf("JIRA_TOKEN = %+v: overlay must replace the whole entry and list", got)
	}
	if got := secrets["OTEL_EXPORTER_OTLP_HEADERS"]; got.Connection != "vault" {
		t.Fatalf("OTEL_EXPORTER_OTLP_HEADERS not inherited: %+v", got)
	}
	if got := secrets["OTEL_EXPORTER_OTLP_TRACES_HEADERS"]; got.Connection != NativeSecretConnection {
		t.Fatalf("traces headers = %+v", got)
	}
	// The returned list is a copy.
	secrets["JIRA_TOKEN"].VaultSecrets[0] = "mutated"
	if r.ConfigSecrets()["JIRA_TOKEN"].VaultSecrets[0] != "kv/data/overlay/jira" {
		t.Fatal("ConfigSecrets returned a shared slice")
	}
}

func TestSecretSourcesAbsent(t *testing.T) {
	cfg := loadLayered(t, "version: \"1\"\n", "")
	r := cfg.(SecretSourcesReader)
	if r.ConfigConnections() != nil || r.ConfigSecrets() != nil {
		t.Fatal("expected no connections or secrets")
	}
	if err := ValidateRuntimeSecretSources(cfg); err != nil {
		t.Fatalf("ValidateRuntimeSecretSources: %v", err)
	}
	if err := ValidateRuntimeSecretSources(&perRepoDefaults{}); err != nil {
		t.Fatalf("defaults: %v", err)
	}
}

func TestSecretSourcesMarshalRoundTrip(t *testing.T) {
	cfg := loadLayered(t, vaultBase, "")
	data, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"connections:", "kind: vault", "secrets:", "JIRA_TOKEN:", "{{repo.full_name}}"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("marshaled config missing %q:\n%s", want, data)
		}
	}
}

func TestSecretSourcesValidation(t *testing.T) {
	tests := []struct {
		name    string
		overlay string
		base    string
		wantErr string
	}{
		{name: "native reserved", overlay: "connections:\n  native:\n    kind: vault\n    vault_address: https://v\n", wantErr: "reserved"},
		{name: "missing kind", overlay: "connections:\n  v:\n    vault_address: https://v\n", wantErr: "kind is required"},
		{name: "unknown kind", overlay: "connections:\n  v:\n    kind: aws\n", wantErr: "unknown kind"},
		{name: "missing address", overlay: "connections:\n  v:\n    kind: vault\n", wantErr: "vault_address is required"},
		{name: "http address", overlay: "connections:\n  v:\n    kind: vault\n    vault_address: http://v\n", wantErr: "https"},
		{name: "bad auth path", overlay: "connections:\n  v:\n    kind: vault\n    vault_address: https://v\n    vault_auth_path: auth/jwt\n", wantErr: "auth/ prefix"},
		{name: "bad connection name", overlay: "connections:\n  \"bad name\":\n    kind: vault\n    vault_address: https://v\n", wantErr: "invalid connection name"},
		{name: "ineligible secret", overlay: "secrets:\n  GH_TOKEN:\n    connection: native\n", wantErr: "not a secret that supports source selection"},
		{name: "undeclared connection", overlay: "secrets:\n  JIRA_TOKEN:\n    connection: vault\n    vault_secrets: [kv/data/x]\n", wantErr: "not declared"},
		{name: "vault without paths", overlay: "secrets:\n  JIRA_TOKEN:\n    connection: vault\n", base: vaultBase, wantErr: "at least one"},
		{name: "native with paths", overlay: "secrets:\n  JIRA_TOKEN:\n    connection: native\n    vault_secrets: [kv/data/x]\n", wantErr: "valid only when"},
		{name: "omitted connection with paths", overlay: "secrets:\n  JIRA_TOKEN:\n    vault_secrets: [kv/data/x]\n", wantErr: "valid only when"},
		{name: "bad template", overlay: "secrets:\n  JIRA_TOKEN:\n    connection: vault\n    vault_secrets: [\"kv/data/{{repo.id}}\"]\n", base: vaultBase, wantErr: "unknown variable"},
		{name: "path escape", overlay: "secrets:\n  JIRA_TOKEN:\n    connection: vault\n    vault_secrets: [kv/data/../sys]\n", base: vaultBase, wantErr: "\"..\""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := loadLayered(t, tt.overlay, tt.base)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate error = %v, want containing %q", err, tt.wantErr)
			}
			if rerr := ValidateRuntimeSecretSources(cfg); rerr == nil {
				t.Fatal("ValidateRuntimeSecretSources accepted an invalid config")
			}
		})
	}
}

func TestSecretSourcesLocalValidationAllowsParentConnection(t *testing.T) {
	// A single layer may select a connection another layer declares; only
	// the merged validation checks the reference.
	if err := validateSecretSources(nil, map[string]SecretSource{
		"JIRA_TOKEN": {Connection: "vault", VaultSecrets: []string{"kv/data/x"}},
	}, false); err != nil {
		t.Fatalf("local validation: %v", err)
	}
	if err := validateSecretSources(nil, map[string]SecretSource{
		"JIRA_TOKEN": {Connection: "vault"},
	}, false); err == nil {
		t.Fatal("local validation accepted a Vault selection without paths")
	}
	if err := validateSecretSources(map[string]SecretConnection{"v": {Kind: "vault", VaultAddress: "https://v"}}, map[string]SecretSource{
		"JIRA_TOKEN": {Connection: "v", VaultSecrets: []string{"kv/data/x"}},
	}, false); err != nil {
		t.Fatalf("local validation with local connection: %v", err)
	}
}
