package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/envfile"
	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/resolve"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// Sandbox operations fullsend prepare performs. Variables so tests can run
// the command without openshell or a gateway.
var (
	prepareEnsureOpenshellFn = sandbox.EnsureAvailable
	prepareCheckGatewayFn    = sandbox.CheckGateway
	prepareCreateSandboxFn   = sandbox.CreateWithRetry
)

// Image statuses reported by fullsend prepare.
const (
	prepareImageCached  = "cached"  // already in the gateway's image store; not pulled
	prepareImagePulled  = "pulled"  // pulled into the gateway's image store
	prepareImageSkipped = "skipped" // not pulled; the preflight pulls it through the gateway
	prepareImageFailed  = "failed"
)

// Image sources reported by fullsend prepare.
const (
	prepareSourceHarness  = "harness"                // the harness image: field
	prepareSourceOverride = "FULLSEND_SANDBOX_IMAGE" // the environment override
	prepareSourceDefault  = "gateway-default"        // no image: the gateway's default sandbox image
)

// Gateway locations reported by fullsend prepare.
const (
	prepareGatewayLocal  = "local"
	prepareGatewayRemote = "remote"
)

// Failure kinds reported by fullsend prepare, so callers can branch on the
// cause without parsing messages.
const (
	prepareErrConfig       = "configuration"
	prepareErrOpenshell    = "openshell-unavailable"
	prepareErrGateway      = "gateway-unavailable"
	prepareErrDiskSpace    = "disk-space"
	prepareErrAuth         = "registry-auth"
	prepareErrUnresolvable = "image-unresolvable"
	prepareErrPull         = "image-pull"
	prepareErrPreflight    = "sandbox-preflight"
)

// prepareResult is the machine-readable result of fullsend prepare
// (--json). Fields are stable for programmatic callers.
type prepareResult struct {
	OK        bool                `json:"ok"`
	Agent     string              `json:"agent"`
	Harness   string              `json:"harness,omitempty"`
	Arch      string              `json:"arch"`
	Gateway   string              `json:"gateway,omitempty"`
	Images    []prepareImage      `json:"images"`
	Preflight *preparePreflight   `json:"preflight,omitempty"`
	Error     *prepareResultError `json:"error,omitempty"`
}

// prepareImage is one image fullsend prepare resolved.
type prepareImage struct {
	Role   string `json:"role"` // "sandbox"
	Ref    string `json:"ref"`  // empty for the gateway's default image
	Source string `json:"source"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// preparePreflight is the outcome of the create/teardown sandbox preflight.
type preparePreflight struct {
	Sandbox string `json:"sandbox"`
	Status  string `json:"status"` // "passed" or "failed"
}

// prepareResultError is why fullsend prepare failed.
type prepareResultError struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// prepareError is a failure with its kind and an actionable hint. Its
// message is the underlying error; the hint is appended for display.
type prepareError struct {
	kind string
	hint string
	err  error
}

func (e *prepareError) Error() string {
	if e.hint == "" {
		return e.err.Error()
	}
	return e.err.Error() + "\nhint: " + e.hint
}

func (e *prepareError) Unwrap() error { return e.err }

// prepareOptions are fullsend prepare's flags.
type prepareOptions struct {
	agentName   string
	fullsendDir string
	envFiles    []string
	forgeFlag   string
	eventFile   string
	jsonOut     bool
	rFlags      resolveFlags
}

func newPrepareCmd() *cobra.Command {
	var opts prepareOptions

	cmd := &cobra.Command{
		Use:   "prepare <agent-name>",
		Short: "Warm an agent's sandbox image and run a sandbox preflight",
		Long: `Resolve the agent's sandbox image exactly as 'fullsend run' does (harness image:
field, FULLSEND_SANDBOX_IMAGE override), pull it into the gateway's image store
when it is not already there, and create and delete a minimal sandbox to check
that the gateway can start one. It runs no pre-script, agent, or inference.

