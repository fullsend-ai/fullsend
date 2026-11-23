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

	"github.com/fullsend-ai/fullsend/internal/agentnew"
	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/envfile"
	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// Sandbox operations fullsend prepare performs. Variables so tests can run
// the command without openshell or a gateway.
var (
	prepareEnsureOpenshellFn = sandbox.EnsureAvailable
	prepareCheckGatewayFn    = sandbox.CheckGateway
	prepareCreateSandboxFn   = sandbox.CreateWithRetryContext
)

// Image statuses reported by fullsend prepare. A local pull never makes an
// image "ready": only the preflight sandbox, which the gateway creates from
// the image out of its own image store, shows the gateway can use it.
const (
	prepareImageUnverified = "unverified" // not yet confirmed by the preflight sandbox
	prepareImageReady      = "ready"      // the preflight sandbox started from it
	prepareImageFailed     = "failed"     // the preflight could not start a sandbox from it
)

// Preflight statuses reported by fullsend prepare.
const (
	preparePreflightPassed = "passed"
	preparePreflightFailed = "failed"
)

// Image sources reported by fullsend prepare.
const (
	prepareSourceHarness  = "harness"                // the harness image: field
	prepareSourceOverride = "FULLSEND_SANDBOX_IMAGE" // the environment override
	prepareSourceDefault  = "gateway-default"        // no image: the gateway's default sandbox image
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
	prepareErrPreflight    = "sandbox-preflight"
	prepareErrCleanup      = "sandbox-cleanup"
	prepareErrCanceled     = "canceled"
)

// Preflight cleanup outcomes reported by fullsend prepare.
const (
	prepareCleanupDeleted = "deleted"
	prepareCleanupFailed  = "failed"
)

