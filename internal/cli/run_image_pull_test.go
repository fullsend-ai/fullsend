package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const testWorkflowToken = "fake-workflow-token-for-tests"

const privateImage = "ghcr.io/example-org/fullsend-code-node24@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// podmanResponse is what the fake returns for one podman subcommand.
type podmanResponse struct {
	out []byte
	err error
}

// fakePodman records podman invocations and answers each by its first
// argument. responses maps a subcommand ("image", "pull") to the output and
// error that call returns; missing entries succeed. authFiles records the
// --authfile path of every call that passed one, and authJSON and authMode
// the content and permissions of that file as they were when the call ran.
type fakePodman struct {
	calls     [][]string
	responses map[string]podmanResponse
	authFiles []string
	authJSON  []byte
	authMode  os.FileMode
}

func (f *fakePodman) run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	for i, a := range args {
		if a == "--authfile" && i+1 < len(args) {
			f.authFiles = append(f.authFiles, args[i+1])
			f.authJSON, _ = os.ReadFile(args[i+1])
			if info, err := os.Stat(args[i+1]); err == nil {
				f.authMode = info.Mode().Perm()
			}
		}
	}
	if r, ok := f.responses[args[0]]; ok {
		return r.out, r.err
	}
	return nil, nil
}

func (f *fakePodman) respond(sub string, out string, err error) {
	if f.responses == nil {
		f.responses = map[string]podmanResponse{}
	}
	f.responses[sub] = podmanResponse{[]byte(out), err}
}

func (f *fakePodman) subcommands() []string {
	var subs []string
	for _, c := range f.calls {
		subs = append(subs, c[0])
	}
	return subs
}

func setupImagePull(t *testing.T) (*fakePodman, *bytes.Buffer, *ui.Printer) {
	t.Helper()
	fake := &fakePodman{}
	// Image not present locally unless a test says otherwise.
	fake.respond("image", "", errors.New("exit status 1"))

	origRun, origLook, origRemote := runPodmanFn, imagePullLookPathFn, gatewayIsRemoteFn
	runPodmanFn = fake.run
	imagePullLookPathFn = func(string) (string, error) { return "/usr/bin/podman", nil }
	// Hermetic: only OPENSHELL_GATEWAY_ENDPOINT names the gateway, never a
	// real `openshell gateway info`.
	gatewayIsRemoteFn = func(ctx context.Context) bool {
		return sandbox.GatewayEndpointIsRemote(ctx, os.Getenv("OPENSHELL_GATEWAY_ENDPOINT"))
	}
	t.Cleanup(func() {
		runPodmanFn = origRun
		imagePullLookPathFn = origLook
		gatewayIsRemoteFn = origRemote
	})

	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv(workflowTokenEnv, testWorkflowToken)
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GITLAB_CI", "")
	t.Setenv("CI_REGISTRY", "")
	t.Setenv("CI_REGISTRY_USER", "")
	t.Setenv("CI_REGISTRY_PASSWORD", "")
	t.Setenv("TMPDIR", t.TempDir())

	var buf bytes.Buffer
	return fake, &buf, ui.New(&buf)
}

func TestPrePullSandboxImage_PullsWithRunScopedAuthFile(t *testing.T) {
	fake, buf, printer := setupImagePull(t)

	prePullSandboxImage(context.Background(), privateImage, printer)

	require.Equal(t, []string{"image", "pull"}, fake.subcommands())
	assert.Equal(t, []string{"image", "exists", privateImage}, fake.calls[0])

	pull := fake.calls[1]
	assert.Equal(t, privateImage, pull[len(pull)-1])
	for _, c := range fake.calls {
		assert.NotContains(t, strings.Join(c, " "), testWorkflowToken, "token must never appear in argv")
	}

	// The auth file held the token for ghcr.io while the pull ran, in the
	// user:token form podman reads, and was removed afterwards.
	require.Len(t, fake.authFiles, 1)
	var authFile struct {
		Auths map[string]struct{ Auth string } `json:"auths"`
	}
	require.NoError(t, json.Unmarshal(fake.authJSON, &authFile))
	require.Contains(t, authFile.Auths, ghcrRegistry)
	decoded, err := base64.StdEncoding.DecodeString(authFile.Auths[ghcrRegistry].Auth)
	require.NoError(t, err)
	assert.Equal(t, ghcrAuthUsername+":"+testWorkflowToken, string(decoded))
	assert.Equal(t, os.FileMode(0o600), fake.authMode)
	_, err = os.Stat(filepath.Dir(fake.authFiles[0]))
	assert.True(t, os.IsNotExist(err), "auth directory must be removed after a successful pull")

	assert.Contains(t, buf.String(), "Pulled sandbox image")
	assert.NotContains(t, buf.String(), testWorkflowToken)
}

