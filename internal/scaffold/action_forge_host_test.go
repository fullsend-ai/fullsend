package scaffold

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type compositeActionStep struct {
	Name string            `yaml:"name"`
	Env  map[string]string `yaml:"env"`
	Run  string            `yaml:"run"`
}

// loadCompositeActionSteps returns the steps of a composite action under the
// repository root, keyed by step name.
func loadCompositeActionSteps(t *testing.T, relPath string) map[string]compositeActionStep {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", relPath))
	require.NoError(t, err)
	var action struct {
		Runs struct {
			Steps []compositeActionStep `yaml:"steps"`
		} `yaml:"runs"`
	}
	require.NoError(t, yaml.Unmarshal(content, &action))
	steps := make(map[string]compositeActionStep, len(action.Runs.Steps))
	for _, step := range action.Runs.Steps {
		steps[step.Name] = step
	}
	return steps
}

func compositeActionStepNamed(t *testing.T, relPath, name string) compositeActionStep {
	t.Helper()
	step, ok := loadCompositeActionSteps(t, relPath)[name]
	require.True(t, ok, "%s must have step %q", relPath, name)
	require.NotEmpty(t, step.Run, "%s step %q must have a run script", relPath, name)
	return step
}

// TestFullsendActionsDoNotHardcodeGitHubDotCom ensures the install paths of
// the agent action and the CLI install action use the job's own host. The job
// token belongs to that host; sending it to github.com fails on any other
// host (GHES, emulators) and leaks the token to a host that did not issue it.
func TestFullsendActionsDoNotHardcodeGitHubDotCom(t *testing.T) {
	for _, relPath := range []string{
		"action.yml",
		".github/actions/install-fullsend-cli/action.yml",
	} {
		t.Run(relPath, func(t *testing.T) {
			for name, step := range loadCompositeActionSteps(t, relPath) {
				for _, literal := range []string{"api.github.com", "@github.com", "https://github.com/"} {
					assert.NotContains(t, step.Run, literal,
						"%s step %q must derive the host from github.api_url / github.server_url", relPath, name)
				}
			}
		})
	}

	detect := compositeActionStepNamed(t, "action.yml", "Detect install method")
	assert.Equal(t, "${{ github.api_url }}", detect.Env["API_URL"])
	download := compositeActionStepNamed(t, "action.yml", "Download release binary")
	assert.Equal(t, "${{ github.server_url }}", download.Env["SERVER_URL"])
	clone := compositeActionStepNamed(t, "action.yml", "Clone fullsend at ref for source build")
	assert.Equal(t, "${{ github.server_url }}", clone.Env["SERVER_URL"])
	cliClone := compositeActionStepNamed(t, ".github/actions/install-fullsend-cli/action.yml",
		"Clone fullsend at workflow SHA for source build")
	assert.Equal(t, "${{ github.server_url }}", cliClone.Env["SERVER_URL"])
}

// TestActionDetectInstallMethodUsesJobAPIURL runs the detect step against a
// stub API server mounted under a GHES-style /api/v3 prefix and checks that
// every release lookup goes to that host with the job token.
func TestActionDetectInstallMethodUsesJobAPIURL(t *testing.T) {
	for _, tool := range []string{"bash", "curl", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available: %v", tool, err)
		}
	}
	script := compositeActionStepNamed(t, "action.yml", "Detect install method").Run

	const (
		token     = "job-host-token"
		commitSHA = "0123456789abcdef0123456789abcdef01234567"
	)

	tests := []struct {
		name       string
		version    string
		wantPaths  []string
		wantOutput string
	}{
		{
			name:    "latest",
			version: "latest",
			wantPaths: []string{
				"/api/v3/repos/fullsend-ai/fullsend/releases/latest",
				"/api/v3/repos/fullsend-ai/fullsend/releases/tags/v1.2.3",
			},
			wantOutput: "install-method=release\nversion-url=v1.2.3\nversion-asset=1.2.3\n",
		},
		{
			name:    "tag",
			version: "v1.2.3",
			wantPaths: []string{
				"/api/v3/repos/fullsend-ai/fullsend/releases/tags/v1.2.3",
			},
			wantOutput: "install-method=release\nversion-url=v1.2.3\nversion-asset=1.2.3\n",
		},
		{
			name:    "commit SHA",
			version: commitSHA,
			wantPaths: []string{
				"/api/v3/repos/fullsend-ai/fullsend/git/tags/" + commitSHA,
				"/api/v3/repos/fullsend-ai/fullsend/tags",
				"/api/v3/repos/fullsend-ai/fullsend/releases/tags/v1.2.3",
			},
			wantOutput: "install-method=release\nversion-url=v1.2.3\nversion-asset=1.2.3\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu    sync.Mutex
				paths []string
			)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				paths = append(paths, r.URL.Path)
				mu.Unlock()
				if r.Header.Get("Authorization") != "Bearer "+token {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v3/repos/fullsend-ai/fullsend/releases/latest":
					_, _ = w.Write([]byte(`{"tag_name":"v1.2.3"}`))
				case "/api/v3/repos/fullsend-ai/fullsend/git/tags/" + commitSHA:
					_, _ = w.Write([]byte(`{}`))
				case "/api/v3/repos/fullsend-ai/fullsend/tags":
					_, _ = w.Write([]byte(`[{"name":"v1.2.3","commit":{"sha":"` + commitSHA + `"}}]`))
				case "/api/v3/repos/fullsend-ai/fullsend/releases/tags/v1.2.3":
					_, _ = w.Write([]byte(`{}`))
				default:
					w.WriteHeader(http.StatusTeapot)
				}
			}))
			defer srv.Close()

			dir := t.TempDir()
			workspace := filepath.Join(dir, "workspace")
			require.NoError(t, os.Mkdir(workspace, 0o755))
			outputPath := filepath.Join(dir, "github-output")

			cmd := exec.Command("bash", "-c", script)
			cmd.Env = append(os.Environ(),
				"VERSION="+tc.version,
				"GH_TOKEN="+token,
				"API_URL="+srv.URL+"/api/v3/",
				"GITHUB_WORKSPACE="+workspace,
				"GITHUB_OUTPUT="+outputPath,
			)
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, string(out))

			got, err := os.ReadFile(outputPath)
			require.NoError(t, err)
			assert.Equal(t, tc.wantOutput, string(got))
			mu.Lock()
			defer mu.Unlock()
			assert.Equal(t, tc.wantPaths, paths)
		})
	}
}

