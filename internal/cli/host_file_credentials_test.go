package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// pemHeader is assembled so the fixture is not itself a PEM block.
const pemHeader = "-----BEGIN " + "PRIVATE KEY-----"

var hostFileFixtures = map[string]string{
	"service_account":                  `{"type": "service_account", "project_id": "example", "private_key_id": "dummy", "private_key": "dummy", "client_email": "agent@example.invalid"}`,
	"authorized_user":                  `{"type": "authorized_user", "client_id": "dummy", "client_secret": "dummy", "refresh_token": "dummy"}`,
	"external_account":                 `{"type": "external_account", "audience": "example", "credential_source": {"file": "/tmp/token"}}`,
	"impersonated_service_account":     `{"type": "impersonated_service_account", "service_account_impersonation_url": "https://example.invalid", "source_credentials": {"type": "authorized_user"}}`,
	"impersonated over external":       `{"type": "impersonated_service_account", "source_credentials": {"type": "impersonated_service_account", "source_credentials": {"type": "external_account", "credential_source": {"file": "/tmp/token"}}}}`,
	"impersonated without source":      `{"type": "impersonated_service_account"}`,
	"pem":                              "# key\n" + pemHeader + "\nZHVtbXk=\n-----END " + "PRIVATE KEY-----\n",
	"plain text":                       "export FOO=bar\n",
	"empty":                            "",
	"other json":                       `{"type": "something_else", "project_id": "example"}`,
	"external_account_authorized_user": `{"type": "external_account_authorized_user", "audience": "example", "client_id": "dummy", "client_secret": "dummy", "refresh_token": "dummy"}`,
	"authorized_user with BOM":         "\uFEFF" + `{"type": "authorized_user", "client_id": "dummy", "client_secret": "dummy", "refresh_token": "dummy"}`,
	"expand to pem":                    "KEY=\"${FULLSEND_TEST_EXPAND_PEM}\"\n",
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
		{"external_account_authorized_user", hostFileExternalAuthUser, true},
		{"authorized_user with BOM", hostFileAuthorizedUser, true},
		{"expand to pem", hostFileUnknown, false},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			got, err := classifyHostFile(writeHostFileFixture(t, tt.fixture), false)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.durable, got.durable())
		})
	}

	t.Run("unreadable path", func(t *testing.T) {
		got, err := classifyHostFile(filepath.Join(t.TempDir(), "missing.json"), false)
		require.Error(t, err)
		assert.Equal(t, hostFileUnknown, got)
	})

	t.Run("directory", func(t *testing.T) {
		got, err := classifyHostFile(t.TempDir(), false)
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
				assert.Equal(t, []string{"host file " + path + " (dest " + dest + "): " + tt.wantLine}, lines)
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
		assert.Contains(t, err.Error(), "host_files[0]: host file "+sa+" (src ${FULLSEND_TEST_SA_HOST_FILE}, dest /tmp/.gcp-credentials.json): service_account key")
		assert.Contains(t, err.Error(), "host_files[2]: host file "+pem+" (dest /tmp/key.pem): PEM private key")
		assert.NotContains(t, err.Error(), "host_files[1]")
		assert.NotContains(t, err.Error(), "host_files[3]")
		assert.Equal(t, []string{"host file " + allowed + " (dest /tmp/adc.json): authorized_user credential (lifetime not controlled by this run)"}, lines)
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

func TestClassifyHostFileExpand(t *testing.T) {
	t.Setenv("FULLSEND_TEST_EXPAND_PEM", pemHeader)
	path := writeHostFileFixture(t, "expand to pem")

	raw, err := classifyHostFile(path, false)
	require.NoError(t, err)
	assert.Equal(t, hostFileUnknown, raw)

	expanded, err := classifyHostFile(path, true)
	require.NoError(t, err)
	assert.Equal(t, hostFilePrivateKey, expanded, "expand: true is classified as uploaded")
}

func TestCheckHostFileCredentialsNamesSrc(t *testing.T) {
	sa := writeHostFileFixture(t, "service_account")
	t.Setenv("FULLSEND_TEST_SA_SRC", sa)
	h := &harness.Harness{HostFiles: []harness.HostFile{{Src: "${FULLSEND_TEST_SA_SRC}", Dest: "/tmp/.cred.json"}}}
	_, err := checkHostFileCredentials(h)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "host file "+sa+" (src ${FULLSEND_TEST_SA_SRC}, dest /tmp/.cred.json)")
}

func TestReportHostFileCredentialsPrintsEachLineOnce(t *testing.T) {
	sa := writeHostFileFixture(t, "service_account")
	h := &harness.Harness{HostFiles: []harness.HostFile{{Src: sa, Dest: "/tmp/.cred.json", AllowDurable: true}}}
	var out bytes.Buffer
	printer := ui.New(&out)
	reported := map[string]bool{}
	require.NoError(t, reportHostFileCredentials(h, printer, reported))
	require.NoError(t, reportHostFileCredentials(h, printer, reported))
	assert.Equal(t, 1, strings.Count(out.String(), "service_account key (lifetime not controlled by this run)"), out.String())

	h.HostFiles[0].AllowDurable = false
	require.Error(t, reportHostFileCredentials(h, printer, reported))
	assert.Contains(t, out.String(), "Host file credential validation failed")
}

// A durable mount without allow_durable stops runAgent before the
// pre-script; a key the pre-script writes is caught before the sandbox.
func TestRunAgent_DurableHostFileGate(t *testing.T) {
	sa := writeHostFileFixture(t, "service_account")
	written := filepath.Join(t.TempDir(), "written-key.json")
	for _, tc := range []struct {
		name, mount, preScript string
		wantErr                string
		wantPreScript          bool
		wantLine               string
	}{
		{name: "durable mount without opt-in fails before the pre-script",
			mount:   "  - src: " + sa + "\n    dest: /tmp/.gcp-credentials.json\n",
			wantErr: "set allow_durable: true on the entry"},
		{name: "opt-in runs the pre-script and prints the line",
			mount:         "  - src: " + sa + "\n    dest: /tmp/.gcp-credentials.json\n    allow_durable: true\n",
			wantPreScript: true, wantLine: "host file " + sa + " (dest /tmp/.gcp-credentials.json): service_account key (lifetime not controlled by this run)"},
		{name: "key written by the pre-script is caught before the sandbox",
			mount:         "  - src: " + written + "\n    dest: /tmp/.gcp-credentials.json\n    optional: true\n",
			preScript:     "printf '%s' '" + hostFileFixtures["service_account"] + "' > " + written + "\n",
			wantPreScript: true, wantErr: "set allow_durable: true on the entry"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usePreScriptStub(t)
			t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
			t.Setenv("FULLSEND_RUNTIME", "")
			marker := filepath.Join(t.TempDir(), "pre-script-ran")
			dir := newVertexChildDir(t, marker, "dummy", "openai/gpt-5.6-luna", "host_files:\n"+tc.mount, "")
			harnessPath := filepath.Join(dir, "harness", "code.yaml")
			data, err := os.ReadFile(harnessPath)
			require.NoError(t, err)
			pre := writePreScript(t, "touch "+marker+"\n"+tc.preScript)
			data = []byte(regexp.MustCompile(`(?m)^pre_script: .*$`).ReplaceAllString(string(data), "pre_script: "+pre))
			require.NoError(t, os.WriteFile(harnessPath, data, 0o644))

			var out bytes.Buffer
			err = runAgent(context.Background(), "code", dir, "", t.TempDir(), "", nil, false, "", "", "",
				resolveFlags{maxDepth: 10, maxResources: 50}, statusOpts{}, ui.New(&out), false, runOverrideFlags{})
			require.Error(t, err, "the stub never creates a sandbox")
			if tc.wantErr != "" {
				assert.Contains(t, err.Error(), tc.wantErr)
			} else {
				assert.NotContains(t, err.Error(), "allow_durable")
			}
			if tc.wantPreScript {
				assert.FileExists(t, marker)
			} else {
				assert.NoFileExists(t, marker)
			}
			if tc.wantLine != "" {
				assert.Equal(t, 1, strings.Count(out.String(), tc.wantLine), out.String())
			}
		})
	}
}