func TestPrePullSandboxImage_PullFailureRedactsTokenAndCleansUp(t *testing.T) {
	fake, buf, printer := setupImagePull(t)
	fake.respond("pull", "Error: initializing source: unauthorized "+testWorkflowToken, errors.New("exit status 125"))

	prePullSandboxImage(context.Background(), privateImage, printer)

	assert.Equal(t, []string{"image", "pull"}, fake.subcommands())
	out := buf.String()
	assert.Contains(t, out, "pulling "+privateImage+" failed")
	assert.Contains(t, out, "***")
	assert.Contains(t, out, "without registry credentials")
	assert.NotContains(t, out, testWorkflowToken)

	require.NotEmpty(t, fake.authFiles)
	_, err := os.Stat(filepath.Dir(fake.authFiles[0]))
	assert.True(t, os.IsNotExist(err), "auth directory must be removed after a failed pull")
}

func TestPrePullSandboxImage_SkipsImageAlreadyPresent(t *testing.T) {
	fake, _, printer := setupImagePull(t)
	fake.respond("image", "", nil)

	prePullSandboxImage(context.Background(), privateImage, printer)

	assert.Equal(t, []string{"image"}, fake.subcommands())
}

func TestPrePullSandboxImage_NoAttempt(t *testing.T) {
	tests := []struct {
		name    string
		image   string
		actions string
		token   string
	}{
		{name: "non-ghcr image", image: "quay.io/org/img:tag", actions: "true", token: testWorkflowToken},
		{name: "empty image", image: "", actions: "true", token: testWorkflowToken},
		{name: "no workflow token", image: privateImage, actions: "true", token: ""},
		{name: "not on GitHub Actions", image: privateImage, actions: "", token: testWorkflowToken},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake, buf, printer := setupImagePull(t)
			t.Setenv("GITHUB_ACTIONS", tc.actions)
			t.Setenv(workflowTokenEnv, tc.token)

			prePullSandboxImage(context.Background(), tc.image, printer)

			assert.Empty(t, fake.calls)
			assert.Empty(t, buf.String())
		})
	}
}

// A remote gateway has its own image store: a local pull would not help it.
func TestPrePullSandboxImage_RemoteGatewaySkipsLocalPull(t *testing.T) {
	fake, buf, printer := setupImagePull(t)
	t.Setenv("OPENSHELL_GATEWAY_ENDPOINT", "https://gateway.example.com:17670")

	prePullSandboxImage(context.Background(), privateImage, printer)

	assert.Empty(t, fake.calls)
	assert.Contains(t, buf.String(), "gateway is remote")
}

// A local endpoint is not remote: the pull still runs.
func TestPrePullSandboxImage_LocalEndpointStillPulls(t *testing.T) {
	fake, _, printer := setupImagePull(t)
	t.Setenv("OPENSHELL_GATEWAY_ENDPOINT", "http://127.0.0.1:17670")

	prePullSandboxImage(context.Background(), privateImage, printer)

	assert.Equal(t, []string{"image", "pull"}, fake.subcommands())
}

// A registered remote gateway, with no endpoint exported, also has its own
// image store.
func TestPrePullSandboxImage_RegisteredRemoteGatewaySkipsLocalPull(t *testing.T) {
	fake, buf, printer := setupImagePull(t)
	gatewayIsRemoteFn = func(context.Context) bool { return true }

	prePullSandboxImage(context.Background(), privateImage, printer)

	assert.Empty(t, fake.calls)
	assert.Contains(t, buf.String(), "gateway is remote")
}

