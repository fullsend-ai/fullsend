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
	t.Setenv("OPENSHELL_GATEWAY_ENDPOINT", "")
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

func TestPrePullGHCRImage_PullsWithRunScopedAuthFile(t *testing.T) {
	fake, buf, printer := setupImagePull(t)

	prePullGHCRImage(context.Background(), privateImage, printer)

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

	assert.Contains(t, buf.String(), "Pulled image")
	assert.NotContains(t, buf.String(), testWorkflowToken)
}

func TestPrePullGHCRImage_PullFailureRedactsTokenAndCleansUp(t *testing.T) {
	fake, buf, printer := setupImagePull(t)
	fake.respond("pull", "Error: initializing source: unauthorized "+testWorkflowToken, errors.New("exit status 125"))

	prePullGHCRImage(context.Background(), privateImage, printer)

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

func TestPrePullGHCRImage_SkipsImageAlreadyPresent(t *testing.T) {
	fake, _, printer := setupImagePull(t)
	fake.respond("image", "", nil)

	prePullGHCRImage(context.Background(), privateImage, printer)

	assert.Equal(t, []string{"image"}, fake.subcommands())
}

func TestPrePullGHCRImage_NoAttempt(t *testing.T) {
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

			prePullGHCRImage(context.Background(), tc.image, printer)

			assert.Empty(t, fake.calls)
			assert.Empty(t, buf.String())
		})
	}
}

func TestPrePullGHCRImage_PodmanMissing(t *testing.T) {
	fake, buf, printer := setupImagePull(t)
	imagePullLookPathFn = func(string) (string, error) { return "", errors.New("not found") }

	prePullGHCRImage(context.Background(), privateImage, printer)

	assert.Empty(t, fake.calls)
	assert.Contains(t, buf.String(), "podman not found")
}

func TestPrePullGHCRImage_RemoteGatewaySkipsLocalPodman(t *testing.T) {
	fake, buf, printer := setupImagePull(t)
	t.Setenv("OPENSHELL_GATEWAY_ENDPOINT", "https://gateway.example.test")
	t.Setenv(workflowTokenEnv, "")

	prePullGHCRImage(context.Background(), privateImage, printer)

	assert.Empty(t, fake.calls)
	assert.Contains(t, buf.String(), "gateway is remote")
	assert.Contains(t, buf.String(), "gateway-host registry authentication or an image already cached there")
	assert.Contains(t, buf.String(), "public images can still be pulled")
	assert.NotContains(t, buf.String(), testWorkflowToken)
}

func TestPrePullGHCRImage_TempDirFailure(t *testing.T) {
	fake, buf, printer := setupImagePull(t)
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "does-not-exist"))

	prePullGHCRImage(context.Background(), privateImage, printer)

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