// bootstrapEnv checks the bytes it is about to upload, so a source that
// changed after the pre-sandbox checks is still refused.
func TestBootstrapEnv_RefusesDurableHostFileAtUpload(t *testing.T) {
	binDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	t.Setenv("PATH", binDir+string(filepath.ListSeparator)+os.Getenv("PATH"))
	t.Setenv("FULLSEND_TEST_EXPAND_PEM", pemHeader)

	sa := writeHostFileFixture(t, "service_account")
	expand := writeHostFileFixture(t, "expand to pem")
	for _, tc := range []struct {
		name    string
		hf      harness.HostFile
		wantErr string
	}{
		{"plain key without opt-in", harness.HostFile{Src: sa, Dest: "/tmp/.cred.json"}, "service_account key"},
		{"expanded key without opt-in", harness.HostFile{Src: expand, Dest: "/sandbox/workspace/.env.d/key.env", Expand: true}, "PEM private key"},
		{"plain key with opt-in", harness.HostFile{Src: sa, Dest: "/tmp/.cred.json", AllowDurable: true}, ""},
		{"expanded key with opt-in", harness.HostFile{Src: expand, Dest: "/sandbox/workspace/.env.d/key.env", Expand: true, AllowDurable: true}, ""},
		{"expanded non-credential", harness.HostFile{Src: writeHostFileFixture(t, "plain text"), Dest: "/sandbox/workspace/.env.d/a.env", Expand: true}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &harness.Harness{Agent: "agents/test.md", HostFiles: []harness.HostFile{tc.hf}}
			err := bootstrapEnv("test-sandbox", "/workspace/repo", h, nil)
			if tc.wantErr == "" {
				if err != nil {
					assert.NotContains(t, err.Error(), "allow_durable")
				}
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "host_files[0]")
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Contains(t, err.Error(), "set allow_durable: true on the entry")
		})
	}
}

func TestClassifyHostFileRefusesNonRegular(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("FIFOs need a POSIX system")
	}
	fifo := filepath.Join(t.TempDir(), "fifo")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	shape, err := classifyHostFile(fifo, false)
	require.Error(t, err, "a FIFO is not read, so the check cannot block")
	assert.Contains(t, err.Error(), "not a regular file")
	assert.Equal(t, hostFileUnknown, shape)
}
