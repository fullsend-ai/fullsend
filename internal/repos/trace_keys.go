package repos

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/preset"
	"github.com/fullsend-ai/fullsend/internal/telemetry/tracecontext"
)

// removeTraceKeyFlag is the `repos install` flag behind
// TraceKeysConfig.RemoveKeyIDs. Removal errors name it, never its value.
const removeTraceKeyFlag = "--remove-trace-verification-key"

// TraceKeysConfig configures EnsureTraceSigningKeys. Manifest,
// ManifestPath, DryRun, Deferred, RepoFilter and ExcludeRepos mean what
// they mean in ManagedConfigWritebackConfig.
type TraceKeysConfig struct {
	Manifest     *Manifest
	ManifestPath string
	DryRun       bool
	Deferred     bool
	RepoFilter   []string
	ExcludeRepos []string
	// Rotate is --rotate-trace-signing-key: generate a new active key even
	// when the installed one is healthy. On an existing install without a
	// trace_context block the first rotation provisions the keys. On
	// GitLab, a rotation waits, like any key write, until the fullsend jobs
	// its fullsend_ref delivers and, while a fullsend workflow is on the
	// default branch, the jobs committed there unset
	// FULLSEND_TRACE_SIGNING_KEY.
	Rotate bool
	// RemoveKeyIDs is --remove-trace-verification-key: the hex key IDs of
	// retained verification keys to drop from the manifest in this run.
	RemoveKeyIDs []string
	// UpstreamRef and UpstreamTag are the running release's ref and tag,
	// as in ConvergeConfig. On GitLab, a fullsend_ref that names neither
	// pins another release, whose fullsend jobs install delivers.
	UpstreamRef string
	UpstreamTag string
	// Rand is the source of new seeds and installation IDs; nil means
	// crypto/rand.
	Rand io.Reader
}

// TraceKeyPlan is one repository's pending trace-key write. Key holds the
// new seed. Reason is "fresh install", "recovery", "rotation" or, on
// GitLab, "exposed" (the stored seed was not a masked, protected,
// wildcard-scoped environment variable).
type TraceKeyPlan struct {
	Owner, Repo, Forge string
	Key                tracecontext.SigningKey
	Reason             string
	// Ledger is the FULLSEND_TRACE_SIGNING_KEY_ID value the preflight
	// read, or "" when the variable was absent. It is a key ID, never key
	// material.
	Ledger string
}

// Trace-key preflight refusals. ErrTraceManifestStale is wrapped with the
// owner/repo and key IDs only, never key material.
var (
	ErrTraceManifestStale    = errors.New("manifest is stale")
	ErrTraceManifestReadOnly = errors.New("manifest is read-only")
)

