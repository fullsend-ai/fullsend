package cli

import (
	"bytes"
	"context"
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

type podmanCall struct {
	stdin string
	args  []string
}

// fakePodman records podman invocations and answers each by its first
// argument. responses maps a subcommand ("image", "login", "pull") to the
// output and error that call returns; missing entries succeed.
type fakePodman struct {
	calls     []podmanCall
	responses map[string]struct {
		out []byte
		err error
	}
	authFiles []string
}

func (f *fakePodman) run(_ context.Context, stdin string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, podmanCall{stdin: stdin, args: append([]string(nil), args...)})
	for i, a := range args {
		if a == "--authfile" && i+1 < len(args) {
			f.authFiles = append(f.authFiles, args[i+1])
			if args[0] == "login" {
				// podman login writes the auth file; mimic it so cleanup is
				// observable.
				_ = os.WriteFile(args[i+1], []byte(`{"auths":{}}`), 0o600)
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
		f.responses = map[string]struct {
			out []byte
			err error
		}{}
	}
	f.responses[sub] = struct {
		out []byte
		err error
	}{[]byte(out), err}
}

func (f *fakePodman) subcommands() []string {
	var subs []string
	for _, c := range f.calls {
		subs = append(subs, c.args[0])
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
	t.Setenv("GITHUB_ACTOR", "octocat")
	t.Setenv("TMPDIR", t.TempDir())

	var buf bytes.Buffer
	return fake, &buf, ui.New(&buf)
}

const privateImage = "ghcr.io/example-org/fullsend-code-node24@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

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

func TestPrePullGHCRImage_LoginThenPullWithTokenOnStdin(t *testing.T) {
	fake, buf, printer := setupImagePull(t)

	prePullGHCRImage(context.Background(), privateImage, printer)

	require.Equal(t, []string{"image", "login", "pull"}, fake.subcommands())

	login := fake.calls[1]
	assert.Equal(t, testWorkflowToken, login.stdin, "token must reach podman on stdin")
	assert.Contains(t, login.args, "--password-stdin")
	assert.Contains(t, login.args, "octocat")
	assert.Equal(t, ghcrRegistry, login.args[len(login.args)-1])

	pull := fake.calls[2]
	assert.Empty(t, pull.stdin)
	assert.Equal(t, privateImage, pull.args[len(pull.args)-1])

	for _, c := range fake.calls {
		assert.NotContains(t, strings.Join(c.args, " "), testWorkflowToken, "token must never appear in argv")
	}

	// Login and pull share one run-scoped auth file, removed afterwards.
	require.Len(t, fake.authFiles, 2)
	assert.Equal(t, fake.authFiles[0], fake.authFiles[1])
	_, err := os.Stat(filepath.Dir(fake.authFiles[0]))
	assert.True(t, os.IsNotExist(err), "auth directory must be removed after a successful pull")

	assert.Contains(t, buf.String(), "Pulled sandbox image")
	assert.NotContains(t, buf.String(), testWorkflowToken)
}

func TestPrePullGHCRImage_DefaultUsername(t *testing.T) {
	fake, _, printer := setupImagePull(t)
	t.Setenv("GITHUB_ACTOR", "")

	prePullGHCRImage(context.Background(), privateImage, printer)

	require.Len(t, fake.calls, 3)
	assert.Contains(t, fake.calls[1].args, ghcrDefaultUsername)
}

func TestPrePullGHCRImage_LoginFailureRedactsTokenAndCleansUp(t *testing.T) {
	fake, buf, printer := setupImagePull(t)
	fake.respond("login", "Error: logging into ghcr.io: invalid credentials "+testWorkflowToken, errors.New("exit status 125"))

	prePullGHCRImage(context.Background(), privateImage, printer)

	assert.Equal(t, []string{"image", "login"}, fake.subcommands(), "no pull after a failed login")
	out := buf.String()
	assert.Contains(t, out, "ghcr.io login failed")
	assert.Contains(t, out, "***")
	assert.NotContains(t, out, testWorkflowToken)

	require.NotEmpty(t, fake.authFiles)
	_, err := os.Stat(filepath.Dir(fake.authFiles[0]))
	assert.True(t, os.IsNotExist(err), "auth directory must be removed after a failed login")
}

func TestPrePullGHCRImage_PullFailureWarnsAndCleansUp(t *testing.T) {
	fake, buf, printer := setupImagePull(t)
	fake.respond("pull", "", errors.New("exit status 125"))

	prePullGHCRImage(context.Background(), privateImage, printer)

	assert.Equal(t, []string{"image", "login", "pull"}, fake.subcommands())
	assert.Contains(t, buf.String(), "exit status 125")
	assert.Contains(t, buf.String(), "without registry credentials")

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

func TestPrePullGHCRImage_TempDirFailure(t *testing.T) {
	fake, buf, printer := setupImagePull(t)
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "does-not-exist"))

	prePullGHCRImage(context.Background(), privateImage, printer)

	assert.Equal(t, []string{"image"}, fake.subcommands())
	assert.Contains(t, buf.String(), "creating registry auth directory failed")
}

func TestRunPodmanFn_PassesArgsAndStdin(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\necho \"args:$*\"\ncat\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "podman"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	out, err := runPodmanFn(context.Background(), "secret-on-stdin", "login", "--password-stdin", ghcrRegistry)
	require.NoError(t, err)
	assert.Equal(t, "args:login --password-stdin ghcr.io\nsecret-on-stdin", string(out))

	out, err = runPodmanFn(context.Background(), "", "image", "exists", "img")
	require.NoError(t, err)
	assert.Equal(t, "args:image exists img\n", string(out))
}