// A non-loopback endpoint that is this host's own address is a local
// gateway: run still pulls into the local store.
func TestPrePullSandboxImage_OwnAddressEndpointStillPulls(t *testing.T) {
	ip := localNonLoopbackIP(t)
	fake, _, printer := setupImagePull(t)
	t.Setenv("OPENSHELL_GATEWAY_ENDPOINT", "https://"+net.JoinHostPort(ip, "17670"))

	prePullSandboxImage(context.Background(), privateImage, printer)

	assert.Equal(t, []string{"image", "pull"}, fake.subcommands())
}

// localNonLoopbackIP returns an address assigned to one of this host's
// interfaces that is not loopback, or skips the test when there is none.
func localNonLoopbackIP(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil {
				return n.IP.String()
			}
		}
	}
	t.Skip("no non-loopback IPv4 interface address on this host")
	return ""
}

func TestPreparedWorkflowToken(t *testing.T) {
	const ghToken = "ghs_exampleTokenValue123"
	const githubToken = "ghs_otherTokenValue456"
	tests := []struct {
		name        string
		actions     string
		workflow    string
		ghToken     string
		githubToken string
		wantToken   string
		wantProblem bool
	}{
		{"explicit workflow token wins", "true", "explicit-token", ghToken, githubToken, "explicit-token", false},
		{"GH_TOKEN only", "true", "", ghToken, "", ghToken, false},
		{"GITHUB_TOKEN only", "true", "", "", githubToken, githubToken, false},
		{"GH_TOKEN preferred over GITHUB_TOKEN", "true", "", ghToken, githubToken, ghToken, false},
		{"neither set", "true", "", "", "", "", false},
		{"malformed GH_TOKEN is refused", "true", "", "bad token;rm", githubToken, "", true},
		{"outside GitHub Actions", "", "", ghToken, githubToken, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GITHUB_ACTIONS", tc.actions)
			t.Setenv(workflowTokenEnv, tc.workflow)
			t.Setenv("GH_TOKEN", tc.ghToken)
			t.Setenv("GITHUB_TOKEN", tc.githubToken)

			token, problem := preparedWorkflowToken()

			assert.Equal(t, tc.wantToken, token)
			assert.Equal(t, tc.wantProblem, problem != "")
			if tc.wantProblem {
				assert.NotContains(t, problem, tc.ghToken, "the problem never carries the token")
			}
		})
	}
}

func TestPrePullSandboxImage_PodmanMissing(t *testing.T) {
	fake, buf, printer := setupImagePull(t)
	imagePullLookPathFn = func(string) (string, error) { return "", errors.New("not found") }

	prePullSandboxImage(context.Background(), privateImage, printer)

	assert.Empty(t, fake.calls)
	assert.Contains(t, buf.String(), "podman not found")
}

func TestPrePullSandboxImage_TempDirFailure(t *testing.T) {
	fake, buf, printer := setupImagePull(t)
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "does-not-exist"))

	prePullSandboxImage(context.Background(), privateImage, printer)

	assert.Equal(t, []string{"image"}, fake.subcommands())
	assert.Contains(t, buf.String(), "creating registry auth directory failed")
}

func TestRunPodmanFn_PassesArgs(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\necho \"args:$*\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "podman"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	out, err := runPodmanFn(context.Background(), "image", "exists", "img")
	require.NoError(t, err)
	assert.Equal(t, "args:image exists img\n", string(out))
}

const (
	testGitLabRegistry = "gitlab.example.com:5050"
	testGitLabPassword = "fake-gitlab-registry-password"
	gitLabImage        = testGitLabRegistry + "/group/project/sandbox:1"
)

// setGitLabCI makes the environment look like a GitLab CI job with
// registry credentials, and not like GitHub Actions.
func setGitLabCI(t *testing.T) {
	t.Helper()
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("GITLAB_CI", "true")
	t.Setenv("CI_REGISTRY", testGitLabRegistry)
	t.Setenv("CI_REGISTRY_USER", "gitlab-ci-token")
	t.Setenv("CI_REGISTRY_PASSWORD", testGitLabPassword)
}

