package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/gitfetch"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// readRunEventFile reads and validates the normalized event JSON file named
// by --event-file and returns the event and its CEL map form (ADR 0088).
func readRunEventFile(path string) (*normevent.Event, map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("reading event file %s: %w", path, err)
	}
	ev, err := normevent.ParseJSON(data)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing event file %s: %w", path, err)
	}
	m, err := ev.ToMap()
	if err != nil {
		return nil, nil, fmt.Errorf("converting event to map: %w", err)
	}
	return ev, m, nil
}

// newRunComposeOpts returns the harness composition options fullsend run
// and fullsend prepare share. Callers add run-only fields, such as
// RunnerSecretNames, themselves.
func newRunComposeOpts(absFullsendDir string, policy fetch.FetchPolicy, forgePlatform string, orgAllowlist []string, treeFetcher gitfetch.TreeFetchFunc, gitToken string, eventMap map[string]any, orgCfg config.ConfigReader) harness.ComposeOpts {
	return harness.ComposeOpts{
		WorkspaceRoot: absFullsendDir,
		FetchPolicy:   policy,
		AuditLogPath:  filepath.Join(absFullsendDir, ".fullsend-cache", "fetch-audit.jsonl"),
		ForgePlatform: forgePlatform,
		OrgAllowlist:  orgAllowlist,
		TreeFetcher:   treeFetcher,
		GitToken:      gitToken,
		GitHubURL:     forge.ConfiguredGitHubURL(),
		Event:         eventMap,
		Config:        harness.BuildConfigMap(orgCfg),
	}
}

// runHarnessInput is what composeRunHarness needs to resolve and compose an
// agent's harness.
type runHarnessInput struct {
	agentName      string
	fullsendDir    string // as the user gave it, for command hints
	absFullsendDir string
	orgConfigPath  string
	orgCfg         config.ConfigWriter // nil when no config exists
	composeOpts    harness.ComposeOpts
}

// composedRunHarness is the harness composeRunHarness produced, with the
// state the rest of a run reads.
type composedRunHarness struct {
	h           *harness.Harness
	harnessPath string
	fetchDeps   []harness.Dependency
	baseDeps    []harness.Dependency
	lockState   runLockState
	// orgCfg may have been loaded strictly for a URL base when the input
	// had none.
	orgCfg              config.ConfigWriter
	composeOpts         harness.ComposeOpts
	fallbackForgeClient *gh.LiveClient
}

