package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
)

// pemHeader is assembled so the fixture is not itself a PEM block.
const pemHeader = "-----BEGIN " + "PRIVATE KEY-----"

var hostFileFixtures = map[string]string{
	"service_account":              `{"type": "service_account", "project_id": "example", "private_key_id": "dummy", "private_key": "dummy", "client_email": "agent@example.invalid"}`,
	"authorized_user":              `{"type": "authorized_user", "client_id": "dummy", "client_secret": "dummy", "refresh_token": "dummy"}`,
	"external_account":             `{"type": "external_account", "audience": "example", "credential_source": {"file": "/tmp/token"}}`,
	"impersonated_service_account": `{"type": "impersonated_service_account", "service_account_impersonation_url": "https://example.invalid", "source_credentials": {"type": "authorized_user"}}`,
	"impersonated over external":   `{"type": "impersonated_service_account", "source_credentials": {"type": "impersonated_service_account", "source_credentials": {"type": "external_account", "credential_source": {"file": "/tmp/token"}}}}`,
	"impersonated without source":  `{"type": "impersonated_service_account"}`,
	"pem":                          "# key\n" + pemHeader + "\nZHVtbXk=\n-----END " + "PRIVATE KEY-----\n",
	"plain text":                   "export FOO=bar\n",
	"empty":                        "",
	"other json":                   `{"type": "something_else", "project_id": "example"}`,
}

func writeHostFileFixture(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture")
	require.NoError(t, os.WriteFile(path, []byte(hostFileFixtures[name]), 0o600))
	return path
}

func TestClassifyHostFile(t *testing.T) {
	tests := []struct {
		fixture string
		want    hostFileShape
		durable bool
	}{
		{"service_account", hostFileServiceAccount, true},
		{"authorized_user", hostFileAuthorizedUser, true},
		{"external_account", hostFileExternalAccount, false},
		{"impersonated_service_account", hostFileImpersonatedSA, true},
		{"impersonated over external", hostFileExternalAccount, false},
		{"impersonated without source", hostFileImpersonatedSA, true},
		{"pem", hostFilePrivateKey, true},
		{"plain text", hostFileUnknown, false},
		{"empty", hostFileUnknown, false},
		{"other json", hostFileUnknown, false},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			got, err := classifyHostFile(writeHostFileFixture(t, tt.fixture))
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.durable, got.durable())
		})
	}

	t.Run("unreadable path", func(t *testing.T) {
		got, err := classifyHostFile(filepath.Join(t.TempDir(), "missing.json"))
		require.Error(t, err)
		assert.Equal(t, hostFileUnknown, got)
	})

	t.Run("directory", func(t *testing.T) {
		got, err := classifyHostFile(t.TempDir())
		require.Error(t, err)
		assert.Equal(t, hostFileUnknown, got)
	})
}

func TestCheckHostFileCredentials(t *testing.T) {
	const dest = "/tmp/.cred.json"
	tests := []struct {
		fixture  string
		wantErr  string
		wantLine string
	}{
		{"service_account", "service_account key", "service_account key (lifetime not controlled by this run)"},
		{"authorized_user", "authorized_user credential", "authorized_user credential (lifetime not controlled by this run)"},
		{"impersonated_service_account", "impersonated_service_account credential", "impersonated_service_account credential (lifetime not controlled by this run)"},
		{"pem", "PEM private key", "PEM private key (lifetime not controlled by this run)"},
		{"external_account", "", ""},
		{"impersonated over external", "", ""},
		{"plain text", "", ""},
		{"empty", "", ""},
		{"other json", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			path := writeHostFileFixture(t, tt.fixture)

			lines, err := checkHostFileCredentials(&harness.Harness{HostFiles: []harness.HostFile{{Src: path, Dest: dest}}})
			assert.Empty(t, lines)
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "host_files[0]")
				assert.Contains(t, err.Error(), path)
				assert.Contains(t, err.Error(), "dest "+dest)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Contains(t, err.Error(), "allow_durable: true")
			}

			lines, err = checkHostFileCredentials(&harness.Harness{HostFiles: []harness.HostFile{{Src: path, Dest: dest, AllowDurable: true}}})
			require.NoError(t, err)
			if tt.wantLine == "" {
				assert.Empty(t, lines)
			} else {
				assert.Equal(t, []string{"host file " + path + ": " + tt.wantLine}, lines)
			}
		})
	}

	t.Run("unreadable path is left to bootstrapEnv", func(t *testing.T) {
		h := &harness.Harness{HostFiles: []harness.HostFile{{Src: filepath.Join(t.TempDir(), "missing.json"), Dest: dest}}}
		lines, err := checkHostFileCredentials(h)
		require.NoError(t, err)
		assert.Empty(t, lines)
	})

	t.Run("skipped optional and empty sources", func(t *testing.T) {
		t.Setenv("FULLSEND_TEST_EMPTY_HOST_FILE", "")
		h := &harness.Harness{HostFiles: []harness.HostFile{
			{Src: filepath.Join(t.TempDir(), "missing.json"), Dest: dest, Optional: true},
			{Src: "${FULLSEND_TEST_EMPTY_HOST_FILE}", Dest: dest, Optional: true},
		}}
		lines, err := checkHostFileCredentials(h)
		require.NoError(t, err)
		assert.Empty(t, lines)
	})

	t.Run("expands src and reports every durable mount", func(t *testing.T) {
		sa := writeHostFileFixture(t, "service_account")
		pem := writeHostFileFixture(t, "pem")
		allowed := writeHostFileFixture(t, "authorized_user")
		t.Setenv("FULLSEND_TEST_SA_HOST_FILE", sa)
		h := &harness.Harness{HostFiles: []harness.HostFile{
			{Src: "${FULLSEND_TEST_SA_HOST_FILE}", Dest: "/tmp/.gcp-credentials.json"},
			{Src: writeHostFileFixture(t, "plain text"), Dest: "/sandbox/workspace/.env.d/agent.env"},
			{Src: pem, Dest: "/tmp/key.pem"},
			{Src: allowed, Dest: "/tmp/adc.json", AllowDurable: true},
		}}
		lines, err := checkHostFileCredentials(h)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "2 durable credential host file(s)")
		assert.Contains(t, err.Error(), "host_files[0]: host file "+sa+" (dest /tmp/.gcp-credentials.json): service_account key")
		assert.Contains(t, err.Error(), "host_files[2]: host file "+pem+" (dest /tmp/key.pem): PEM private key")
		assert.NotContains(t, err.Error(), "host_files[1]")
		assert.NotContains(t, err.Error(), "host_files[3]")
		assert.Equal(t, []string{"host file " + allowed + ": authorized_user credential (lifetime not controlled by this run)"}, lines)
	})
}

func TestHostFileAllowDurableYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harness.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`agent: agents/test.md
role: coder
host_files:
  - src: /tmp/key.json
    dest: /tmp/.cred.json
    allow_durable: true
  - src: /tmp/other.json
    dest: /tmp/.other.json
`), 0o600))
	h, err := harness.Load(path)
	require.NoError(t, err)
	require.Len(t, h.HostFiles, 2)
	assert.True(t, h.HostFiles[0].AllowDurable)
	assert.False(t, h.HostFiles[1].AllowDurable)
}
