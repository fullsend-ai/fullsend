package cli

import (
	"context"
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

// ghcrDefaultUsername is the login name used when GITHUB_ACTOR is unset.
// ghcr.io authenticates the token, not the username.
const ghcrDefaultUsername = "github-actions"

// sandboxImagePullTimeout bounds the login and pull, matching the OpenShell
// Podman driver's own pull timeout.
const sandboxImagePullTimeout = 10 * time.Minute

// imagePullLookPathFn and runPodmanFn are swapped in tests.
var imagePullLookPathFn = exec.LookPath

// runPodmanFn runs podman with args, feeding stdin when it is non-empty, and
// returns the combined output.
var runPodmanFn = func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "podman", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	return cmd.CombinedOutput()
}

// isGHCRImage reports whether image is a reference on ghcr.io.
func isGHCRImage(image string) bool {
	host, _, ok := strings.Cut(image, "/")
	return ok && strings.EqualFold(host, ghcrRegistry)
}

// prePullGHCRImage pulls a ghcr.io sandbox image into the runner user's
// Podman store with the Actions workflow token, so harness images published
// as private GitHub packages can be used (#9019).
//
// The OpenShell Podman driver pulls the sandbox image through the libpod API
// without registry credentials, using the "missing" pull policy by default.
// Once the image is in the local store, that pull is a no-op. The token goes
// to podman only on stdin, and the auth file lives in a private temporary
// directory removed on every return path, so no credential outlives the
// pull or reaches the runner user's shared Podman auth file.
//
// It runs only on GitHub Actions, for ghcr.io images, when GH_WORKFLOW_TOKEN
// is set. Failures are warnings: the gateway still attempts its own
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
		printer.StepWarn("podman not found; the sandbox gateway will pull " + image + " without registry credentials")
		return
	}

	ctx, cancel := context.WithTimeout(ctx, sandboxImagePullTimeout)
	defer cancel()

	// Already present: the gateway's default pull policy reuses it too.
	if _, err := runPodmanFn(ctx, "", "image", "exists", image); err == nil {
		return
	}

	warn := func(step string, out []byte, err error) {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			detail = err.Error()
		}
		printer.StepWarn(fmt.Sprintf("%s failed: %s; the sandbox gateway will pull %s without registry credentials",
			step, strings.ReplaceAll(detail, token, "***"), image))
	}

	dir, err := os.MkdirTemp("", "fullsend-registry-auth-")
	if err != nil {
		warn("creating registry auth directory", nil, err)
		return
	}
	defer os.RemoveAll(dir)
	authFile := filepath.Join(dir, "auth.json")

	username := os.Getenv("GITHUB_ACTOR")
	if username == "" {
		username = ghcrDefaultUsername
	}

	printer.StepStart("Pulling sandbox image with the workflow token: " + image)
	if out, err := runPodmanFn(ctx, token, "login", "--authfile", authFile,
		"--username", username, "--password-stdin", ghcrRegistry); err != nil {
		warn(ghcrRegistry+" login", out, err)
		return
	}
	if out, err := runPodmanFn(ctx, "", "pull", "--quiet", "--authfile", authFile, image); err != nil {
		warn("pulling "+image, out, err)
		return
	}
	printer.StepDone("Pulled sandbox image " + image)
}