// prepareResult is the machine-readable result of fullsend prepare
// (--json). Fields are stable for programmatic callers.
type prepareResult struct {
	OK        bool                `json:"ok"`
	Agent     string              `json:"agent"`
	Harness   string              `json:"harness,omitempty"`
	Arch      string              `json:"arch"`
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
	// Cleanup is "deleted" or "failed" once a sandbox was created; "failed"
	// means the sandbox may still exist and needs deleting by hand.
	Cleanup string `json:"cleanup,omitempty"`
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
field, FULLSEND_SANDBOX_IMAGE override), pre-pull a private ghcr.io image with
the workflow token as 'fullsend run' does, and create and delete a minimal
sandbox under a restrictive policy to check that the gateway can start one. The
gateway pulls the image into its own image store when that sandbox starts. It
runs no pre-script, agent, or inference.

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

	for _, ef := range opts.envFiles {
		if err := envfile.Load(ef); err != nil {
			return res, fmt.Errorf("loading env file %s: %w", ef, err)
		}
	}
	// After the env files, which may set FULLSEND_SANDBOX_ARCH.
	res.Arch = sandboxArch()

	// Keep the runner secrets bundle out of every child process (podman,
	// openshell, git) without consuming it: the runs that follow still need
	// it, so it is restored on return rather than deleted.
	restoreEnv, err := hideRunnerSecretsEnv()
	if err != nil {
		return res, err
	}
	defer restoreEnv()

	if err := prepareCanceled(ctx); err != nil {
		return res, err
	}
	image, source, harnessPath, sandboxTimeout, err := resolvePrepareImage(ctx, opts, printer)
	res.Harness = harnessPath
	if err != nil {
		if cErr := prepareCanceled(ctx); cErr != nil {
			return res, cErr
		}
		return res, err
	}
	if err := prepareCanceled(ctx); err != nil {
		return res, err
	}
	res.Images = append(res.Images, prepareImage{Role: "sandbox", Ref: image, Source: source, Status: prepareImageUnverified})
	img := &res.Images[0]
	printer.KeyValue("Image", displayPrepareImage(image))
	printer.KeyValue("Arch", res.Arch)

	if err := prepareCanceled(ctx); err != nil {
		return res, err
	}
	printer.StepStart("Checking openshell availability")
	if err := prepareEnsureOpenshellFn(); err != nil {
		printer.StepFail("openshell not available")
		return res, &prepareError{kind: prepareErrOpenshell, err: fmt.Errorf("openshell is required: %w", err),
			hint: "install the OpenShell CLI (see the running agents locally guide)"}
	}
	if err := prepareCanceled(ctx); err != nil {
		return res, err
	}
	printer.StepStart("Checking gateway")
	if err := prepareCheckGatewayFn(); err != nil {
		printer.StepFail("Gateway not running")
		return res, &prepareError{kind: prepareErrGateway, err: fmt.Errorf("gateway check failed: %w", err),
			hint: "start the OpenShell gateway, or set OPENSHELL_GATEWAY_ENDPOINT to a running one"}
	}
	printer.StepDone("Gateway available")

	// fullsend run gets GH_WORKFLOW_TOKEN from minting its agent token;
	// prepare mints nothing, so it resolves the same token from the
	// environment (preparedWorkflowToken) for the image pull.
	workflowToken, tokenProblem := preparedWorkflowToken()
	if tokenProblem != "" {
		printer.StepWarn(tokenProblem)
	}

	if err := prepareCanceled(ctx); err != nil {
		return res, err
	}
	warmPrepareImage(ctx, img, workflowToken, printer)
	if err := prepareCanceled(ctx); err != nil {
		return res, err
	}

	policyPath, removePolicy, err := writePreflightPolicy()
	if err != nil {
		return res, &prepareError{kind: prepareErrConfig, err: err}
	}
	defer removePolicy()

	res.Preflight = &preparePreflight{Sandbox: generateSandboxName(opts.agentName)}
	if err := runPrepareSandboxPreflight(ctx, res.Preflight, image, policyPath, sandboxTimeout, printer); err != nil {
		// The image is at fault only when the gateway could not pull or
		// resolve it; other preflight failures say nothing about it.
		// A sandbox that reached Ready and failed only afterwards, in
		// teardown or cancellation, did start from the image.
		var pe *prepareError
		if res.Preflight.Status == preparePreflightPassed {
			img.Status = prepareImageReady
		} else if errors.As(err, &pe) && (pe.kind == prepareErrDiskSpace || pe.kind == prepareErrAuth || pe.kind == prepareErrUnresolvable) {
			img.Status = prepareImageFailed
		}
		return res, err
	}
	img.Status = prepareImageReady
	if image != "" {
		img.Detail += "; the preflight sandbox started from it, so the gateway has pulled it"
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
		agentName:      opts.agentName,
		fullsendDir:    opts.fullsendDir,
		absFullsendDir: absFullsendDir,
		orgConfigPath:  orgConfigPath,
		orgCfg:         orgCfg,
		composeOpts:    newRunComposeOpts(absFullsendDir, policy, forgePlatform, orgAllowlist, opts.rFlags.treeFetcher, gitToken, eventMap, orgCfg),
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

// preparedWorkflowToken returns the GitHub Actions workflow token for
// fullsend prepare, which does not mint an agent token and so never has
// GH_WORKFLOW_TOKEN preserved for it the way fullsend run does
// (mintAgentTokenAtLevel). It resolves the same value without minting and
// without touching the environment: an existing GH_WORKFLOW_TOKEN, else the
// caller's GH_TOKEN, else its GITHUB_TOKEN, which must look like a token.
// Outside GitHub Actions there is no workflow token and it returns "". A
// non-empty problem explains why a token that was present is not used.
func preparedWorkflowToken() (token, problem string) {
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		return "", ""
	}
	if existing := os.Getenv(workflowTokenEnv); existing != "" {
		return existing, ""
	}
	name := "GH_TOKEN"
	token = os.Getenv(name)
	if token == "" {
		name = "GITHUB_TOKEN"
		token = os.Getenv(name)
	}
	if token == "" {
		return "", ""
	}
	if !mintTokenPattern.MatchString(token) {
		return "", name + " has an unexpected format, so it is not used as the workflow token"
	}
	return token, ""
}