// composeRunHarness resolves an agent's harness source and composes it
// (base chain, agents: harness blocks, lock pins, overlays), exactly as
// fullsend run does. fullsend prepare calls it too, so the images it warms
// cannot drift from the ones a run uses.
func composeRunHarness(ctx context.Context, in runHarnessInput, printer *ui.Printer) (composedRunHarness, error) {
	agentName, orgCfg, composeOpts := in.agentName, in.orgCfg, in.composeOpts

	// Resolve agent source: config agents take precedence, then agents repo
	// fallback. Always create a GitHub client for the agents-repo fallback —
	// fullsend-ai/agents is public, so unauthenticated requests work when no
	// GitHub token is available (e.g., on GitLab pipelines). The client
	// stays on api.github.com unless GITHUB_API_URL is the API of the
	// GITHUB_SERVER_URL instance; then the fallback resolves and reads the
	// agents repo there (agentsRepoHostURLPrefix). GITHUB_API_URL alone
	// never receives the compose token or picks the fallback commit.
	fallbackForgeClient := newConfiguredHostGitHubClient(composeOpts.GitToken)
	// The configured host serves raw content from itself, so allow it in the
	// fetch policy too; the default policy names only github.com hosts.
	composeOpts.FetchPolicy = withAgentsRepoHostAllowed(composeOpts.FetchPolicy, fallbackForgeClient)
	harnessPath, fetchDeps, err := resolveAgentSource(ctx, in.absFullsendDir, agentName, fallbackForgeClient, orgCfg, composeOpts, printer)
	if err != nil {
		return composedRunHarness{}, err
	}

	printer.StepStart("Loading harness: " + harnessPath)

	// If the agent was fetched from a URL, forward the source URL so
	// LoadWithBase can resolve relative resources even without a base:
	// field (ADR-0045 resource resolution for config-registered agents).
	if len(fetchDeps) > 0 && fetchDeps[0].URL != "" {
		composeOpts.SourceURL = fetchDeps[0].URL
		useAgentsRepoHostFetch(&composeOpts, fallbackForgeClient)
	}

	// The harness path is known now, so the lock-update advice can name
	// the forge platform: for a child with no forge: block run under one,
	// the advised re-lock must carry --forge or it composes with platform
	// "" and drops the forge-specific base files (#8988).
	composeOpts.LockUpdateCommand = lockUpdateHint(agentName, in.fullsendDir, harnessPath, composeOpts.ForgePlatform, true)

	// If the harness has a URL base and org config failed to load,
	// load it strictly now so LoadWithBase gets a proper error path
	// rather than an unhelpful "URL base requires allowed_remote_resources".
	if orgCfg == nil {
		if rawH, rawErr := harness.LoadRaw(harnessPath); rawErr == nil && rawH.Base != "" && harness.IsURL(rawH.Base) {
			orgCfg, err = requireOrgConfig(in.orgConfigPath, printer)
			if err != nil {
				return composedRunHarness{}, err
			}
			composeOpts.OrgAllowlist = orgCfg.AllowedResources()
		}
	}

	// The agent's agents: entry may carry harness: blocks, composed on the
	// harness as child layers (ADR 0162).
	if orgCfg != nil {
		composeOpts.Blocks = config.HarnessBlocksFor(orgCfg.AgentEntries(), agentName)
		composeOpts.Agent = agentName
	}

	// A current lock entry pins the relative files of a URL base, so pass
	// its pins in before composition fetches them (#8979).
	lockState, err := readRunLock(in.absFullsendDir, agentName, harnessPath, composeOpts.Blocks)
	if err != nil {
		return composedRunHarness{}, err
	}
	switch {
	case lockState.loadErr != nil:
		printer.StepWarn("Could not load lock file: " + lockState.loadErr.Error())
	case lockState.stale:
		// A plain lock re-composes a stale entry (lock returns early only
		// when the entry is current), so the stale advice carries no
		// --update: it would set BypassURLIndex and re-fetch every
		// un-pinned relative base file from upstream, taking changes the
		// user was not told about, where the URL index's cached copies
		// re-compose the entry as before. The hint still carries --forge
		// and --fullsend-dir exactly like run's other lock advice (#8988),
		// and the command ends the sentence: a period right after its last
		// argument would be copied with it and break the advice as written.
		printer.StepWarn("Harness has changed since lock file was generated; run: " + lockUpdateHint(agentName, in.fullsendDir, harnessPath, composeOpts.ForgePlatform, false))
	case len(lockState.pins) > 0:
		noun := "dependency"
		if len(lockState.pins) > 1 {
			noun = "dependencies"
		}
		printer.StepInfo(fmt.Sprintf("Pinning %d %s from lock file", len(lockState.pins), noun))
	}
	composeOpts.LockedPins = lockState.pins

	h, baseDeps, err := harness.LoadWithBase(ctx, harnessPath, composeOpts)
	if err != nil {
		printer.StepFail("Failed to load harness")
		return composedRunHarness{}, fmt.Errorf("loading harness: %w", err)
	}
	// An entry written before these files were locked is still current but
	// leaves them to the URL index (#8979).
	if lockState.entry != nil && !lockState.stale {
		if unpinned := unpinnedDeps(baseDeps, lockState.pins); len(unpinned) > 0 {
			// Without --forge, lock reads forge keys from the child only
			// (#8988), so a child with no forge: block needs the platform
			// named. A child with forge keys is locked for all of them.
			// The name and first dependency are control-escaped, and a
			// name that starts with '-' is guarded behind "--", in
			// unpinnedDepsWarning so a crafted name can neither forge log
			// lines nor read as a flag.
			printer.StepWarn(unpinnedDepsWarning(agentName, unpinned, in.fullsendDir, harnessPath, composeOpts.ForgePlatform))
		}
	}

	return composedRunHarness{
		h:                   h,
		harnessPath:         harnessPath,
		fetchDeps:           fetchDeps,
		baseDeps:            baseDeps,
		lockState:           lockState,
		orgCfg:              orgCfg,
		composeOpts:         composeOpts,
		fallbackForgeClient: fallbackForgeClient,
	}, nil
}
