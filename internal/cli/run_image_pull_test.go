package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

	origRun, origLook := runPodmanFn, imagePullLookPathFn
	runPodmanFn = fake.run
	imagePullLookPathFn = func(string) (string, error) { return "/usr/bin/podman", nil }
	t.Cleanup(func() {
		runPodmanFn = origRun
		imagePullLookPathFn = origLook
	})

	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv(workflowTokenEnv, testWorkflowToken)
	t.Setenv("TMPDIR", t.TempDir())

	var buf bytes.Buffer
	return fake, &buf, ui.New(&buf)
}

func TestIsGHCRImage(t *testing.T) {
	tests := map[string]bool{
		privateImage:                         true,
		"GHCR.io/org/img:tag":                true,
		"docker.io/library/alpine:3":         false,
		"quay.io/org/ghcr.io:tag":            false,
		"ghcr.io.example.com/org/img:latest": false,
		"ghcr.io":                            false,
		"":                                   false,
	}
	for image, want := range tests {
		assert.Equal(t, want, isGHCRImage(image), image)
	}
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

func TestPullSandboxImage_ReportsLocalOutcome(t *testing.T) {
	t.Run("pulled", func(t *testing.T) {
		_, _, printer := setupImagePull(t)
		assert.Equal(t, localPullPulled, pullSandboxImage(context.Background(), privateImage, testWorkflowToken, printer))
	})
	t.Run("already present", func(t *testing.T) {
		fake, _, printer := setupImagePull(t)
		fake.respond("image", "", nil)
		assert.Equal(t, localPullCached, pullSandboxImage(context.Background(), privateImage, testWorkflowToken, printer))
	})
	t.Run("pull failed", func(t *testing.T) {
		fake, _, printer := setupImagePull(t)
		fake.respond("pull", "Error: unauthorized", errors.New("exit status 125"))
		assert.Equal(t, localPullFailed, pullSandboxImage(context.Background(), privateImage, testWorkflowToken, printer))
	})
	t.Run("podman missing", func(t *testing.T) {
		_, _, printer := setupImagePull(t)
		imagePullLookPathFn = func(string) (string, error) { return "", errors.New("not found") }
		assert.Equal(t, localPullFailed, pullSandboxImage(context.Background(), privateImage, testWorkflowToken, printer))
	})
	t.Run("no credential applies", func(t *testing.T) {
		fake, _, printer := setupImagePull(t)
		assert.Equal(t, localPullNone, pullSandboxImage(context.Background(), privateImage, "", printer))
		assert.Equal(t, localPullNone, pullSandboxImage(context.Background(), "quay.io/org/img:tag", testWorkflowToken, printer))
		assert.Empty(t, fake.calls)
	})
}

// prepare supplies the token it resolved without minting; the pull must use
// that token, not the environment's, and still keep it out of argv.
func TestPullSandboxImage_UsesSuppliedToken(t *testing.T) {
	const supplied = "supplied-token-for-tests"
	fake, buf, printer := setupImagePull(t)

	require.Equal(t, localPullPulled, pullSandboxImage(context.Background(), privateImage, supplied, printer))

	var authFile struct {
		Auths map[string]struct{ Auth string } `json:"auths"`
	}
	require.NoError(t, json.Unmarshal(fake.authJSON, &authFile))
	decoded, err := base64.StdEncoding.DecodeString(authFile.Auths[ghcrRegistry].Auth)
	require.NoError(t, err)
	assert.Equal(t, ghcrAuthUsername+":"+supplied, string(decoded))
	for _, c := range fake.calls {
		assert.NotContains(t, strings.Join(c, " "), supplied)
	}
	assert.NotContains(t, buf.String(), supplied)
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
	t.Setenv(workflowTokenEnv, "")
	t.Setenv("GITLAB_CI", "true")
	t.Setenv("CI_REGISTRY", testGitLabRegistry)
	t.Setenv("CI_REGISTRY_USER", "gitlab-ci-token")
	t.Setenv("CI_REGISTRY_PASSWORD", testGitLabPassword)
}

func TestSandboxImageCredential_GitLabCI(t *testing.T) {
	t.Run("job registry image", func(t *testing.T) {
		setupImagePull(t)
		setGitLabCI(t)
		cred, ok := sandboxImageCredential(gitLabImage, "")
		require.True(t, ok)
		assert.Equal(t, testGitLabRegistry, cred.registry)
		assert.Equal(t, "gitlab-ci-token", cred.username)
		assert.Equal(t, testGitLabPassword, cred.secret)
	})
	t.Run("other registry", func(t *testing.T) {
		setupImagePull(t)
		setGitLabCI(t)
		_, ok := sandboxImageCredential("quay.io/org/img:tag", "")
		assert.False(t, ok)
	})
	t.Run("missing password", func(t *testing.T) {
		setupImagePull(t)
		setGitLabCI(t)
		t.Setenv("CI_REGISTRY_PASSWORD", "")
		_, ok := sandboxImageCredential(gitLabImage, "")
		assert.False(t, ok)
	})
	t.Run("not on GitLab CI", func(t *testing.T) {
		setupImagePull(t)
		setGitLabCI(t)
		t.Setenv("GITLAB_CI", "")
		_, ok := sandboxImageCredential(gitLabImage, "")
		assert.False(t, ok)
	})
	t.Run("workflow token does not apply to the GitLab registry", func(t *testing.T) {
		setupImagePull(t)
		t.Setenv("GITHUB_ACTIONS", "true")
		t.Setenv("GITLAB_CI", "")
		_, ok := sandboxImageCredential(gitLabImage, testWorkflowToken)
		assert.False(t, ok)
	})
}

func TestPrePullSandboxImage_GitLabCIRegistry(t *testing.T) {
	fake, buf, printer := setupImagePull(t)
	setGitLabCI(t)

	prePullSandboxImage(context.Background(), gitLabImage, printer)

	assert.Equal(t, []string{"image", "pull"}, fake.subcommands())
	assert.Equal(t, os.FileMode(0o600), fake.authMode)
	var authFile struct {
		Auths map[string]struct{ Auth string } `json:"auths"`
	}
	require.NoError(t, json.Unmarshal(fake.authJSON, &authFile))
	require.Contains(t, authFile.Auths, testGitLabRegistry)
	decoded, err := base64.StdEncoding.DecodeString(authFile.Auths[testGitLabRegistry].Auth)
	require.NoError(t, err)
	assert.Equal(t, "gitlab-ci-token:"+testGitLabPassword, string(decoded))
	for _, c := range fake.calls {
		assert.NotContains(t, strings.Join(c, " "), testGitLabPassword)
	}
	assert.NotContains(t, buf.String(), testGitLabPassword)
	_, statErr := os.Stat(filepath.Dir(fake.authFiles[0]))
	assert.True(t, os.IsNotExist(statErr), "auth directory must be removed after the pull")
}

func TestPrePullSandboxImage_GitLabCIPullFailureRedactsPassword(t *testing.T) {
	fake, buf, printer := setupImagePull(t)
	setGitLabCI(t)
	fake.respond("pull", "denied for "+testGitLabPassword, errors.New("exit status 125"))

	prePullSandboxImage(context.Background(), gitLabImage, printer)

	assert.Contains(t, buf.String(), "without registry credentials")
	assert.NotContains(t, buf.String(), testGitLabPassword)
}