// TestActionReleaseDownloadUsesJobServerURL checks the release asset URL is
// built from the job's server URL, and is unchanged on github.com.
func TestActionReleaseDownloadUsesJobServerURL(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash not available: %v", err)
	}
	script := compositeActionStepNamed(t, "action.yml", "Download release binary").Run

	for _, tc := range []struct {
		serverURL string
		wantURL   string
	}{
		{
			serverURL: "https://github.com",
			wantURL:   "https://github.com/fullsend-ai/fullsend/releases/download/v1.2.3/fullsend_1.2.3_linux_amd64.tar.gz",
		},
		{
			serverURL: "https://ghe.example.test/",
			wantURL:   "https://ghe.example.test/fullsend-ai/fullsend/releases/download/v1.2.3/fullsend_1.2.3_linux_amd64.tar.gz",
		},
	} {
		t.Run(tc.serverURL, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			require.NoError(t, os.Mkdir(bin, 0o755))
			argsPath := filepath.Join(dir, "curl-args")
			require.NoError(t, os.WriteFile(filepath.Join(bin, "curl"),
				[]byte("#!/usr/bin/env bash\nprintf '%s\\n' \"$@\" >> \"${CURL_ARGS:?}\"\n"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(bin, "tar"),
				[]byte("#!/usr/bin/env bash\nexit 0\n"), 0o755))

			cmd := exec.Command("bash", "-c", script)
			cmd.Env = append(os.Environ(),
				"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"CURL_ARGS="+argsPath,
				"SERVER_URL="+tc.serverURL,
				"VERSION_URL=v1.2.3",
				"VERSION_ASSET=1.2.3",
				"RUNNER_OS=Linux",
				"RUNNER_ARCH=X64",
				"GH_TOKEN=job-host-token",
				"RUNNER_TEMP="+dir,
				"GITHUB_PATH="+filepath.Join(dir, "github-path"),
			)
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, string(out))

			args, err := os.ReadFile(argsPath)
			require.NoError(t, err)
			assert.Contains(t, strings.Split(strings.TrimSpace(string(args)), "\n"), tc.wantURL)
		})
	}
}

// TestSourceBuildClonesFromJobServerURL checks both source-build clone steps
// add a remote on the job's host (carrying the job token only there), and that
// the remote is unchanged on github.com.
func TestSourceBuildClonesFromJobServerURL(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash not available: %v", err)
	}
	const token = "job-host-token"

	steps := []struct {
		name     string
		script   string
		env      []string
		repoPath string
	}{
		{
			name:     "agent action",
			script:   compositeActionStepNamed(t, "action.yml", "Clone fullsend at ref for source build").Run,
			env:      []string{"SOURCE_REF=v1.2.3", "DOWNLOAD_OUTCOME="},
			repoPath: "fullsend-ai/fullsend",
		},
		{
			name: "install-fullsend-cli action",
			script: compositeActionStepNamed(t, ".github/actions/install-fullsend-cli/action.yml",
				"Clone fullsend at workflow SHA for source build").Run,
			env: []string{
				"WORKFLOW_REPOSITORY=example-org/fullsend",
				"WORKFLOW_SHA=0123456789abcdef0123456789abcdef01234567",
				"INSTALL_RELEASE_OUTCOME=",
			},
			repoPath: "example-org/fullsend",
		},
	}
	hosts := []struct {
		serverURL string
		wantBase  string
	}{
		{serverURL: "https://github.com", wantBase: "https://x-access-token:" + token + "@github.com/"},
		{serverURL: "https://ghe.example.test/", wantBase: "https://x-access-token:" + token + "@ghe.example.test/"},
		{serverURL: "http://localhost:8080", wantBase: "http://x-access-token:" + token + "@localhost:8080/"},
	}

	for _, step := range steps {
		for _, host := range hosts {
			t.Run(step.name+"/"+host.serverURL, func(t *testing.T) {
				dir := t.TempDir()
				bin := filepath.Join(dir, "bin")
				require.NoError(t, os.Mkdir(bin, 0o755))
				argsPath := filepath.Join(dir, "git-args")
				require.NoError(t, os.WriteFile(filepath.Join(bin, "git"),
					[]byte("#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> \"${GIT_ARGS:?}\"\n"), 0o755))

				cmd := exec.Command("bash", "-c", step.script)
				cmd.Env = append(os.Environ(), step.env...)
				cmd.Env = append(cmd.Env,
					"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
					"GIT_ARGS="+argsPath,
					"GH_TOKEN="+token,
					"SERVER_URL="+host.serverURL,
					"RUNNER_TEMP="+dir,
				)
				out, err := cmd.CombinedOutput()
				require.NoError(t, err, string(out))

				args, err := os.ReadFile(argsPath)
				require.NoError(t, err)
				src := filepath.Join(dir, "fullsend-src")
				assert.Contains(t, strings.Split(strings.TrimSpace(string(args)), "\n"),
					"-C "+src+" remote add origin "+host.wantBase+step.repoPath+".git")
			})
		}
	}
}