// EnsureTraceSigningKeys runs in the repos install preflight, after
// EnsureManagedConfigDefaults and before the manifest is persisted, with
// the same DryRun/Deferred contract. For each selected repository it reads
// the committed .fullsend/config.yaml trace_context, the
// FULLSEND_TRACE_SIGNING_KEY_ID ledger and whether
// FULLSEND_TRACE_SIGNING_KEY exists (on GitLab, also whether it is a
// masked, protected, wildcard-scoped environment variable), then:
//   - refuses with ErrTraceManifestStale, before any edit, when the
//     installed installation_id differs from the manifest's; an installed
//     active or retained key is missing from the manifest (unless
//     RemoveKeyIDs names it); the installed config has trace_context and
//     the manifest has none, unless no fullsend workflow, secret or ledger
//     remains (a full uninstall, which keeps the committed config on
//     GitHub: the repository is then a fresh install with a new
//     installation ID); the ledger names a key outside the manifest's
//     active and retained keys; or another manifest entry uses the same
//     installation_id. The first two reasons do not apply to a committed
//     block of another installation while no fullsend workflow is on the
//     default branch: that block belongs to an ended installation that the
//     entry's replaces, and stays committed until the commit of the install
//     that enrolled the repository again lands;
//   - plans nothing when the secret exists, the ledger equals the active
//     key's ID and Rotate is false, or when the repository is an existing
//     install without trace_context and Rotate is false. GitLab passes the
//     protected seed to every job on a protected branch or tag, so on
//     GitLab a repository whose fullsend_ref pins a release other than the
//     running one gets no plan and its entry records no new key, whatever
//     the reason for the key, unless the poll, dispatcher and agent jobs of
//     that release, which install delivers, each unset
//     FULLSEND_TRACE_SIGNING_KEY on their first before_script line. Reading
//     those jobs refuses the run on any error but a missing template or
//     job, except for a fresh install that a read-only manifest leaves
//     without keys (below). While a fullsend workflow is on the default
//     branch, where fullsend's schedules and dispatch run, a GitLab
//     repository also gets no plan and no new key in its entry, whatever
//     the reason for the key, until the jobs committed there unset it the
//     same way, and reading them refuses the run the same way. Once the
//     refusals have passed, progress names the fullsend_ref to move (and,
//     for a fresh install, says to rotate once it has moved), or says why
//     the key waits and to run install again once install's scaffold change
//     is on the default branch, rotating for an existing install's first
//     keys or a rotation, with a writable manifest when it is read-only;
//   - otherwise generates a key (and, when absent, an installation ID),
//     moves the previous active key to retained, edits the in-memory
//     manifest entry (carving an explicit entry out of a glob like
//     EnsureManagedConfigDefaults) and returns a plan;
//   - drops each RemoveKeyIDs key from every selected repository that
//     retains it, and refuses, before any edit, a key ID that is active in
//     a selected repository, that a selected repository's
//     FULLSEND_TRACE_SIGNING_KEY_ID names (a retained key only after an
//     interrupted run, until a run without the removal writes a new key), or
//     that no selected repository retains. Keys retained before this run
//     count, so a run never removes the key it replaces;
//   - refuses with ErrTraceManifestReadOnly, before any edit, when a plan
//     or removal is needed and the manifest is read-only, except for a
//     fresh install without Rotate, including one whose committed config
//     keeps an ended installation's trace_context or whose pinned GitLab
//     jobs cannot be read: that repository gets no plan and no edit, and
//     progress says its keys were not provisioned and that a later
//     rotation with a writable manifest provisions them.
//
// Plans are keyed "owner/repo". A removal alone returns no plan, since a
// plan means a key write. Repositories that fail RequireInferenceAuth are
// skipped. Progress names the installation ID and key ID of each plan,
// never the seed, and, once the refusals have passed, the ID and key IDs of
// each ended installation whose block is still committed, with or without
// a plan.
func EnsureTraceSigningKeys(ctx context.Context, cfg TraceKeysConfig, clients ForgeClientFactory, progress ProgressFunc) (map[string]*TraceKeyPlan, error) {
	if cfg.Manifest == nil {
		return nil, fmt.Errorf("manifest is required")
	}
	if progress == nil {
		progress = func(_, _, _ string) {}
	}
	remove, err := ParseTraceKeyIDs(cfg.RemoveKeyIDs)
	if err != nil {
		return nil, err
	}
	removeSet := make(map[[8]byte]bool, len(remove))
	for _, id := range remove {
		removeSet[id] = true
	}

	m := cfg.Manifest
	selected, err := selectManifestRepos(ctx, m, clients, cfg.RepoFilter)
	if err != nil {
		return nil, err
	}
	pinned := &gitlabPinnedJobs{
		clients: clients, manifest: m, upstreamRef: cfg.UpstreamRef, upstreamTag: cfg.UpstreamTag,
		byPin: make(map[string]pinnedJobs),
	}
	readOnly := ManifestReadOnly(m, cfg.ManifestPath)
	var repos []*traceRepo
	var errs []error
	for _, rr := range withoutRepos(selected, cfg.ExcludeRepos) {
		// Convergence fails a repository without inference
		// authentication before its key writes, so it gains no key.
		if m.ResolveConfigForEntry(rr.Owner, rr.Repo, rr.Forge, rr.Entry).RequireInferenceAuth() != nil {
			continue
		}
		t, err := readTraceRepo(ctx, m, clients, rr, cfg.Rotate, readOnly, removeSet, pinned)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		repos = append(repos, t)
	}
	errs = append(errs, checkTraceRemovals(repos, remove)...)
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	var edited []string
	for _, t := range repos {
		t.dropRetained(removeSet)
		// A fresh install goes ahead without keys when the manifest cannot
		// record them, also one that enrolls a fully uninstalled repository
		// again: install's commit then removes the ended installation's
		// block. An explicit rotation still needs the edit.
		if readOnly && !cfg.Rotate && t.reason == "fresh install" {
			t.reason, t.readOnlySkip = "", true
		}
		if t.reason != "" || len(t.dropped) > 0 {
			edited = append(edited, t.name())
		}
	}
	if readOnly && len(edited) > 0 {
		return nil, fmt.Errorf("%w: recording the trace keys of %s needs a repos.yaml edit; repos.yaml and the trace keys were not changed: re-run with a writable local manifest",
			ErrTraceManifestReadOnly, strings.Join(edited, ", "))
	}
	// The rotation a notice asks for records keys in repos.yaml.
	writable := ""
	if readOnly {
		writable = " with a writable manifest"
	}
	for _, t := range repos {
		// A notice names the repository in the run it asks for, so that
		// run does not rotate the other selected repositories.
		switch {
		case t.readOnlySkip:
			progress(t.name(), "manifest", "Trace signing keys not provisioned: repos.yaml is read-only; "+
				"a later repos install "+t.name()+" --rotate-trace-signing-key with a writable manifest provisions them")
		case t.awaitingUnset:
			// An existing install's first keys need the rotation again; a
			// keyed repository's next run needs it only for a rotation.
			head, flag := "Trace signing keys not provisioned", " --rotate-trace-signing-key"
			if t.manifest != nil {
				head = "Trace signing key not written (" + t.withheld + ")"
				if t.withheld != "rotation" {
					flag = ""
				}
			}
			progress(t.name(), "manifest", fmt.Sprintf("%s: the committed fullsend GitLab jobs do not unset %s yet; "+
				"run repos install %s%s again%s once the scaffold change this install commits is on the default branch",
				head, forge.SecretTraceSigningKey, t.name(), flag, writable))
		case t.pin != "":
			setting := ForgeGitLab + ".fullsend_ref"
			if t.rr.Entry.FullsendRef != "" {
				setting = "the fullsend_ref of its repos.yaml entry"
			}
			// Once install's commit is on the default branch, a fresh
			// install that went ahead without keys is an existing install,
			// which only a rotation provisions.
			then := ""
			if t.withheld == "fresh install" {
				then = ", then run repos install " + t.name() + " --rotate-trace-signing-key" + writable + " to provision the keys"
			}
			progress(t.name(), "manifest", fmt.Sprintf("Trace signing key not written (%s): the fullsend GitLab jobs of fullsend_ref %s do not unset %s, "+
				"and GitLab would pass the seed to them; move %s to a release whose jobs unset it%s",
				t.withheld, t.pin, forge.SecretTraceSigningKey, setting, then))
		}
		if t.ended {
			// An entry left without an installation renders no trace_context.
			fate := "the repos.yaml entry's installation replaces it and stops trusting"
			if t.block == nil && t.reason == "" {
				fate = "install's commit removes it, so the repository stops trusting"
			}
			progress(t.name(), "manifest", fmt.Sprintf("The committed %s has the trace_context of the ended installation %s: %s %s",
				preset.OverlayPath, tracecontext.FormatInstallationID(t.installed.installation), fate, keyIDs(t.installed.ids())))
		}
	}
	if len(edited) == 0 {
		return nil, nil
	}

	plans := make(map[string]*TraceKeyPlan)
	for _, t := range repos {
		if t.reason == "" && len(t.dropped) == 0 {
			continue
		}
		if t.reason != "" {
			key, err := t.provision(cfg.Rand)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", t.name(), err)
			}
			p := &TraceKeyPlan{Owner: t.rr.Owner, Repo: t.rr.Repo, Forge: t.rr.Forge, Key: key, Reason: t.reason}
			if t.ledger != nil {
				p.Ledger = tracecontext.FormatKeyID(*t.ledger)
			}
			plans[t.name()] = p
		}
		entry := findExplicitEntry(m, t.name())
		if entry == nil {
			carved, _, err := carveOutForForge(m, t.name(), t.rr.Forge)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", t.name(), err)
			}
			if carved == nil {
				return nil, fmt.Errorf("no manifest entry or glob covers %q", t.name())
			}
			entry = carved
		}
		entry.Config = entry.Config.WithTraceContext(t.next)
	}
	if err := validateManifestManaged(m); err != nil {
		return nil, fmt.Errorf("validating manifest after recording trace keys: %w", err)
	}
	if cfg.ManifestPath != "" && !cfg.DryRun {
		if err := writeManifest(cfg.ManifestPath, m); err != nil {
			return nil, err
		}
	}

	verb := "Persisted"
	if cfg.DryRun && !cfg.Deferred {
		verb = "Would persist"
	}
	for _, t := range repos {
		if p := plans[t.name()]; p != nil {
			progress(t.name(), "manifest", fmt.Sprintf("%s config.trace_context in the manifest entry: installation ID %s, new active key ID %s (%s)",
				verb, t.next.InstallationID, tracecontext.FormatKeyID(p.Key.KeyID()), p.Reason))
		}
		if len(t.exposedIDs) > 0 {
			stays := "stays trusted as a retained key until you remove it"
			if len(t.exposedIDs) > 1 {
				stays = "stay trusted as retained keys until you remove them"
			}
			progress(t.name(), "manifest", fmt.Sprintf("%s was exposed (%s): %s %s with %s in a later run",
				forge.SecretTraceSigningKey, t.defect, keyIDs(t.exposedIDs), stays, removeTraceKeyFlag))
		}
		if len(t.dropped) > 0 {
			progress(t.name(), "manifest", fmt.Sprintf("%s the removal of retained trace verification %s from the manifest entry",
				verb, keyIDs(t.dropped)))
		}
	}
	return plans, nil
}

