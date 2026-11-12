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

// isGHCRImage reports whether image is a reference on ghcr.io.
func isGHCRImage(image string) bool {
	host, _, ok := strings.Cut(image, "/")
	return ok && strings.EqualFold(host, ghcrRegistry)
}

// prePullGHCRImage pulls a ghcr.io sandbox or mounted image into the runner
// user's Podman store with the Actions workflow token, so private GitHub
// packages can be used (#9019).
//
// At the pinned OpenShell (0.1.4) the Podman driver pulls the sandbox image
// through the libpod API without registry credentials, using the "missing"
// pull policy by default. Once the image is in the local store, that pull
// is a no-op. The token is written to an auth file in a private temporary
// directory that is removed on every return path, so no credential outlives
// the pull or reaches the runner user's shared Podman auth file. The file
// is written here rather than by `podman login`, which stores credentials
// wherever registries.conf's credential-helpers point.
//
// Known limitation: podman consults --authfile only under the default
// containers-auth.json credential helper. A registries.conf that names
// other helpers makes the pull run anonymously; it then fails for a private
// image and the warning below is the only trace.
//
// It runs only on GitHub Actions, for ghcr.io sandbox and image_mounts images,
// when GH_WORKFLOW_TOKEN is set. Failures are warnings: the gateway still attempts its own
// unauthenticated pull, which keeps public images working as before.
func prePullGHCRImage(ctx context.Context, image string, printer *ui.Printer) {
	if os.Getenv("GITHUB_ACTIONS") != "true" || !isGHCRImage(image) {
		return
	}
	token := os.Getenv(workflowTokenEnv)
	if token == "" {
		return
	}
	if _, err := imagePullLookPathFn("podman"); err != nil {
		printer.StepWarn("podman not found; the OpenShell gateway will pull " + image + " without registry credentials")
		return
	}

	ctx, cancel := context.WithTimeout(ctx, sandboxImagePullTimeout)
	defer cancel()

	// Already present: the gateway's default pull policy reuses it too.
	if _, err := runPodmanFn(ctx, "image", "exists", image); err == nil {
		return
	}

	warn := func(step string, out []byte, err error) {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			detail = err.Error()
		}
		printer.StepWarn(fmt.Sprintf("%s failed: %s; the OpenShell gateway will pull %s without registry credentials",
			step, strings.ReplaceAll(detail, token, "***"), image))
	}

	dir, err := os.MkdirTemp("", "fullsend-registry-auth-")
	if err != nil {
		warn("creating registry auth directory", nil, err)
		return
	}
	defer os.RemoveAll(dir)
	authFile := filepath.Join(dir, "auth.json")

	auth := base64.StdEncoding.EncodeToString([]byte(ghcrAuthUsername + ":" + token))
	authJSON := fmt.Sprintf(`{"auths":{%q:{"auth":%q}}}`, ghcrRegistry, auth)
	if err := os.WriteFile(authFile, []byte(authJSON), 0o600); err != nil {
		warn("writing registry auth file", nil, err)
		return
	}

	printer.StepStart("Pulling image with the workflow token: " + image)
	if out, err := runPodmanFn(ctx, "pull", "--quiet", "--authfile", authFile, image); err != nil {
		warn("pulling "+image, out, err)
		return
	}
	printer.StepDone("Pulled image " + image)
}
