package cli

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

// ghcrRegistry is the GitHub Container Registry host. The Actions workflow
// token can read its private packages when the job grants packages: read.
const ghcrRegistry = "ghcr.io"

// ghcrAuthUsername is the user name stored beside the token in the auth
// file. ghcr.io authenticates the token, not the username.
const ghcrAuthUsername = "github-actions"

// sandboxImagePullTimeout bounds the pull, matching the OpenShell Podman
// driver's own pull timeout (OpenShell 0.1.4).
const sandboxImagePullTimeout = 10 * time.Minute

// imagePullLookPathFn locates the podman binary. Override in tests to
// simulate podman being present or missing without touching the host PATH.
var imagePullLookPathFn = exec.LookPath

// runPodmanFn runs podman with args and returns the combined output.
// Override in tests to avoid running a real podman binary.
var runPodmanFn = func(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "podman", args...).CombinedOutput()
}

// imageRegistryHost returns the part of image before the first slash, which
// is the registry host (with any port) for a fully qualified reference, or
// "" when image has no slash.
func imageRegistryHost(image string) string {
	host, _, ok := strings.Cut(image, "/")
	if !ok {
		return ""
	}
	return host
}

// registryCredential is a registry login used for one image pull. The
// secret is only ever written to a private, temporary auth file and is
// redacted from every message.
type registryCredential struct {
	registry string // auth-file key: the registry host, with any port
	username string
	secret   string
	source   string // human-readable origin, for progress messages
}

// sandboxImageCredential returns the registry credential fullsend pulls
// image with, and whether one applies. It is forge-neutral: each forge's CI
// supplies a job-scoped credential for its own registry.
//
//   - GitHub Actions: GH_WORKFLOW_TOKEN for ghcr.io images (#9019).
//   - GitLab CI: CI_REGISTRY_USER and CI_REGISTRY_PASSWORD for images on
//     the instance's container registry (CI_REGISTRY).
//
// fullsend run and fullsend prepare both use it, so an image prepare
// warms is pulled with the credential a run would use.
func sandboxImageCredential(image string) (registryCredential, bool) {
	host := imageRegistryHost(image)
	if host == "" {
		return registryCredential{}, false
	}
	if os.Getenv("GITHUB_ACTIONS") == "true" && strings.EqualFold(host, ghcrRegistry) {
		if token := os.Getenv(workflowTokenEnv); token != "" {
			return registryCredential{registry: ghcrRegistry, username: ghcrAuthUsername, secret: token, source: "the workflow token"}, true
		}
	}
	if os.Getenv("GITLAB_CI") == "true" {
		registry := os.Getenv("CI_REGISTRY")
		user, password := os.Getenv("CI_REGISTRY_USER"), os.Getenv("CI_REGISTRY_PASSWORD")
		if registry != "" && strings.EqualFold(host, registry) && user != "" && password != "" {
			return registryCredential{registry: registry, username: user, secret: password, source: "the GitLab CI registry credentials"}, true
		}
	}
	return registryCredential{}, false
}

// imagePullError is a failed step of a local image pull. Its message
// carries podman's output with the credential secret redacted.
type imagePullError struct {
	step   string
	detail string
	err    error
}

func (e *imagePullError) Error() string { return e.step + " failed: " + e.detail }
func (e *imagePullError) Unwrap() error { return e.err }

func newImagePullError(step string, out []byte, err error, secret string) *imagePullError {
	detail := strings.TrimSpace(string(out))
	if detail == "" {
		detail = err.Error()
	}
	if secret != "" {
		detail = strings.ReplaceAll(detail, secret, "***")
	}
	return &imagePullError{step: step, detail: detail, err: err}
}

// localImageExists reports whether image is already in the local Podman
// store. The gateway's default "missing" pull policy reuses it too.
func localImageExists(ctx context.Context, image string) bool {
	_, err := runPodmanFn(ctx, "image", "exists", image)
	return err == nil
}

// pullLocalImage pulls image into the local Podman store, authenticating
// with cred when it is non-nil. The credential is written to an auth file
// in a private temporary directory that is removed on every return path,
// so no credential outlives the pull or reaches the user's shared Podman
// auth file. The file is written here rather than by `podman login`, which
// stores credentials wherever registries.conf's credential-helpers point.
//
// Known limitation: podman consults --authfile only under the default
// containers-auth.json credential helper. A registries.conf that names
// other helpers makes the pull run anonymously.
func pullLocalImage(ctx context.Context, image string, cred *registryCredential) error {
	args := []string{"pull", "--quiet"}
	secret := ""
	if cred != nil {
		secret = cred.secret
		dir, err := os.MkdirTemp("", "fullsend-registry-auth-")
		if err != nil {
			return newImagePullError("creating registry auth directory", nil, err, secret)
		}
		defer os.RemoveAll(dir)
		authFile := filepath.Join(dir, "auth.json")

		auth := base64.StdEncoding.EncodeToString([]byte(cred.username + ":" + cred.secret))
		authJSON := fmt.Sprintf(`{"auths":{%q:{"auth":%q}}}`, cred.registry, auth)
		if err := os.WriteFile(authFile, []byte(authJSON), 0o600); err != nil {
			return newImagePullError("writing registry auth file", nil, err, secret)
		}
		args = append(args, "--authfile", authFile)
	}
	args = append(args, image)
	if out, err := runPodmanFn(ctx, args...); err != nil {
		return newImagePullError("pulling "+image, out, err, secret)
	}
	return nil
}

// prePullSandboxImage pulls a private sandbox image into the runner user's
// Podman store with the CI job's registry credential
// (sandboxImageCredential), so harness images published as private
// packages can be used (#9019).
//
// At the pinned OpenShell (0.1.4) the Podman driver pulls the sandbox image
// through the libpod API without registry credentials, using the "missing"
// pull policy by default. Once the image is in the local store, that pull
// is a no-op.
//
// It runs only when a credential applies. Failures are warnings: the
// gateway still attempts its own unauthenticated pull, which keeps public
// images working as before.
func prePullSandboxImage(ctx context.Context, image string, printer *ui.Printer) {
	cred, ok := sandboxImageCredential(image)
	if !ok {
		return
	}
	if _, err := imagePullLookPathFn("podman"); err != nil {
		printer.StepWarn("podman not found; the sandbox gateway will pull " + image + " without registry credentials")
		return
	}

	ctx, cancel := context.WithTimeout(ctx, sandboxImagePullTimeout)
	defer cancel()

	if localImageExists(ctx, image) {
		return
	}

	printer.StepStart("Pulling sandbox image with " + cred.source + ": " + image)
	if err := pullLocalImage(ctx, image, &cred); err != nil {
		printer.StepWarn(err.Error() + "; the sandbox gateway will pull " + image + " without registry credentials")
		return
	}
	printer.StepDone("Pulled sandbox image " + image)
}