// ParseTraceKeyIDs parses the --remove-trace-verification-key values, each
// exactly 16 lowercase hex characters, in order. The error names the flag
// and the value's position, never the value: a seed pasted by mistake must
// not reach logs.
func ParseTraceKeyIDs(ids []string) ([][8]byte, error) {
	out := make([][8]byte, len(ids))
	for i, s := range ids {
		id, err := tracecontext.ParseKeyID(s)
		if err != nil {
			return nil, fmt.Errorf("%s value %d is not a key ID: use the key's 16 lowercase hex characters", removeTraceKeyFlag, i+1)
		}
		out[i] = id
	}
	return out, nil
}

// keyIDs names hex key IDs for a message: "key ID a" or "key IDs a, b".
func keyIDs(ids []string) string {
	if len(ids) == 1 {
		return "key ID " + ids[0]
	}
	return "key IDs " + strings.Join(ids, ", ")
}

// traceKeySet is a validated trace_context block's installation ID and
// verification keys, so they are compared as bytes.
type traceKeySet struct {
	installation [16]byte
	active       [8]byte
	keys         map[[8]byte]ed25519.PublicKey // active and retained, by key ID
}

// parseTraceKeySet parses tc; a nil block yields a nil set. Errors name the
// field, never the value.
func parseTraceKeySet(tc *config.TraceContextConfig) (*traceKeySet, error) {
	if tc == nil {
		return nil, nil
	}
	installation, err := tc.Installation()
	if err != nil {
		return nil, err
	}
	active, err := tc.ActiveKey()
	if err != nil {
		return nil, err
	}
	keys, err := tc.KeySet()
	if err != nil {
		return nil, err
	}
	return &traceKeySet{installation: installation, active: tracecontext.KeyID(active), keys: keys}, nil
}

// has reports whether s holds exactly pub under id.
func (s *traceKeySet) has(id [8]byte, pub ed25519.PublicKey) bool {
	k, ok := s.keys[id]
	return ok && bytes.Equal(k, pub)
}

// ids returns the hex IDs of s's keys, sorted.
func (s *traceKeySet) ids() []string {
	ids := make([]string, 0, len(s.keys))
	for id := range s.keys {
		ids = append(ids, tracecontext.FormatKeyID(id))
	}
	slices.Sort(ids)
	return ids
}