Run it once before starting many 'fullsend run' invocations in parallel, so a
missing image is pulled once and a broken environment fails fast. It exits
non-zero, naming the cause, when the image cannot be resolved or pulled or the
preflight sandbox cannot be created.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.agentName = args[0]
			progress := cmd.OutOrStdout()
			if opts.jsonOut {
				progress = cmd.ErrOrStderr()
			}
			return runPrepare(cmd.Context(), opts, ui.New(progress), cmd.OutOrStdout())
		},
	}

	addFullsendDirFlag(cmd, &opts.fullsendDir)
	cmd.Flags().StringArrayVar(&opts.envFiles, "env-file", nil, "load environment variables from a dotenv file (repeatable)")
	cmd.Flags().StringVar(&opts.forgeFlag, "forge", "", `forge platform to use (e.g. "github", "gitlab"); auto-detected from CI env vars when omitted`)
	cmd.Flags().StringVar(&opts.eventFile, "event-file", "", "path to a normalized event JSON file for CEL overlay resolution (ADR 0088)")
	cmd.Flags().BoolVar(&opts.jsonOut, "json", false, "print the result as JSON on stdout (progress goes to stderr)")
	cmd.Flags().BoolVar(&opts.rFlags.offline, "offline", false, "reject network fetches; only use cached remote resources")
	cmd.Flags().IntVar(&opts.rFlags.maxDepth, "max-depth", resolve.DefaultMaxDepth, "maximum dependency depth for transitive resolution (0 disables)")
	cmd.Flags().IntVar(&opts.rFlags.maxResources, "max-resources", resolve.DefaultMaxResources, "maximum total remote resources per harness")

	return cmd
}

// runPrepare runs fullsend prepare and, with --json, writes the result to
// out whether or not preparation succeeded.
func runPrepare(ctx context.Context, opts prepareOptions, printer *ui.Printer, out io.Writer) error {
	res, err := prepareAgent(ctx, opts, printer)
	if err != nil {
		res.OK = false
		res.Error = &prepareResultError{Kind: prepareErrConfig, Message: err.Error()}
		var pe *prepareError
		if errors.As(err, &pe) {
			res.Error = &prepareResultError{Kind: pe.kind, Message: pe.err.Error(), Hint: pe.hint}
		}
	}
	if opts.jsonOut {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if encErr := enc.Encode(res); encErr != nil && err == nil {
			return fmt.Errorf("writing JSON result: %w", encErr)
		}
	}
	return err
}

// prepareAgent resolves the agent's sandbox image, warms it, and runs the
// sandbox preflight. The returned result is filled in as far as preparation
// got, also on error.
func prepareAgent(ctx context.Context, opts prepareOptions, printer *ui.Printer) (prepareResult, error) {
	res := prepareResult{Agent: opts.agentName, Images: []prepareImage{}}

	printer.Banner(Version())
	printer.Blank()
	printer.Header("Preparing agent: " + opts.agentName)
	printer.Blank()

	if opts.rFlags.maxDepth < 0 {
		return res, fmt.Errorf("--max-depth must be >= 0, got %d", opts.rFlags.maxDepth)
	}
	if opts.rFlags.maxResources < 1 {
		return res, fmt.Errorf("--max-resources must be >= 1, got %d", opts.rFlags.maxResources)
	}
	for _, ef := range opts.envFiles {
		if err := envfile.Load(ef); err != nil {
			return res, fmt.Errorf("loading env file %s: %w", ef, err)
		}
	}
	// After the env files, which may set FULLSEND_SANDBOX_ARCH.
	res.Arch = sandboxArch()

	image, source, harnessPath, sandboxTimeout, err := resolvePrepareImage(ctx, opts, printer)
	res.Harness = harnessPath
	if err != nil {
		return res, err
	}
	res.Images = append(res.Images, prepareImage{Role: "sandbox", Ref: image, Source: source})
	img := &res.Images[0]
	printer.KeyValue("Image", displayPrepareImage(image))
	printer.KeyValue("Arch", res.Arch)

	// Read before CheckGateway: it may set OPENSHELL_GATEWAY_ENDPOINT itself
	// for a containerized CLI that reaches the local gateway through a
	// host-gateway name. Only an endpoint the caller set names a gateway
	// whose image store may not be this host's.
	remote := os.Getenv("OPENSHELL_GATEWAY_ENDPOINT") != ""
	res.Gateway = prepareGatewayLocal
	if remote {
		res.Gateway = prepareGatewayRemote
	}

	printer.StepStart("Checking openshell availability")
	if err := prepareEnsureOpenshellFn(); err != nil {
		printer.StepFail("openshell not available")
		return res, &prepareError{kind: prepareErrOpenshell, err: fmt.Errorf("openshell is required: %w", err),
			hint: "install the OpenShell CLI (see the running agents locally guide)"}
	}
	printer.StepStart("Checking gateway")
	if err := prepareCheckGatewayFn(); err != nil {
		printer.StepFail("Gateway not running")
		return res, &prepareError{kind: prepareErrGateway, err: fmt.Errorf("gateway check failed: %w", err),
			hint: "start the OpenShell gateway, or set OPENSHELL_GATEWAY_ENDPOINT to a running one"}
	}
	printer.StepDone("Gateway available")

	if err := warmPrepareImage(ctx, img, remote, printer); err != nil {
		return res, err
	}

	res.Preflight = &preparePreflight{Sandbox: generateSandboxName(opts.agentName)}
	if err := runPrepareSandboxPreflight(res.Preflight, image, sandboxTimeout, printer); err != nil {
		// The preflight is the first pull of an image the gateway had to
		// fetch itself; a pull failure there is the image's failure.
		if img.Status == prepareImageSkipped {
			img.Status = prepareImageFailed
		}
		return res, err
	}
	if img.Status == prepareImageSkipped && image != "" {
		img.Detail += "; the sandbox preflight pulled it through the gateway"
	}

	res.OK = true
	printer.Blank()
	printer.StepDone("Agent " + opts.agentName + " is prepared")
	return res, nil
}