func TestSandboxImageCredential(t *testing.T) {
	t.Run("github actions ghcr", func(t *testing.T) {
		setupImagePull(t)
		cred, ok := sandboxImageCredential(privateImage)
		require.True(t, ok)
		assert.Equal(t, ghcrRegistry, cred.registry)
		assert.Equal(t, ghcrAuthUsername, cred.username)
		assert.Equal(t, testWorkflowToken, cred.secret)
	})
	t.Run("gitlab ci registry", func(t *testing.T) {
		setupImagePull(t)
		setGitLabCI(t)
		cred, ok := sandboxImageCredential(gitLabImage)
		require.True(t, ok)
		assert.Equal(t, testGitLabRegistry, cred.registry)
		assert.Equal(t, "gitlab-ci-token", cred.username)
		assert.Equal(t, testGitLabPassword, cred.secret)
	})
	t.Run("gitlab ci other registry", func(t *testing.T) {
		setupImagePull(t)
		setGitLabCI(t)
		_, ok := sandboxImageCredential("quay.io/org/img:tag")
		assert.False(t, ok)
	})
	t.Run("gitlab ci missing password", func(t *testing.T) {
		setupImagePull(t)
		setGitLabCI(t)
		t.Setenv("CI_REGISTRY_PASSWORD", "")
		_, ok := sandboxImageCredential(gitLabImage)
		assert.False(t, ok)
	})
	t.Run("gitlab registry outside gitlab ci", func(t *testing.T) {
		setupImagePull(t)
		setGitLabCI(t)
		t.Setenv("GITLAB_CI", "")
		_, ok := sandboxImageCredential(gitLabImage)
		assert.False(t, ok)
	})
	t.Run("unqualified image", func(t *testing.T) {
		setupImagePull(t)
		_, ok := sandboxImageCredential("alpine")
		assert.False(t, ok)
	})
}

func TestPrePullSandboxImage_GitLabRegistry(t *testing.T) {
	fake, buf, printer := setupImagePull(t)
	setGitLabCI(t)

	prePullSandboxImage(context.Background(), gitLabImage, printer)

	require.Equal(t, []string{"image", "pull"}, fake.subcommands())
	var authFile struct {
		Auths map[string]struct{ Auth string } `json:"auths"`
	}
	require.NoError(t, json.Unmarshal(fake.authJSON, &authFile))
	require.Contains(t, authFile.Auths, testGitLabRegistry)
	decoded, err := base64.StdEncoding.DecodeString(authFile.Auths[testGitLabRegistry].Auth)
	require.NoError(t, err)
	assert.Equal(t, "gitlab-ci-token:"+testGitLabPassword, string(decoded))
	for _, c := range fake.calls {
		assert.NotContains(t, strings.Join(c, " "), testGitLabPassword, "password must never appear in argv")
	}
	assert.Contains(t, buf.String(), "GitLab CI registry credentials")
	assert.NotContains(t, buf.String(), testGitLabPassword)
}

func TestPullLocalImage_Anonymous(t *testing.T) {
	fake, _, _ := setupImagePull(t)

	require.NoError(t, pullLocalImage(context.Background(), "quay.io/org/img:tag", nil))

	assert.Equal(t, [][]string{{"pull", "--quiet", "quay.io/org/img:tag"}}, fake.calls)
	assert.Empty(t, fake.authFiles)
}

func TestPullLocalImage_AnonymousFailureKeepsOutput(t *testing.T) {
	fake, _, _ := setupImagePull(t)
	fake.respond("pull", "Error: no space left on device", errors.New("exit status 125"))

	err := pullLocalImage(context.Background(), "quay.io/org/img:tag", nil)

	require.Error(t, err)
	assert.Equal(t, "pulling quay.io/org/img:tag failed: Error: no space left on device", err.Error())
	var pullErr *imagePullError
	require.ErrorAs(t, err, &pullErr)
	assert.EqualError(t, errors.Unwrap(err), "exit status 125")
}

func TestPullLocalImage_AuthDirFailure(t *testing.T) {
	fake, _, _ := setupImagePull(t)
	// A regular file as TMPDIR makes MkdirTemp fail.
	parent := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(parent, nil, 0o600))
	t.Setenv("TMPDIR", parent)

	cred := registryCredential{registry: ghcrRegistry, username: ghcrAuthUsername, secret: testWorkflowToken}
	err := pullLocalImage(context.Background(), privateImage, &cred)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating registry auth directory failed")
	assert.NotContains(t, err.Error(), testWorkflowToken)
	assert.Empty(t, fake.calls)
}