// traceRepo is one selected repository's trace-key state, read before any
// manifest edit.
type traceRepo struct {
	rr ResolvedRepo
	// block is the repos.yaml entry's trace_context (a copy), or nil;
	// manifest and installed are the parsed entry and committed blocks.
	block     *config.TraceContextConfig
	manifest  *traceKeySet
	installed *traceKeySet
	// ended reports that installed belongs to an installation that ended,
	// as with a full uninstall. The installation in the repos.yaml entry,
	// or the one a fresh install adds, replaces it; when the entry gets
	// none, install's commit removes it.
	ended bool
	// reason is why a key is planned; "" plans none.
	reason string
	// readOnlySkip reports a fresh install that goes ahead without keys
	// because the manifest is read-only.
	readOnlySkip bool
	// awaitingUnset reports that no key is planned on GitLab because a
	// fullsend job on the default branch does not unset the seed first.
	awaitingUnset bool
	// pin is the fullsend_ref whose delivered GitLab jobs do not unset the
	// seed first, so no key is planned. withheld is why a key would have
	// been planned when either check withholds it.
	pin, withheld string
	// defect and exposedIDs describe an exposed GitLab seed: why it is
	// exposed, and the key IDs it may belong to.
	defect     string
	exposedIDs []string
	// next is the entry's new block; dropped lists the key IDs a removal
	// takes out of it.
	next    *config.TraceContextConfig
	dropped []string
	// ledger is the key ID that FULLSEND_TRACE_SIGNING_KEY_ID names, one of
	// the entry's keys, or nil when the variable is absent.
	ledger *[8]byte
}

func (t *traceRepo) name() string { return t.rr.Owner + "/" + t.rr.Repo }