// displayPrepareImage renders image for progress output.
func displayPrepareImage(image string) string {
	if image == "" {
		return "(gateway default)"
	}
	return image
}

// prepareCanceled returns a canceled failure when ctx is done.
func prepareCanceled(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return &prepareError{kind: prepareErrCanceled, err: fmt.Errorf("preparation canceled: %w", err)}
	}
	return nil
}

// hideRunnerSecretsEnv removes the runner secrets bundle variables from the
// process environment, so no child process inherits them, and returns a
// function that puts them back. fullsend prepare must not consume the
// bundle the way fullsend run does (the runs that follow read it), so it is
// hidden for the command's duration and left in place afterwards.
func hideRunnerSecretsEnv() (restore func(), err error) {
	type saved struct{ name, value string }
	var kept []saved
	restore = func() {
		for _, s := range kept {
			_ = os.Setenv(s.name, s.value)
		}
	}
	for _, name := range []string{runnerSecretsFileEnv, runnerSecretsEnv} {
		value, ok := os.LookupEnv(name)
		if !ok {
			continue
		}
		if err := os.Unsetenv(name); err != nil {
			restore()
			return func() {}, fmt.Errorf("hiding %s from child processes: %w", name, err)
		}
		kept = append(kept, saved{name, value})
	}
	return restore, nil
}

// writePreflightPolicy writes the restrictive base sandbox policy — the
// filesystem, landlock and process rules generated agents start from, with
// no network access — to a private temporary file for the preflight sandbox,
// and returns its path and a function that removes it. OpenShell 0.1 refuses
// a sandbox with no policy, and the preflight runs no workload that needs
// more than this.
func writePreflightPolicy() (path string, remove func(), err error) {
	dir, err := os.MkdirTemp("", "fullsend-preflight-policy-")
	if err != nil {
		return "", func() {}, fmt.Errorf("creating preflight policy directory: %w", err)
	}
	remove = func() { _ = os.RemoveAll(dir) }
	path = filepath.Join(dir, "base.yaml")
	if err := os.WriteFile(path, agentnew.BasePolicy(), 0o600); err != nil {
		remove()
		return "", func() {}, fmt.Errorf("writing preflight policy: %w", err)
	}
	return path, remove, nil
}

// warmPrepareImage pre-pulls a private image the way fullsend run does
// (pullSandboxImage: the CI job's registry credential, private auth file,
// redacted output), and records on img what happened in this host's
// Podman store. It does not say the gateway's image store is warm: the
// gateway pulls into its own store when the preflight sandbox starts, which
// is what confirms the image. A failed pull is a warning, as in fullsend run,
// so a public image still works and a full disk fails in the preflight.
func warmPrepareImage(ctx context.Context, img *prepareImage, workflowToken string, printer *ui.Printer) {
	if img.Ref == "" {
		img.Detail = "no image configured; the gateway uses its default sandbox image"
		return
	}
	switch pullSandboxImage(ctx, img.Ref, workflowToken, printer) {
	case localPullCached:
		img.Detail = "already in the local Podman store"
	case localPullPulled:
		img.Detail = "pulled into the local Podman store with the job's registry credential"
	case localPullFailed:
		img.Detail = "the local pull did not complete; the gateway pulls the image without registry credentials"
	default:
		img.Detail = "no registry credential applies, so nothing was pulled locally; the gateway pulls the image into its own image store"
		printer.StepInfo(img.Detail)
	}
}

