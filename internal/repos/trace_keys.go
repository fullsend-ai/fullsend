package repos

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/preset"
	"github.com/fullsend-ai/fullsend/internal/telemetry/tracecontext"
)

// TraceKeysConfig configures EnsureTraceSigningKeys. Manifest,
// ManifestPath, RepoFilter and ExcludeRepos mean what they mean in
// ManagedConfigWritebackConfig.
type TraceKeysConfig struct {
	Manifest     *Manifest
	ManifestPath string
	// Deferred reports that the caller persists the edited manifest, so
	// progress says Persisted rather than Would persist.
	Deferred     bool
	RepoFilter   []string
	ExcludeRepos []string
	// Rotate is --rotate-trace-signing-key: generate a new active key even
	// when the installed one is healthy. On an existing install without a
	// trace_context block the first rotation provisions the keys.
	Rotate bool
}

// traceKeyReason is why EnsureTraceSigningKeys plans a key.
type traceKeyReason string

const (
	reasonFreshInstall traceKeyReason = "fresh install"
	reasonRecovery     traceKeyReason = "recovery"
	reasonRotation     traceKeyReason = "rotation"
)

// TraceKeyPlan is one repository's pending trace-key write. Key holds the
// new seed. KeyIDVar and KeyIDVarExists are what the preflight read from
// FULLSEND_TRACE_SIGNING_KEY_ID: convergence writes nothing unless a second
// read returns the same. KeyIDVar is a key ID, never key material.
type TraceKeyPlan struct {
	Key tracecontext.SigningKey
	// Reason is why the key is planned: a fresh install, a recovery or a
	// rotation.
	Reason         traceKeyReason
	KeyIDVar       string
	KeyIDVarExists bool
}

// Trace-key preflight refusals. They name the repository and key IDs, never
// key material or the value of FULLSEND_TRACE_SIGNING_KEY_ID.
var (
	ErrTraceManifestStale    = errors.New("repos.yaml does not match the repository's trace keys")
	ErrTraceManifestReadOnly = errors.New("repos.yaml is read-only")
	ErrTraceKeyIDInvalid     = errors.New(forge.VarTraceSigningKeyID + " is not a key ID")
)