// readTraceRepo reads rr's committed trace_context, ledger and secret,
// refuses a stale manifest entry, and decides whether a key is planned. On
// GitLab, a needed key waits for the fullsend jobs of rr's pinned release
// and, while a fullsend workflow is on the default branch, for the jobs
// committed there. The committed jobs are read only for such a key, and so
// is the workflow marker of a keyed entry, unless its committed block names
// another installation: the marker then tells an ended installation from a
// stale entry first. A fresh install from a read-only manifest without
// rotate gets no key, so an error reading its pinned jobs does not refuse it.
func readTraceRepo(ctx context.Context, m *Manifest, clients ForgeClientFactory, rr ResolvedRepo, rotate, readOnly bool, remove map[[8]byte]bool, pinned *gitlabPinnedJobs) (*traceRepo, error) {
	t := &traceRepo{rr: rr}
	name := t.name()
	if w := rr.Entry.Config.Writer(); w != nil {
		t.block = w.TraceContext()
	}
	var err error
	if t.manifest, err = parseTraceKeySet(t.block); err != nil {
		return nil, fmt.Errorf("%s: repos.yaml entry: %w", name, err)
	}
	fc, err := clients.ConfigFor(rr.Forge)
	if err != nil {
		return nil, fmt.Errorf("creating %q client for %s: %w", rr.Forge, name, err)
	}
	committed, err := installedTraceContext(ctx, fc.Client, rr.Owner, rr.Repo, rr.Forge)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if t.installed, err = parseTraceKeySet(committed); err != nil {
		return nil, fmt.Errorf("%s: committed %s: %w", name, preset.OverlayPath, err)
	}
	ledger, ledgerExists, err := fc.Client.GetRepoVariable(ctx, rr.Owner, rr.Repo, forge.VarTraceSigningKeyID)
	if err != nil {
		return nil, fmt.Errorf("%s: reading %s: %w", name, forge.VarTraceSigningKeyID, err)
	}
	ledgerID, ledgerErr := tracecontext.ParseKeyID(ledger)

	// An entry without keys provisions them on a fresh install, and on an
	// existing install only when the operator rotates.
	hasWorkflow := false
	otherInstallation := t.manifest != nil && t.installed != nil && t.installed.installation != t.manifest.installation
	markerRead := t.manifest == nil || otherInstallation
	if markerRead {
		if _, _, hasWorkflow, err = readWorkflowMarker(ctx, fc.Client, rr.Owner, rr.Repo, fc); err != nil {
			return nil, fmt.Errorf("%s: checking workflow file: %w", name, err)
		}
	}
	// A full uninstall leaves no workflow, seed or ledger, but on GitHub it
	// keeps the committed block. That block belongs to an ended
	// installation, so the repository is a fresh install, and convergence
	// replaces or removes the block.
	if t.manifest == nil && t.installed != nil && !hasWorkflow && !ledgerExists {
		exists, _, err := readTraceSecret(ctx, fc.Client, rr)
		if err != nil {
			return nil, fmt.Errorf("%s: checking secret %s: %w", name, forge.SecretTraceSigningKey, err)
		}
		t.ended = !exists
	}
	// The ended block stays committed until the commit of the install that
	// enrolled the repository again lands, by default through a pull
	// request, so later runs and retries find it beside the entry's block.
	// While no fullsend workflow is on the default branch, a committed block
	// of another installation is that ended installation's: the entry's
	// installation replaces it.
	if otherInstallation && !hasWorkflow {
		t.ended = true
	}

	var whys []string
	pendingRemoval := false
	if !t.ended {
		whys, pendingRemoval = t.staleReasons(remove)
	}
	// Only a ledger naming one of the entry's keys can be explained by an
	// interrupted run; any other ledger was written for another key set.
	if ledgerExists && (ledgerErr != nil || t.manifest == nil || t.manifest.keys[ledgerID] == nil) {
		whys = append(whys, fmt.Sprintf("%s does not name a key the repos.yaml entry lists", forge.VarTraceSigningKeyID))
	}
	if t.manifest != nil {
		if other := traceInstallationUser(m, name, t.manifest.installation); other != "" {
			whys = append(whys, fmt.Sprintf("its trace_context.installation_id is also used by the repos.yaml entry %s; every repository needs its own", other))
		}
	}
	if len(whys) > 0 {
		// While a removal's change has not merged, the entry is the one the
		// last install wrote: restoring it helps only an older copy.
		restore := "restore the entry's trace_context from the repos.yaml the last install wrote"
		if pendingRemoval {
			restore = "if this repos.yaml is an older copy, " + restore
		}
		return nil, fmt.Errorf("%w for %s: %s; repos.yaml and the trace keys were not changed: %s",
			ErrTraceManifestStale, name, strings.Join(whys, "; "), restore)
	}
	if ledgerExists {
		t.ledger = &ledgerID
	}

	if t.manifest == nil {
		switch {
		case !hasWorkflow:
			t.reason = "fresh install"
		case rotate:
			t.reason = "rotation"
		}
	} else {
		secretExists, defect, err := readTraceSecret(ctx, fc.Client, rr)
		if err != nil {
			return nil, fmt.Errorf("%s: checking secret %s: %w", name, forge.SecretTraceSigningKey, err)
		}
		t.defect = defect
		switch {
		case !secretExists:
			t.reason = "recovery"
		case t.defect != "":
			// The stored seed belongs to the active key or, after an
			// interrupted run, to the key the ledger names; both stay
			// trusted until the operator removes them.
			t.reason = "exposed"
			t.exposedIDs = []string{tracecontext.FormatKeyID(t.manifest.active)}
			if ledgerExists && ledgerID != t.manifest.active {
				t.exposedIDs = append(t.exposedIDs, tracecontext.FormatKeyID(ledgerID))
			}
		case !ledgerExists || ledgerID != t.manifest.active:
			t.reason = "recovery"
		case rotate:
			t.reason = "rotation"
		}
	}
	if t.reason == "" || rr.Forge != ForgeGitLab {
		return t, nil
	}

	// GitLab passes the protected seed to every job on a protected branch
	// or tag, so a key waits until the fullsend jobs that receive it unset
	// it first (ADR 0154). Install delivers the jobs of a pinned
	// fullsend_ref, so a release whose jobs do not unset it gets no key.
	pin, unset, err := pinned.unset(ctx, rr)
	if err != nil {
		// A read-only manifest leaves a fresh install without keys anyway;
		// convergence fails it on its own when it cannot read the jobs
		// either.
		if readOnly && !rotate && t.reason == "fresh install" {
			return t, nil
		}
		return nil, fmt.Errorf("%s: reading the fullsend GitLab jobs of fullsend_ref %q: %w", name, pin, err)
	}
	// A withheld key is not written: an exposed seed stays active, so its
	// key IDs are not reported as retained.
	withheld := t.reason
	if t.defect != "" {
		withheld += ": " + t.defect
	}
	if !unset {
		t.pin, t.withheld = pin, withheld
		t.reason, t.exposedIDs = "", nil
		return t, nil
	}
	// Install commits the scaffold change whose jobs unset the seed only
	// after the seed write, so while a fullsend workflow is on the default
	// branch, where fullsend's schedules and dispatch run, a key also waits
	// until the jobs there unset it. Without one, as on a fresh install or
	// before install's own change merges, no fullsend job runs there.
	if !markerRead {
		if _, _, hasWorkflow, err = readWorkflowMarker(ctx, fc.Client, rr.Owner, rr.Repo, fc); err != nil {
			return nil, fmt.Errorf("%s: checking workflow file: %w", name, err)
		}
	}
	if !hasWorkflow {
		return t, nil
	}
	ready, err := gitlabJobsUnsetTraceKey(func(path string) ([]byte, bool, error) {
		return readExistingFile(ctx, fc.Client, rr.Owner, rr.Repo, path)
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if !ready {
		t.awaitingUnset, t.withheld = true, withheld
		t.reason, t.exposedIDs = "", nil
	}
	return t, nil
}

// pinnedJobs is whether one pinned release's fullsend GitLab jobs unset
// FULLSEND_TRACE_SIGNING_KEY first, or the error that reading them met.
type pinnedJobs struct {
	unset bool
	err   error
}

// gitlabPinnedJobs reads, once per pinned release, the fullsend GitLab jobs
// that install delivers for a fullsend_ref that pins a release other than
// the running one.
type gitlabPinnedJobs struct {
	clients                  ForgeClientFactory
	manifest                 *Manifest
	upstreamRef, upstreamTag string
	byPin                    map[string]pinnedJobs
}

// unset returns the release that rr's fullsend_ref pins, as convergence
// resolves it, and whether that release's poll, dispatcher and agent jobs
// each unset FULLSEND_TRACE_SIGNING_KEY first (gitlabJobsUnsetTraceKey). pin
// is "" when install delivers the running release's jobs, which do. A
// template missing at the pin, or a missing job, does not unset it; any
// other read error is returned.
func (g *gitlabPinnedJobs) unset(ctx context.Context, rr ResolvedRepo) (pin string, unset bool, err error) {
	fullsendRef := g.manifest.ResolveConfigForEntry(rr.Owner, rr.Repo, rr.Forge, rr.Entry).FullsendRef
	// Convergence fetches the templates at the fullsend_ref itself.
	// Resolving a tag to its commit changes only the version marker
	// rendered into them, so no ref is resolved here.
	rref := resolveTargetRef(ctx, fullsendRef, g.upstreamRef, g.upstreamTag, nil)
	if rref.manifestRef == "" {
		return "", true, nil
	}
	r, ok := g.byPin[rref.manifestRef]
	if !ok {
		r.unset, r.err = g.read(ctx, rref)
		g.byPin[rref.manifestRef] = r
	}
	return rref.manifestRef, r.unset, r.err
}

// read fetches the GitLab templates of rref's pin, as convergence does,
// and checks their jobs.
func (g *gitlabPinnedJobs) read(ctx context.Context, rref resolvedRef) (bool, error) {
	upstream, err := g.clients.ConfigFor(ForgeGitHub)
	if err != nil {
		return false, fmt.Errorf("matching pinned GitLab templates require an upstream GitHub client: %w", err)
	}
	files, err := FetchRemoteScaffold(ctx, upstream.Client, rref.manifestRef, rref.ref, ForgeGitLab,
		gitlabAgentRunnerTags(g.manifest), gitlabControlRunnerTags(g.manifest), false)
	if forge.IsNotFound(err) {
		// A template the pin does not serve unsets nothing; convergence
		// fails the repository on the same read.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return gitlabJobsUnsetTraceKey(func(path string) ([]byte, bool, error) {
		for _, f := range files {
			if f.Path == path {
				return f.Content, true, nil
			}
		}
		return nil, false, nil
	})
}

// readTraceSecret reports whether FULLSEND_TRACE_SIGNING_KEY exists and, on
// GitLab, why the stored seed is exposed ("" when it is usable or absent).
func readTraceSecret(ctx context.Context, client forge.Client, rr ResolvedRepo) (bool, string, error) {
	if rr.Forge != ForgeGitLab {
		exists, err := client.RepoSecretExists(ctx, rr.Owner, rr.Repo, forge.SecretTraceSigningKey)
		return exists, "", err
	}
	prot, err := client.GetRepoSecretProtection(ctx, rr.Owner, rr.Repo, forge.SecretTraceSigningKey)
	if err != nil {
		return false, "", err
	}
	return prot.Exists, openAIKeyDefect(prot), nil
}

// gitlabJobsUnsetTraceKey reports whether the poll, dispatcher and agent
// templates that read returns each define their fullsend job with "unset
// FULLSEND_TRACE_SIGNING_KEY" as its first before_script entry, so that the
// job's before_script and script commands after it do not inherit the seed
// (ADR 0154). GitLab runs an after_script or hooks entry, whether the job's
// own or inherited from the project's default: or a top-level after_script,
// in another shell with the variable set; like the project's own jobs, those
// are not checked. read returns a template's content and whether it exists.
// A missing or undecodable template, or a missing job, is not ready; a read
// error is returned, naming the file.
func gitlabJobsUnsetTraceKey(read func(path string) ([]byte, bool, error)) (bool, error) {
	for _, job := range []struct{ path, name string }{
		{fullsendPollTemplatePath, fullsendPollJobName},
		{fullsendDispatcherTemplatePath, gitlabDispatcherJobName},
		{fullsendAgentTemplatePath, fullsendAgentJobName},
	} {
		content, found, err := read(job.path)
		if err != nil {
			return false, fmt.Errorf("reading %s: %w", job.path, err)
		}
		if !found {
			return false, nil
		}
		node, ok := gitlabJobNode(content, job.name)
		if !ok {
			return false, nil
		}
		lines, ok := gitlabJobScriptLines(node, "before_script")
		if !ok || len(lines) == 0 || lines[0] != "unset "+forge.SecretTraceSigningKey {
			return false, nil
		}
	}
	return true, nil
}

// staleReasons returns why the committed trace_context shows that t's
// repos.yaml entry is older than the install, naming key IDs only, and
// whether a removal whose change has not merged may explain the committed
// keys missing from the entry. A key named in remove may be missing from the
// entry. A key that a removal dropped from the entry stays committed until
// the removal's change merges, and install cannot tell that from a key
// added to the committed config by hand, so the reason for missing keys
// names a pending removal first. A removal never drops the active key, so
// it does not when the committed active key is missing.
func (t *traceRepo) staleReasons(remove map[[8]byte]bool) (whys []string, pendingRemoval bool) {
	in := t.installed
	if in == nil {
		return nil, false
	}
	if t.manifest == nil {
		return []string{"the committed " + preset.OverlayPath + " has trace_context and the repos.yaml entry has none" +
			" (install enrolls the repository again as a new installation only once no fullsend workflow, " +
			forge.SecretTraceSigningKey + " or " + forge.VarTraceSigningKeyID + " remains, as after a finished repos uninstall)"}, false
	}
	if in.installation != t.manifest.installation {
		whys = append(whys, "the committed trace_context.installation_id differs from the repos.yaml entry's")
	}
	var missing []string
	for id, pub := range in.keys {
		if !remove[id] && !t.manifest.has(id, pub) {
			missing = append(missing, tracecontext.FormatKeyID(id))
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		pending := ""
		if pendingRemoval = !slices.Contains(missing, tracecontext.FormatKeyID(in.active)); pendingRemoval {
			pending = fmt.Sprintf("if a repos install %[1]s run removed the key and that change has not merged yet, "+
				"merge that change, or name the key with %[1]s again on each run until it merges; ", removeTraceKeyFlag)
		}
		whys = append(whys, fmt.Sprintf("the committed config trusts %s, which the repos.yaml entry does not list (%sto revoke a retained key on purpose, name it with %s; "+
			"if you did not add the key, the committed %s may have been edited: revert that change, and do not add the key to repos.yaml)",
			keyIDs(missing), pending, removeTraceKeyFlag, preset.OverlayPath))
	}
	return whys, pendingRemoval
}

// dropRetained sets t.next to a copy of the entry's block without the
// retained keys named in remove, recording their IDs in t.dropped.
func (t *traceRepo) dropRetained(remove map[[8]byte]bool) {
	t.next = t.block
	if t.next == nil || len(remove) == 0 {
		return
	}
	var kept []string
	for _, s := range t.next.VerificationKeys.Retained {
		pub, err := tracecontext.ParseVerificationKey(s)
		if id := tracecontext.KeyID(pub); err == nil && remove[id] {
			t.dropped = append(t.dropped, tracecontext.FormatKeyID(id))
			continue
		}
		kept = append(kept, s)
	}
	t.next.VerificationKeys.Retained = kept
}

// provision generates t's new active key into t.next, keeping the
// installation ID and retaining the previous active key, or starting a
// block with a new installation ID.
func (t *traceRepo) provision(rand io.Reader) (tracecontext.SigningKey, error) {
	key, err := tracecontext.NewSigningKey(rand)
	if err != nil {
		return tracecontext.SigningKey{}, err
	}
	if t.next == nil {
		id, err := tracecontext.NewInstallationID(rand)
		if err != nil {
			return tracecontext.SigningKey{}, err
		}
		t.next = &config.TraceContextConfig{
			InstallationID:   tracecontext.FormatInstallationID(id),
			SigningKey:       &config.TraceSigningKeyRef{Secret: config.TraceSigningKeySecret},
			VerificationKeys: &config.TraceVerificationKeys{},
		}
	} else {
		t.next.VerificationKeys.Retained = append(t.next.VerificationKeys.Retained, t.next.VerificationKeys.Active)
	}
	t.next.VerificationKeys.Active = tracecontext.FormatVerificationKey(key.Public())
	return key, nil
}

// installedTraceContext returns the trace_context of the committed
// .fullsend/config.yaml, or nil when the file or the block is absent.
func installedTraceContext(ctx context.Context, client forge.Client, owner, repo, forgeName string) (*config.TraceContextConfig, error) {
	data, found, err := readExistingFile(ctx, client, owner, repo, preset.OverlayPath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", preset.OverlayPath, err)
	}
	if !found {
		return nil, nil
	}
	if data, err = overlayForEvaluation(data, forgeName); err != nil {
		return nil, err
	}
	r, err := config.ParsePerRepoConfig(data)
	if err != nil {
		return nil, fmt.Errorf("existing %s cannot be parsed: %w", preset.OverlayPath, err)
	}
	return r.TraceContext(), nil
}

// traceInstallationUser returns the name of a manifest entry other than
// name whose trace_context uses installation, or "" when there is none.
// Unselected entries count: an installation ID identifies one install.
func traceInstallationUser(m *Manifest, name string, installation [16]byte) string {
	for _, e := range m.AllRepos() {
		w := e.Config.Writer()
		if w == nil || strings.EqualFold(e.Name, name) {
			continue
		}
		if tc := w.TraceContext(); tc != nil {
			if id, err := tc.Installation(); err == nil && id == installation {
				return e.Name
			}
		}
	}
	return ""
}

// checkTraceRemovals refuses, before any edit, each removal that names an
// active key of a selected repository, the key that a selected repository's
// FULLSEND_TRACE_SIGNING_KEY_ID names, or a key that no selected repository
// retains in its repos.yaml entry or committed config. The ledger names a
// retained key only after an interrupted run, and removing that key while
// the run's key write waits or fails would leave the ledger naming a key the
// entry no longer lists, which every later run refuses as stale. The
// committed block of an ended installation counts for neither: its keys are
// no longer the repository's. Errors name the flag and the value's
// position, never the value.
func checkTraceRemovals(repos []*traceRepo, remove [][8]byte) []error {
	var errs []error
	for i, id := range remove {
		var activeIn, ledgerIn []string
		retained := false
		for _, t := range repos {
			sets := []*traceKeySet{t.manifest, t.installed}
			if t.ended {
				sets = sets[:1]
			}
			for _, s := range sets {
				switch {
				case s == nil:
				case s.active == id:
					activeIn = append(activeIn, t.name())
				case s.keys[id] != nil:
					retained = true
				}
			}
			if t.ledger != nil && *t.ledger == id {
				ledgerIn = append(ledgerIn, t.name())
			}
		}
		switch {
		case len(activeIn) > 0:
			errs = append(errs, fmt.Errorf("%s value %d is the active verification key of %s: a key can be removed only once it is retained, so rotate first and remove the retired key in a later run, once the rotation is on the default branch",
				removeTraceKeyFlag, i+1, strings.Join(slices.Compact(activeIn), ", ")))
		case len(ledgerIn) > 0:
			errs = append(errs, fmt.Errorf("%[1]s value %[2]d is the key that %[3]s names for %[4]s, as after an interrupted run: run repos install without that value first, and remove the key once a run has written a new key ID to %[3]s",
				removeTraceKeyFlag, i+1, forge.VarTraceSigningKeyID, strings.Join(ledgerIn, ", ")))
		case !retained:
			errs = append(errs, fmt.Errorf("%s value %d names no verification key that a selected repository retains", removeTraceKeyFlag, i+1))
		}
	}
	return errs
}

// convergeTraceSigningKey writes the planned key of d's repository from
// cfg.TraceKeyPlans: FULLSEND_TRACE_SIGNING_KEY, then, only when that write
// succeeded, the FULLSEND_TRACE_SIGNING_KEY_ID ledger. It is its own step,
// independent of ReuseSecrets and of the variable and secret phases, so the
// ledger can never get ahead of the seed. On GitLab both writes hold the
// project lock that serializes installers (LockGitLabProject). Before
// writing, it reads the ledger again and writes no key when the value
// differs from the one the preflight read (plan.Ledger), as when another
// install or uninstall of the repository ran meanwhile. A repository without
// a plan gets no action. A dry run reports "would write" actions only, and
// errors are redacted with redactSecretValues. Callers stop the repository
// before its scaffold or config commit when an action is "error".
func convergeTraceSigningKey(ctx context.Context, d convergeDiscovery, cfg ConvergeConfig, progress ProgressFunc) (actions []ComponentAction) {
	resolved := d.resolved
	repoFullName := resolved.Owner + "/" + resolved.Repo
	plan := cfg.TraceKeyPlans[repoFullName]
	if plan == nil {
		return nil
	}
	keyID := tracecontext.FormatKeyID(plan.Key.KeyID())
	secretAction := ComponentAction{Component: "secret:" + forge.SecretTraceSigningKey, Action: "update"}
	ledgerAction := ComponentAction{Component: "var:" + forge.VarTraceSigningKeyID, Action: "update"}
	if cfg.DryRun {
		secretAction.Detail = fmt.Sprintf("would set %s (key ID %s)", forge.SecretTraceSigningKey, keyID)
		ledgerAction.Detail = fmt.Sprintf("would set %s = %s", forge.VarTraceSigningKeyID, keyID)
		progress(repoFullName, "dry-run", fmt.Sprintf("Would set secret %s", forge.SecretTraceSigningKey))
		progress(repoFullName, "dry-run", fmt.Sprintf("Would set variable %s", forge.VarTraceSigningKeyID))
		return []ComponentAction{secretAction, ledgerAction}
	}

	client := resolved.ForgeConfig.Client
	seed := plan.Key.EncodeSeed()
	values := map[string]string{forge.SecretTraceSigningKey: seed}
	notSet := func(why string) []ComponentAction {
		secretAction.Action = "error"
		secretAction.Detail = fmt.Sprintf("%s not set: %s", forge.SecretTraceSigningKey, redactSecretValues(why, values))
		return []ComponentAction{secretAction}
	}
	if plan.Forge == ForgeGitLab {
		release, err := LockGitLabProject(ctx, client, resolved.Owner, resolved.Repo, false)
		if err != nil {
			return notSet(err.Error())
		}
		defer func() {
			var relErr error
			release(&relErr)
			if relErr != nil {
				actions = append(actions, ComponentAction{Component: "var:" + GitLabProjectLeaseVar, Action: "error", Detail: redactSecretValues(relErr.Error(), values)})
			}
		}()
	}
	ledger, exists, err := client.GetRepoVariable(ctx, resolved.Owner, resolved.Repo, forge.VarTraceSigningKeyID)
	if err != nil {
		return notSet(fmt.Sprintf("reading %s: %s", forge.VarTraceSigningKeyID, err))
	}
	if !exists {
		ledger = ""
	}
	if ledger != plan.Ledger {
		return notSet(fmt.Sprintf("%s changed after this run read it, as when another install or uninstall of %s runs at the same time; "+
			"once that run has finished, run repos install again (after another install, with the repos.yaml that install wrote)", forge.VarTraceSigningKeyID, repoFullName))
	}
	if err := client.CreateRepoSecret(ctx, resolved.Owner, resolved.Repo, forge.SecretTraceSigningKey, seed); err != nil {
		secretAction.Action = "error"
		secretAction.Detail = fmt.Sprintf("failed to set %s: %s", forge.SecretTraceSigningKey, redactSecretValues(err.Error(), values))
		return []ComponentAction{secretAction}
	}
	secretAction.Detail = fmt.Sprintf("set %s (key ID %s)", forge.SecretTraceSigningKey, keyID)
	progress(repoFullName, "sync", fmt.Sprintf("Set secret %s", forge.SecretTraceSigningKey))
	if err := client.CreateOrUpdateRepoVariable(ctx, resolved.Owner, resolved.Repo, forge.VarTraceSigningKeyID, keyID); err != nil {
		ledgerAction.Action = "error"
		ledgerAction.Detail = fmt.Sprintf("failed to set %s: %s", forge.VarTraceSigningKeyID, redactSecretValues(err.Error(), values))
		return []ComponentAction{secretAction, ledgerAction}
	}
	ledgerAction.Detail = fmt.Sprintf("set %s = %s", forge.VarTraceSigningKeyID, keyID)
	progress(repoFullName, "sync", fmt.Sprintf("Set variable %s", forge.VarTraceSigningKeyID))
	return []ComponentAction{secretAction, ledgerAction}
}

// actionsError joins the details of the "error" actions into one error,
// or returns nil when no action failed.
func actionsError(actions []ComponentAction) error {
	var details []string
	for _, a := range actions {
		if a.Action == "error" {
			details = append(details, a.Detail)
		}
	}
	if len(details) == 0 {
		return nil
	}
	return fmt.Errorf("convergence errors: %s", strings.Join(details, "; "))
}