// runPrepareSandboxPreflight creates a minimal sandbox from image — no
// providers, no workload, the restrictive policy at policyPath — waits for it
// to be ready, and deletes it. One attempt: a preflight exists to fail fast.
// Cancelling ctx stops the wait. A failed creation is deleted by
// CreateWithRetryContext; a sandbox that cannot be deleted, either way, fails
// the preflight and is named so it can be removed by hand.
func runPrepareSandboxPreflight(ctx context.Context, pf *preparePreflight, image, policyPath string, readyTimeout time.Duration, printer *ui.Printer) error {
	printer.StepStart("Creating preflight sandbox: " + pf.Sandbox)
	start := time.Now()
	if err := prepareCreateSandboxFn(ctx, pf.Sandbox, nil, image, policyPath, 1, readyTimeout, false); err != nil {
		pf.Status = preparePreflightFailed
		if errors.Is(err, sandbox.ErrCleanupFailed) {
			pf.Cleanup = prepareCleanupFailed
		}
		if cErr := prepareCanceled(ctx); cErr != nil {
			printer.StepFail("Preflight sandbox canceled")
			var pe *prepareError
			if errors.As(cErr, &pe) {
				pe.err = fmt.Errorf("%w: %w", pe.err, err)
				pe.hint = prepareCleanupHint(pf)
			}
			return cErr
		}
		printer.StepFail("Preflight sandbox could not be created")
		kind, hint := classifyPrepareFailure(err.Error())
		if cleanup := prepareCleanupHint(pf); cleanup != "" {
			if hint != "" {
				hint += "; "
			}
			hint += cleanup
		}
		return &prepareError{kind: kind, hint: hint, err: fmt.Errorf("creating preflight sandbox: %w", err)}
	}
	pf.Status = preparePreflightPassed
	printer.StepDone(fmt.Sprintf("Preflight sandbox ready (%.1fs)", time.Since(start).Seconds()))
	// Deleted even when ctx was canceled meanwhile: the sandbox exists.
	if err := deleteSandboxFn(pf.Sandbox); err != nil {
		pf.Cleanup = prepareCleanupFailed
		printer.StepFail("Could not delete preflight sandbox " + pf.Sandbox)
		return &prepareError{kind: prepareErrCleanup, hint: prepareCleanupHint(pf),
			err: fmt.Errorf("deleting preflight sandbox %s: %w", pf.Sandbox, err)}
	}
	pf.Cleanup = prepareCleanupDeleted
	printer.StepDone("Deleted preflight sandbox")
	// A cancellation that arrived while the sandbox was becoming ready or
	// being deleted is reported after the cleanup, not swallowed.
	if cErr := prepareCanceled(ctx); cErr != nil {
		printer.StepFail("Preparation canceled")
		return cErr
	}
	return nil
}

// prepareCleanupHint says how to remove a preflight sandbox that was not
// deleted, or returns "" when it was.
func prepareCleanupHint(pf *preparePreflight) string {
	if pf.Cleanup != prepareCleanupFailed {
		return ""
	}
	return "the preflight sandbox may still exist; delete it with 'openshell sandbox delete " + pf.Sandbox + "'"
}

// classifyPrepareFailure maps a sandbox creation error message to a failure
// kind and an actionable hint, or returns the generic preflight kind with no
// hint when the cause is not recognized.
func classifyPrepareFailure(msg string) (kind, hint string) {
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
		return prepareErrAuth, "the registry refused the pull; make the image public or provide the CI job's registry credential (on GitHub Actions the workflow token, GH_WORKFLOW_TOKEN or the job's GH_TOKEN/GITHUB_TOKEN, with packages: read for ghcr.io; on GitLab CI CI_REGISTRY_USER and CI_REGISTRY_PASSWORD for an image on CI_REGISTRY). The credential is applied to this host's Podman store, so a gateway on another host still pulls without it"
	case containsAny("manifest unknown", "name unknown", "invalid reference format", "no such host", "not found: manifest", "repository does not exist"):
		return prepareErrUnresolvable, "the image reference could not be resolved; check the harness image: field and FULLSEND_SANDBOX_IMAGE"
	}
	return prepareErrPreflight, ""
}