// EnsureTraceSigningKeys plans the trace signing keys of the selected
// repositories (ADR 0154) before the manifest is persisted, so convergence
// writes them before its scaffold or config commit
// (ConvergeConfig.TraceKeyPlans). GitLab repositories get no key and are not
// read; progress says so. For each other selected repository it reads the
// committed .fullsend/config.yaml trace_context and
// FULLSEND_TRACE_SIGNING_KEY_ID and, when they decide the outcome, the
// fullsend workflow marker and whether FULLSEND_TRACE_SIGNING_KEY exists.
// Then it:
//   - refuses with ErrTraceKeyIDInvalid, before any edit, a variable that is
//     not 16 lowercase hex characters;
//   - refuses with ErrTraceManifestStale, before any edit, an entry whose
//     installation_id differs from the committed one, that lacks a committed
//     key, or that has no trace_context while the committed config has one;
//     a variable set beside an entry without trace_context, or naming a key
//     the entry does not list; and an installation_id another entry uses.
//     The committed block of an ended installation is not compared with the
//     entry: a keyless entry's block once no fullsend workflow, seed or
//     variable remains (a full uninstall, which keeps the committed config
//     on GitHub), and another installation's block while no fullsend
//     workflow is on the default branch;
//   - plans "recovery" when the secret is missing or the variable is absent
//     or names a retained key, "rotation" for Rotate, and "fresh install"
//     for a keyless entry without a fullsend workflow; a healthy install,
//     and an existing install without trace_context, get no plan;
//   - refuses with ErrTraceManifestReadOnly, before any edit, a plan that a
//     read-only manifest cannot record, except a fresh install without
//     Rotate, which goes ahead without keys and says so;
//   - otherwise generates a key (and, for an entry without keys, an
//     installation ID) from crypto/rand, retains the previous active key,
//     records the block in the manifest entry (carving an explicit entry out
//     of a glob like EnsureManagedConfigDefaults) and returns a plan keyed
//     "owner/repo".
//
// EnsureTraceSigningKeys never writes repos.yaml; the caller persists the
// edited manifest (repos install does so through its compare-before-write
// guard). Deferred reports that the caller persists it, so progress says
// Persisted rather than Would persist. Repositories that fail
// RequireInferenceAuth are skipped. Progress names installation and key IDs,
// and the glob a new entry was copied from, never the seed.
func EnsureTraceSigningKeys(ctx context.Context, cfg TraceKeysConfig, clients ForgeClientFactory, progress ProgressFunc) (map[string]*TraceKeyPlan, error) {
	m := cfg.Manifest
	selected, err := selectManifestRepos(ctx, m, clients, cfg.RepoFilter)
	if err != nil {
		return nil, err
	}
	readOnly := ManifestReadOnly(m, cfg.ManifestPath)
	var repos []*traceRepo
	var gitlab []string
	var errs []error
	for _, rr := range withoutRepos(selected, cfg.ExcludeRepos) {
		if rr.Forge == ForgeGitLab {
			gitlab = append(gitlab, rr.Owner+"/"+rr.Repo)
			continue
		}
		// Convergence fails a repository without inference
		// authentication before its key writes, so it gains no key.
		if m.ResolveConfigForEntry(rr.Owner, rr.Repo, rr.Forge, rr.Entry).RequireInferenceAuth() != nil {
			continue
		}
		t, err := readTraceRepo(ctx, m, clients, rr, cfg.Rotate)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		repos = append(repos, t)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	var edited []string
	for _, t := range repos {
		// A fresh install goes ahead without keys when the manifest cannot
		// record them. An explicit rotation still needs the edit.
		if readOnly && !cfg.Rotate && t.reason == reasonFreshInstall {
			t.reason, t.readOnlySkip = "", true
		}
		if t.reason != "" {
			edited = append(edited, t.name())
		}
	}
	if readOnly && len(edited) > 0 {
		return nil, fmt.Errorf("%w: recording the trace keys of %s needs a repos.yaml edit; repos.yaml and the trace keys were not changed: re-run with a writable local manifest",
			ErrTraceManifestReadOnly, strings.Join(edited, ", "))
	}
	for _, name := range gitlab {
		progress(name, "manifest", "Trace signing keys not provisioned: repos install does not provision them on GitLab yet")
	}
	for _, t := range repos {
		if t.readOnlySkip {
			// The notice names the repository in the run it asks for, so
			// that run does not rotate the other selected repositories.
			progress(t.name(), "manifest", "Trace signing keys not provisioned: repos.yaml is read-only; "+
				"a later repos install "+t.name()+" --rotate-trace-signing-key with a writable manifest provisions them")
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

	plans := make(map[string]*TraceKeyPlan, len(edited))
	for _, t := range repos {
		if t.reason == "" {
			continue
		}
		key, err := t.provision()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", t.name(), err)
		}
		p := &TraceKeyPlan{Key: key, Reason: t.reason}
		if t.recordedKeyID != nil {
			p.KeyIDVar, p.KeyIDVarExists = tracecontext.FormatKeyID(*t.recordedKeyID), true
		}
		plans[t.name()] = p
		entry := findExplicitEntry(m, t.name())
		if entry == nil {
			carved, glob, err := carveOutForForge(m, t.name(), t.rr.Forge)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", t.name(), err)
			}
			if carved == nil {
				return nil, fmt.Errorf("no manifest entry or glob covers %q", t.name())
			}
			entry, t.carvedFrom = carved, glob
		}
		entry.Config = entry.Config.WithTraceContext(t.next)
	}
	if err := validateManifestManaged(m); err != nil {
		return nil, fmt.Errorf("validating manifest after recording trace keys: %w", err)
	}

	verb := "Would persist"
	if cfg.Deferred {
		verb = "Persisted"
	}
	for _, t := range repos {
		if p := plans[t.name()]; p != nil {
			where := manifestEntryWhere(t.name(), t.carvedFrom)
			progress(t.name(), "manifest", fmt.Sprintf("%s config.trace_context in %s: installation ID %s, new active key ID %s (%s)",
				verb, where, t.next.InstallationID, tracecontext.FormatKeyID(p.Key.KeyID()), p.Reason))
		}
	}
	return plans, nil
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

// keyIDs names hex key IDs for a message: "key ID a" or "key IDs a, b".
func keyIDs(ids []string) string {
	if len(ids) == 1 {
		return "key ID " + ids[0]
	}
	return "key IDs " + strings.Join(ids, ", ")
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
	// reason is why a key is planned; "" plans none.
	reason traceKeyReason
	// readOnlySkip reports a fresh install that goes ahead without keys
	// because the manifest is read-only.
	readOnlySkip bool
	// ended reports that installed belongs to an installation that ended,
	// as with a full uninstall. The installation in the repos.yaml entry,
	// or the one a fresh install adds, replaces it; when the entry gets
	// none, install's commit removes it.
	ended bool
	// next is the entry's new block.
	next *config.TraceContextConfig
	// carvedFrom names the glob entry that the repository's new explicit
	// entry was copied from, or is "" when it already had its own entry.
	carvedFrom string
	// recordedKeyID is the key ID FULLSEND_TRACE_SIGNING_KEY_ID names, or
	// nil when the variable is absent.
	recordedKeyID *[8]byte
}

func (t *traceRepo) name() string { return t.rr.Owner + "/" + t.rr.Repo }

// readTraceRepo reads rr's committed trace_context and
// FULLSEND_TRACE_SIGNING_KEY_ID and, when they decide the outcome, its
// workflow marker and secret; refuses a malformed variable or an entry
// they contradict; and decides whether a key is planned.
func readTraceRepo(ctx context.Context, m *Manifest, clients ForgeClientFactory, rr ResolvedRepo, rotate bool) (*traceRepo, error) {
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
	client := fc.Client
	committed, err := installedTraceContext(ctx, client, rr.Owner, rr.Repo, rr.Forge)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if t.installed, err = parseTraceKeySet(committed); err != nil {
		return nil, fmt.Errorf("%s: committed %s: %w", name, preset.OverlayPath, err)
	}
	value, recorded, err := client.GetRepoVariable(ctx, rr.Owner, rr.Repo, forge.VarTraceSigningKeyID)
	if err != nil {
		return nil, fmt.Errorf("%s: reading %s: %w", name, forge.VarTraceSigningKeyID, err)
	}
	var keyID [8]byte
	if recorded {
		// The value is never echoed: only install writes the variable, so
		// anything else in it is unexpected input.
		if keyID, err = tracecontext.ParseKeyID(value); err != nil {
			return nil, fmt.Errorf("%w for %s: its value is not 16 lowercase hex characters; repos.yaml and the trace keys were not changed: "+
				"delete the repository variable %s, then run repos install again", ErrTraceKeyIDInvalid, name, forge.VarTraceSigningKeyID)
		}
	}

	// The workflow marker tells an existing install from a fresh one, and an
	// ended installation's committed block from one the entry contradicts.
	hasWorkflow := false
	otherInstallation := t.manifest != nil && t.installed != nil && t.installed.installation != t.manifest.installation
	if t.manifest == nil || otherInstallation {
		if _, _, hasWorkflow, err = readWorkflowMarker(ctx, client, rr.Owner, rr.Repo, fc); err != nil {
			return nil, fmt.Errorf("%s: checking workflow file: %w", name, err)
		}
	}
	// The secret decides an entry with keys, and whether a keyless entry's
	// committed block belongs to a fully uninstalled repository.
	secretExists := false
	if t.manifest != nil || (t.installed != nil && !hasWorkflow && !recorded) {
		if secretExists, err = client.RepoSecretExists(ctx, rr.Owner, rr.Repo, forge.SecretTraceSigningKey); err != nil {
			return nil, fmt.Errorf("%s: checking secret %s: %w", name, forge.SecretTraceSigningKey, err)
		}
	}
	// A full uninstall leaves no workflow, seed or variable, but on GitHub it
	// keeps the committed block, so the repository is a fresh install of a
	// new installation. That block, like another installation's block while
	// no fullsend workflow is on the default branch, stays committed until
	// the commit of the install that enrolled the repository again lands, by
	// default through a pull request.
	t.ended = (t.manifest == nil && t.installed != nil && !hasWorkflow && !recorded && !secretExists) ||
		(otherInstallation && !hasWorkflow)

	var whys []string
	if !t.ended {
		whys = t.staleReasons()
	}
	// Only a variable naming one of the entry's keys can come from a run
	// with this repos.yaml; any other was written for another key set.
	switch {
	case recorded && t.manifest == nil:
		whys = append(whys, forge.VarTraceSigningKeyID+" is set and the repos.yaml entry has no trace_context")
	case recorded && t.manifest.keys[keyID] == nil:
		whys = append(whys, forge.VarTraceSigningKeyID+" names a key the repos.yaml entry does not list")
	}
	if t.manifest != nil {
		if other := traceInstallationUser(m, name, t.manifest.installation); other != "" {
			whys = append(whys, fmt.Sprintf("its trace_context.installation_id is also used by the repos.yaml entry %s; every repository needs its own", other))
		}
	}
	if len(whys) > 0 {
		return nil, fmt.Errorf("%w for %s: %s; repos.yaml and the trace keys were not changed: "+
			"run install with the repos.yaml the last install wrote, or restore the entry's trace_context from it",
			ErrTraceManifestStale, name, strings.Join(whys, "; "))
	}
	if recorded {
		t.recordedKeyID = &keyID
	}

	if t.manifest == nil {
		// An entry without keys provisions them on a fresh install, and on
		// an existing install only when the operator rotates.
		switch {
		case !hasWorkflow:
			t.reason = reasonFreshInstall
		case rotate:
			t.reason = reasonRotation
		}
		return t, nil
	}
	// A variable that is absent (an interrupted write, a deleted malformed
	// value, a torn-down install) or that names a retained key means the
	// secret may not hold the active key.
	switch {
	case !secretExists, !recorded, keyID != t.manifest.active:
		t.reason = reasonRecovery
	case rotate:
		t.reason = reasonRotation
	}
	return t, nil
}

// staleReasons returns why the committed trace_context shows that t's
// repos.yaml entry is not the one the last install wrote, naming key IDs
// only.
func (t *traceRepo) staleReasons() []string {
	in := t.installed
	if in == nil {
		return nil
	}
	if t.manifest == nil {
		return []string{"the committed " + preset.OverlayPath + " has trace_context and the repos.yaml entry has none" +
			" (install enrolls the repository again as a new installation only once no fullsend workflow, " +
			forge.SecretTraceSigningKey + " or " + forge.VarTraceSigningKeyID + " remains, as after a finished repos uninstall)"}
	}
	var whys []string
	if in.installation != t.manifest.installation {
		whys = append(whys, "the committed trace_context.installation_id differs from the repos.yaml entry's")
	}
	var missing []string
	for id, pub := range in.keys {
		if !t.manifest.has(id, pub) {
			missing = append(missing, tracecontext.FormatKeyID(id))
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		whys = append(whys, fmt.Sprintf("the committed %s trusts %s, which the repos.yaml entry does not list "+
			"(if you did not add the key, the committed file may have been edited: revert that change, and do not add the key to repos.yaml)",
			preset.OverlayPath, keyIDs(missing)))
	}
	return whys
}

// provision generates t's new active key into t.next, keeping the
// installation ID and retaining the previous active key, or starting a
// block with a new installation ID.
func (t *traceRepo) provision() (tracecontext.SigningKey, error) {
	key, err := tracecontext.NewSigningKey(nil)
	if err != nil {
		return tracecontext.SigningKey{}, err
	}
	t.next = t.block
	if t.next == nil {
		id, err := tracecontext.NewInstallationID(nil)
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

// convergeTraceSigningKey writes the planned key of d's repository from
// cfg.TraceKeyPlans before the scaffold or config commit that delivers the
// config naming it (ADR 0154). It reads FULLSEND_TRACE_SIGNING_KEY_ID again
// and writes nothing unless the variable is still absent, or still present
// with the same value, as the preflight read it; otherwise another install
// or uninstall of the repository ran meanwhile. It then deletes the
// variable, writes FULLSEND_TRACE_SIGNING_KEY and sets the variable to the
// new key ID, each step only after the one before succeeded, so the variable
// never names a key whose seed the secret no longer holds, apart from
// overlapping runs. Nothing locks the read and
// the writes, so a run that writes after the read goes unseen. A
// repository without a plan gets no action. A dry run reports "would set"
// actions only, and errors are redacted with redactSecretValues. Callers
// stop the repository before its scaffold or config commit when an action
// is "error".
func convergeTraceSigningKey(ctx context.Context, d convergeDiscovery, cfg ConvergeConfig, progress ProgressFunc) []ComponentAction {
	resolved := d.resolved
	repoFullName := resolved.Owner + "/" + resolved.Repo
	plan := cfg.TraceKeyPlans[repoFullName]
	if plan == nil {
		return nil
	}
	keyID := tracecontext.FormatKeyID(plan.Key.KeyID())
	secretAction := ComponentAction{Component: "secret:" + forge.SecretTraceSigningKey, Action: "update"}
	keyIDAction := ComponentAction{Component: "var:" + forge.VarTraceSigningKeyID, Action: "update"}
	if cfg.DryRun {
		secretAction.Detail = fmt.Sprintf("would set %s (key ID %s)", forge.SecretTraceSigningKey, keyID)
		keyIDAction.Detail = fmt.Sprintf("would set %s = %s", forge.VarTraceSigningKeyID, keyID)
		progress(repoFullName, "dry-run", "Would set secret "+forge.SecretTraceSigningKey)
		progress(repoFullName, "dry-run", "Would set variable "+forge.VarTraceSigningKeyID)
		return []ComponentAction{secretAction, keyIDAction}
	}

	client := resolved.ForgeConfig.Client
	owner, repo := resolved.Owner, resolved.Repo
	seed := plan.Key.EncodeSeed()
	values := map[string]string{forge.SecretTraceSigningKey: seed}
	notSet := func(why string) []ComponentAction {
		secretAction.Action = "error"
		secretAction.Detail = fmt.Sprintf("%s not set: %s", forge.SecretTraceSigningKey, redactSecretValues(why, values))
		return []ComponentAction{secretAction}
	}
	value, exists, err := client.GetRepoVariable(ctx, owner, repo, forge.VarTraceSigningKeyID)
	if err != nil {
		return notSet(fmt.Sprintf("reading %s: %s", forge.VarTraceSigningKeyID, err))
	}
	if exists != plan.KeyIDVarExists || value != plan.KeyIDVar {
		return notSet(fmt.Sprintf("%s changed after this run read it, as when another install or uninstall of %s runs at the same time; "+
			"once that run has finished, run repos install again (after another install, with the repos.yaml that install wrote)", forge.VarTraceSigningKeyID, repoFullName))
	}
	if err := client.DeleteRepoVariable(ctx, owner, repo, forge.VarTraceSigningKeyID); err != nil {
		return notSet(fmt.Sprintf("deleting %s: %s", forge.VarTraceSigningKeyID, err))
	}
	if err := client.CreateRepoSecret(ctx, owner, repo, forge.SecretTraceSigningKey, seed); err != nil {
		// A failed write may still have landed, as after a timeout. With the
		// variable deleted, a retry from an older repos.yaml would then
		// replace that key without retaining it.
		secretAction.Action = "error"
		secretAction.Detail = fmt.Sprintf("failed to set %s: %s; %s may now hold a key that only the repos.yaml this run wrote lists: "+
			"keep and commit that repos.yaml, and run repos install again with it",
			forge.SecretTraceSigningKey, redactSecretValues(err.Error(), values), forge.SecretTraceSigningKey)
		return []ComponentAction{secretAction}
	}
	secretAction.Detail = fmt.Sprintf("set %s (key ID %s)", forge.SecretTraceSigningKey, keyID)
	progress(repoFullName, "sync", "Set secret "+forge.SecretTraceSigningKey)
	if err := client.CreateOrUpdateRepoVariable(ctx, owner, repo, forge.VarTraceSigningKeyID, keyID); err != nil {
		// With the variable absent, nothing on the forge tells an older
		// repos.yaml from this run's, so a retry from an older copy would
		// replace this key without retaining it.
		keyIDAction.Action = "error"
		keyIDAction.Detail = fmt.Sprintf("failed to set %s: %s; %s now holds a key that only the repos.yaml this run wrote lists: "+
			"keep and commit that repos.yaml, and run repos install again with it",
			forge.VarTraceSigningKeyID, redactSecretValues(err.Error(), values), forge.SecretTraceSigningKey)
		return []ComponentAction{secretAction, keyIDAction}
	}
	keyIDAction.Detail = fmt.Sprintf("set %s = %s", forge.VarTraceSigningKeyID, keyID)
	progress(repoFullName, "sync", "Set variable "+forge.VarTraceSigningKeyID)
	return []ComponentAction{secretAction, keyIDAction}
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