// resolvePrepareImage composes the agent's harness the way fullsend run
// does and returns its sandbox image after the FULLSEND_SANDBOX_IMAGE
// override, where the image came from, the harness path, and the sandbox
// ready timeout.
//
// It shares composeRunHarness and the composition options with fullsend
// run. It does not take the runner secrets bundle (ADR 0136), which run
// deletes as it reads it: the runs that follow need it. Without the
// bundle's names composition skips only the overlay runner-secret guard,
// which validates and never changes the composed image.
func resolvePrepareImage(ctx context.Context, opts prepareOptions, printer *ui.Printer) (image, source, harnessPath string, sandboxTimeout time.Duration, err error) {
	fail := func(err error) (string, string, string, time.Duration, error) {
		return "", "", harnessPath, 0, &prepareError{kind: prepareErrConfig, err: err}
	}

	absFullsendDir, err := filepath.Abs(opts.fullsendDir)
	if err != nil {
		return fail(fmt.Errorf("resolving fullsend dir: %w", err))
	}
	orgConfigPath := filepath.Join(absFullsendDir, "config.yaml")
	orgCfg, err := loadLockConfig(orgConfigPath)
	if err != nil {
		printer.StepFail("Failed to load fullsend config")
		return fail(err)
	}
	if err := siblingConfigBlockError(orgConfigPath, opts.agentName); err != nil {
		printer.StepFail("Failed to load fullsend config")
		return fail(err)
	}
	forgePlatform, err := detectForgePlatform(opts.forgeFlag, orgCfg)
	if err != nil {
		printer.StepFail("Invalid --forge flag")
		return fail(err)
	}

	orgAllowlist := config.DefaultAllowedRemoteResources()
	if orgCfg != nil {
		orgAllowlist = orgCfg.AllowedResources()
	}
	gitToken := opts.rFlags.gitToken
	if gitToken == "" {
		var tokenErr error
		gitToken, tokenErr = resolveToken()
		if tokenErr != nil {
			printer.StepWarn("Git token not available; private repo harness fetches may fail")
		}
	}

	// The same event sources fullsend run reads for overlay resolution.
	var eventMap map[string]any
	if opts.eventFile != "" {
		if _, eventMap, err = readRunEventFile(opts.eventFile); err != nil {
			return fail(err)
		}
	} else {
		eventMap = extractNormalizedEventFromDispatch(absFullsendDir)
	}

	policy := fetch.DefaultPolicy
	policy.Offline = opts.rFlags.offline
	composed, err := composeRunHarness(ctx, runHarnessInput{
		agentName:       opts.agentName,
		fullsendDir:     opts.fullsendDir,
		absFullsendDir:  absFullsendDir,
		orgConfigPath:   orgConfigPath,
		orgCfg:          orgCfg,
		composeGitToken: gitToken,
		composeOpts:     newRunComposeOpts(absFullsendDir, policy, forgePlatform, orgAllowlist, opts.rFlags.treeFetcher, gitToken, eventMap, orgCfg),
	}, printer)
	if err != nil {
		return fail(err)
	}
	harnessPath = composed.harnessPath
	printer.StepDone("Loaded harness: " + harnessPath)

	h := composed.h
	image, source = h.Image, prepareSourceHarness
	if resolved, overridden := applySandboxImageOverride(h.Image); overridden {
		printer.StepInfo(fmt.Sprintf("Image override via FULLSEND_SANDBOX_IMAGE: %s -> %s", h.Image, resolved))
		image, source = resolved, prepareSourceOverride
	} else if image == "" {
		source = prepareSourceDefault
	}
	return image, source, harnessPath, time.Duration(h.SandboxTimeoutSeconds) * time.Second, nil
}

// displayPrepareImage renders image for progress output.
func displayPrepareImage(image string) string {
	if image == "" {
		return "(gateway default)"
	}
	return image
}

// warmPrepareImage puts img into the gateway's image store, recording the
// outcome on img. It pulls only into a store it knows is the gateway's: the
// local Podman store of a local gateway, which OpenShell's Podman driver
// pulls into. A remote gateway's store is out of reach from this host, so
// warming is left to the preflight sandbox, which the gateway pulls the
// image for itself.
func warmPrepareImage(ctx context.Context, img *prepareImage, remote bool, printer *ui.Printer) error {
	switch {
	case img.Ref == "":
		img.Status = prepareImageSkipped
		img.Detail = "no image configured; the gateway uses its default sandbox image"
		return nil
	case remote:
		img.Status = prepareImageSkipped
		img.Detail = "OPENSHELL_GATEWAY_ENDPOINT names a gateway whose image store is not on this host, so it was not pulled locally"
		printer.StepWarn("Not pulling " + img.Ref + ": " + img.Detail + "; the sandbox preflight pulls it through the gateway, anonymously")
		return nil
	}
	if _, err := imagePullLookPathFn("podman"); err != nil {
		img.Status = prepareImageSkipped
		img.Detail = "podman not found, so the gateway's image store could not be checked from this host"
		printer.StepWarn("Not pulling " + img.Ref + ": " + img.Detail + "; the sandbox preflight pulls it through the gateway")
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, sandboxImagePullTimeout)
	defer cancel()

	if localImageExists(ctx, img.Ref) {
		img.Status = prepareImageCached
		printer.StepDone("Image already present: " + img.Ref)
		return nil
	}

	var cred *registryCredential
	how := "anonymously"
	if c, ok := sandboxImageCredential(img.Ref); ok {
		cred, how = &c, "with "+c.source
	}
	printer.StepStart("Pulling sandbox image " + how + ": " + img.Ref)
	if err := pullLocalImage(ctx, img.Ref, cred); err != nil {
		img.Status = prepareImageFailed
		printer.StepFail("Failed to pull " + img.Ref)
		kind, hint := classifyPrepareFailure(err.Error(), prepareErrPull)
		return &prepareError{kind: kind, hint: hint, err: err}
	}
	img.Status = prepareImagePulled
	printer.StepDone("Pulled sandbox image " + img.Ref)
	return nil
}

// runPrepareSandboxPreflight creates a minimal sandbox from image — no
// providers, no policy, no workload — waits for it to be ready, and deletes
// it. One attempt: a preflight exists to fail fast. A failed creation is
// deleted by CreateWithRetry.
func runPrepareSandboxPreflight(pf *preparePreflight, image string, readyTimeout time.Duration, printer *ui.Printer) error {
	printer.StepStart("Creating preflight sandbox: " + pf.Sandbox)
	start := time.Now()
	if err := prepareCreateSandboxFn(pf.Sandbox, nil, image, "", 1, readyTimeout, false); err != nil {
		pf.Status = "failed"
		printer.StepFail("Preflight sandbox could not be created")
		kind, hint := classifyPrepareFailure(err.Error(), prepareErrPreflight)
		return &prepareError{kind: kind, hint: hint, err: fmt.Errorf("creating preflight sandbox: %w", err)}
	}
	pf.Status = "passed"
	printer.StepDone(fmt.Sprintf("Preflight sandbox ready (%.1fs)", time.Since(start).Seconds()))
	if err := deleteSandboxFn(pf.Sandbox); err != nil {
		printer.StepWarn("Could not delete preflight sandbox " + pf.Sandbox + ": " + err.Error())
	} else {
		printer.StepDone("Deleted preflight sandbox")
	}
	return nil
}

// classifyPrepareFailure maps an image pull or sandbox creation error
// message to a failure kind and an actionable hint, or returns fallback
// with no hint when the cause is not recognized.
func classifyPrepareFailure(msg, fallback string) (kind, hint string) {
	m := strings.ToLower(msg)
	containsAny := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(m, s) {
				return true
			}
		}
		return false
	}
	switch {
	case containsAny("no space left on device", "disk quota exceeded"):
		return prepareErrDiskSpace, "the host running the gateway's image store is out of disk space; free space (for example with 'podman system prune') or use a runner with a larger disk"
	case containsAny("unauthorized", "authentication required", "access to the resource is denied", "403 forbidden"):
		return prepareErrAuth, "the registry refused the pull; make the image public or provide the CI job's registry credential (GH_WORKFLOW_TOKEN with packages: read for ghcr.io on GitHub Actions, CI_REGISTRY_USER and CI_REGISTRY_PASSWORD for CI_REGISTRY on GitLab CI)"
	case containsAny("manifest unknown", "name unknown", "invalid reference format", "no such host", "not found: manifest", "repository does not exist"):
		return prepareErrUnresolvable, "the image reference could not be resolved; check the harness image: field and FULLSEND_SANDBOX_IMAGE"
	}
	return fallback, ""
}
