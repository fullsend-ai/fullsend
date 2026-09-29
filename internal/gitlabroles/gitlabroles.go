// Package gitlabroles defines the GitLab role-credential contract and
// the remaining role-identity gate (#7497, #7559).
//
// Built-in Poller, Analyst, and Coder identities and administrator-
// registered custom roles are the same kind of Registry entry. Resolve
// selects a credential from that registry; it does not branch on a
// three-role enum.
//
// This package is the internal contract for provisioning (#7498),
// routing (#7499), and rotation/recovery (#7500). Select / SelectAgent
// / Require are the dispatch-time entry points wired into fullsend
// poll, fullsend run, and post-review. DiagnoseLifecycle reports
// expiry, revocation, and overlapping tokens. CheckBuiltinReadiness
// is the #7501 verification for Poller, Analyst, and Coder; it does
// not enable enforced mode or retire the shared token.
//
// Runtime authentication is role-credential only: Resolve selects the
// registered role credential and never FULLSEND_FORGE_TOKEN, including
// leftover ModeDisabled / unset gates and explicit ModeRollback.
// ModeMigrating remains an internal install intermediate. Ordinary
// repos install still converges leftover shared-token installs; the
// gate no longer changes runtime credential selection.
//
// Canonical documentation: docs/contributing/gitlab-role-credentials.md.
package gitlabroles

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// Mode is the role-identity gate stored in FULLSEND_GITLAB_ROLE_MIGRATION.
// Absent or empty is ModeDisabled (leftover shared-token install state).
// Runtime credential selection does not switch on Mode.
type Mode string

const (
	// ModeDisabled is leftover shared-token install state (unset gate or
	// an historical disabled value). Runtime jobs still require a
	// provisioned role credential. Operators cannot set this via
	// --gitlab-role-migration; emergency recovery is ModeRollback.
	// Ordinary repos install converges leftover disabled installs to
	// enforced.
	ModeDisabled Mode = "disabled"
	// ModeMigrating is the internal install intermediate written while
	// role credentials are being provisioned, before cutover enables
	// enforced. Jobs require a provisioned role credential; there is no
	// shared-token fallback. Operators cannot set this via
	// --gitlab-role-migration.
	ModeMigrating Mode = "migrating"
	// ModeRollback is the operator-initiated emergency recovery path
	// (--gitlab-role-migration=rollback --gitlab-role-rollback-confirmed).
	// Runtime jobs still require a provisioned role credential; the
	// shared token is not selected.
	ModeRollback Mode = "rollback"
	// ModeEnforced requires a provisioned role credential. The shared
	// token is not used. Ordinary unflagged repos install enables this
	// mode once role checks pass.
	ModeEnforced Mode = "enforced"
)

// Kind identifies the job that needs a GitLab credential.
type Kind string

const (
	KindPoller Kind = "poller"
	KindAgent  Kind = "agent"
)

// DeveloperAccessLevel is GitLab's Developer (30) access. Role tokens
// use the same access level as the current shared bot PAT; GitLab
// project-token scopes cannot express finer boundaries.
const DeveloperAccessLevel = 30

// SharedTokenName is the project access token name for the shared bot
// PAT stored as FULLSEND_FORGE_TOKEN.
const SharedTokenName = "fullsend-bot"

// Built-in project access token names. Custom own-credential names are
// derived by CustomTokenName (fullsend-role-<name>).
const (
	PollerTokenName  = "fullsend-poller"
	AnalystTokenName = "fullsend-analyst"
	CoderTokenName   = "fullsend-coder"
)

// RoleState is the configured/unconfigured status of one role secret.
// Presence is boolean. Expiry, revocation, and overlapping tokens are
// reported by DiagnoseLifecycle as LifecycleState on RoleReport.
type RoleState string

const (
	RoleStateUnconfigured RoleState = "unconfigured"
	RoleStateConfigured   RoleState = "configured"
)

// Sentinel errors. Callers distinguish "not registered", "not
// provisioned", and "authentication failed" with errors.Is. Error
// strings and the Error type carry secret *names* only, never values.

// ErrInvalidMode indicates FULLSEND_GITLAB_ROLE_MIGRATION holds a value
// that is not one of the four defined modes.
var ErrInvalidMode = errors.New("invalid GitLab role migration mode")

// ErrInvalidRegistry indicates FULLSEND_GITLAB_ROLE_REGISTRY failed to
// parse or validate as a role registry document.
var ErrInvalidRegistry = errors.New("invalid GitLab role registry")

// ErrUnknownJob indicates a Job has no name to resolve (an empty agent
// name, or a Kind Resolve does not recognize).
var ErrUnknownJob = errors.New("job has no GitLab role mapping")

// ErrUnregistered indicates a job's agent name or harness role does not
// match any registered role in the registry.
var ErrUnregistered = errors.New("GitLab role is not registered")

// ErrUnconfigured indicates a registered role's credential secret is
// not yet provisioned.
var ErrUnconfigured = errors.New("GitLab role credential is not provisioned")

// ErrSharedUnconfigured indicates the shared FULLSEND_FORGE_TOKEN
// credential is not provisioned.
var ErrSharedUnconfigured = errors.New("shared GitLab credential is not provisioned")

// ErrAuthFailed indicates a role credential already failed
// authentication during this job; Resolve never switches identities
// after this.
var ErrAuthFailed = errors.New("GitLab role credential authentication failed")

// Error annotates a sentinel with the role, mode, and secret name
// involved. Secret is a CI/CD variable name, never a token value.
type Error struct {
	Role   Role
	Mode   Mode
	Secret string
	Err    error
}

func (e *Error) Error() string {
	if e == nil || e.Err == nil {
		return "gitlab role credential error"
	}
	var b strings.Builder
	b.WriteString(e.Err.Error())
	if e.Role != "" {
		fmt.Fprintf(&b, ": role %q", e.Role)
	}
	if e.Mode != "" {
		fmt.Fprintf(&b, ": mode %q", e.Mode)
	}
	if e.Secret != "" {
		fmt.Fprintf(&b, ": secret %s", e.Secret)
	}
	return b.String()
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Job selects which GitLab identity a process needs.
type Job struct {
	Kind Kind
	// Name is an agent name or harness role (e.g. "review", "coder").
	// Ignored when Kind is KindPoller.
	Name string
}

// PollerJob is the GitLab poller/controller, not an agent harness.
func PollerJob() Job {
	return Job{Kind: KindPoller}
}

// AgentJob is a harness/agent run identified by agent name or harness role.
func AgentJob(name string) Job {
	return Job{Kind: KindAgent, Name: name}
}

// Request is the input to Resolve. Present maps secret/variable names
// to whether they are non-empty; it must never contain secret values.
type Request struct {
	Mode Mode
	Job  Job
	// Registry is the trusted allowlist. The zero value means built-in
	// roles only (existing installations).
	Registry Registry
	// Present reports whether each named CI/CD variable is non-empty.
	Present map[string]bool
	// FailedSecret is a secret *name* that already failed authentication
	// during this job. When set, Resolve refuses to select any other
	// identity, including the shared token.
	FailedSecret string
}

// Source is the credential Resolve selected. SecretName is a CI/CD
// variable name, not a token value.
type Source struct {
	Role       Role
	Kind       RoleKind
	SecretName string
	Shared     bool
	Reused     bool
	Reason     string
}

// RoleReport is the status of one registered role for Diagnose.
type RoleReport struct {
	Name       Role
	Kind       RoleKind
	SecretName string
	TokenName  string
	ReuseOf    Role
	State      RoleState
	// Lifecycle is presence plus expiry/revocation. Empty when no
	// token inventory was supplied to DiagnoseLifecycle.
	Lifecycle   LifecycleState
	ExpiresAt   string
	TokenIDs    []int
	Overlapping bool
}

// Report is the observable migration/role status. Diagnostics never
// include secret values.
//
// Ready is install/converge-state readiness, not a runtime-readiness
// signal: for leftover ModeDisabled / ModeRollback it is
// SharedPresent (the legacy shared-token install path is complete),
// while runtime credential selection (Resolve / Select / SelectAgent)
// always requires the registered per-role secret in every mode,
// including those two, and never falls back to FULLSEND_FORGE_TOKEN.
// A leftover disabled/rollback install can therefore report Ready
// while `fullsend poll` / `fullsend run` still fail closed with
// ErrUnconfigured because the role secret has not been provisioned
// yet — see TestDiagnoseReadyDivergesFromResolveOnLeftoverModes. #7782
// made runtime job routing role-credential-only in every mode but
// intentionally left status/converge (this function) unchanged; do
// not read Ready as a promise that runtime authentication will
// succeed.
type Report struct {
	Mode          Mode
	SharedPresent bool
	Roles         []RoleReport
	Partial       bool
	Ready         bool
	Missing       []Role
	Diagnostics   []string
}

// SharedSecretName is FULLSEND_FORGE_TOKEN.
func SharedSecretName() string {
	return forge.SecretForgeToken
}

// ModeVariableName is the non-masked role-identity-gate CI/CD variable.
func ModeVariableName() string {
	return forge.VarGitLabRoleMigration
}

// TokenScopes is the GitLab PAT scope list for every role token.
func TokenScopes() []string {
	return []string{"api"}
}

// ParseMode interprets FULLSEND_GITLAB_ROLE_MIGRATION. Empty or
// whitespace-only is ModeDisabled so leftover shared-token installs and
// local runs without the gate remain parseable. Unknown values fail
// closed. Leftover disabled and migrating strings remain parseable so
// ordinary repos install can converge them; they are not
// operator-settable.
func ParseMode(raw string) (Mode, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "", string(ModeDisabled):
		return ModeDisabled, nil
	case string(ModeMigrating):
		return ModeMigrating, nil
	case string(ModeRollback):
		return ModeRollback, nil
	case string(ModeEnforced):
		return ModeEnforced, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidMode, s)
	}
}

// Valid reports whether m is one of the four defined modes.
func (m Mode) Valid() bool {
	switch m {
	case ModeDisabled, ModeMigrating, ModeRollback, ModeEnforced:
		return true
	default:
		return false
	}
}

// UsesSharedOnly reports leftover install/status modes that historically
// used FULLSEND_FORGE_TOKEN. Runtime credential selection ignores this
// predicate; Resolve always requires the registered role secret.
func (m Mode) UsesSharedOnly() bool {
	return m == ModeDisabled || m == ModeRollback
}

// RequiresRoleCredentials reports whether Diagnose treats a missing role
// credential as required. Runtime Resolve always requires the registered
// role secret regardless of this predicate.
func (m Mode) RequiresRoleCredentials() bool {
	return m == ModeEnforced || m == ModeMigrating
}

// OperatorSettable reports whether operators may pass this mode via
// --gitlab-role-migration. Leftover disabled/migrating values remain
// parseable for installed repositories but are not operator-settable.
func (m Mode) OperatorSettable() bool {
	return m == ModeEnforced || m == ModeRollback
}

// ModeFrom reads the migration gate via getenv. A nil getenv uses
// os.Getenv.
func ModeFrom(getenv func(string) string) (Mode, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	return ParseMode(getenv(forge.VarGitLabRoleMigration))
}

// PresenceFrom snapshots whether the shared token and each registered
// role secret are non-empty. A nil getenv uses os.Getenv. A zero
// registry means built-in roles only. Values are not retained.
func PresenceFrom(getenv func(string) string, reg Registry) map[string]bool {
	if getenv == nil {
		getenv = os.Getenv
	}
	names := reg.secretNames()
	out := make(map[string]bool, len(names))
	for _, name := range names {
		out[name] = strings.TrimSpace(getenv(name)) != ""
	}
	return out
}

// Resolve selects the CI/CD variable a job should authenticate with.
// Built-in and custom registered roles use the same lookup: the job
// name is resolved through the registry, then the registration's
// credential reference is selected.
//
// Rules:
//   - Every valid mode requires the registered role secret. There is
//     no shared-token path. Unconfigured is distinct from unregistered
//     and from authentication failure.
//   - FailedSecret set: fail closed with ErrAuthFailed. Never switch
//     identities after a runtime authentication failure.
func Resolve(req Request) (Source, error) {
	if !req.Mode.Valid() {
		return Source{}, &Error{Mode: req.Mode, Err: ErrInvalidMode}
	}
	reg := req.Registry.effective()
	if req.FailedSecret != "" {
		role, _ := reg.roleForSecret(req.FailedSecret)
		return Source{}, &Error{
			Role:   role,
			Mode:   req.Mode,
			Secret: req.FailedSecret,
			Err:    ErrAuthFailed,
		}
	}

	rec, err := roleForJob(reg, req.Job)
	if err != nil {
		return Source{}, &Error{Mode: req.Mode, Err: err}
	}
	secret := rec.Credential.SecretName
	if isPresent(req.Present, secret) {
		return Source{
			Role:       rec.Name,
			Kind:       rec.Kind,
			SecretName: secret,
			Shared:     false,
			Reused:     rec.Credential.Kind == CredentialReuse,
			Reason:     "role credential configured",
		}, nil
	}
	return Source{}, &Error{
		Role:   rec.Name,
		Mode:   req.Mode,
		Secret: secret,
		Err:    ErrUnconfigured,
	}
}

// Diagnose reports migration mode, per-role presence, partial
// configuration, and readiness. Missing role secrets are not drift
// when the mode does not require them. Custom roles in the registry
// are included; an empty registry reports built-ins only.
//
// See Report.Ready: this is install/converge-state readiness, not a
// runtime-readiness signal. Resolve / Select / SelectAgent always
// require the registered role secret, in every mode, regardless of
// what Diagnose reports here.
func Diagnose(mode Mode, present map[string]bool, reg Registry) Report {
	reg = reg.effective()
	rep := Report{
		Mode:          mode,
		SharedPresent: isPresent(present, forge.SecretForgeToken),
	}
	if !mode.Valid() {
		rep.Diagnostics = []string{fmt.Sprintf("invalid migration mode %q", mode)}
		return rep
	}

	roles := reg.Registrations()
	rep.Roles = make([]RoleReport, 0, len(roles))
	configured := 0
	for _, rec := range roles {
		secret := rec.Credential.SecretName
		state := RoleStateUnconfigured
		if isPresent(present, secret) {
			state = RoleStateConfigured
			configured++
		} else {
			rep.Missing = append(rep.Missing, rec.Name)
		}
		rep.Roles = append(rep.Roles, RoleReport{
			Name:       rec.Name,
			Kind:       rec.Kind,
			SecretName: secret,
			TokenName:  rec.Credential.TokenName,
			ReuseOf:    rec.Credential.ReuseOf,
			State:      state,
		})
	}
	total := len(roles)
	rep.Partial = configured > 0 && configured < total
	switch {
	case mode.UsesSharedOnly():
		rep.Ready = rep.SharedPresent
	default:
		rep.Ready = total > 0 && configured == total
	}
	rep.Diagnostics = diagnoseMessages(mode, rep, configured, total)
	return rep
}

func diagnoseMessages(mode Mode, rep Report, configured, total int) []string {
	msgs := []string{
		fmt.Sprintf("mode=%s", mode),
	}
	if rep.SharedPresent {
		msgs = append(msgs, "shared credential FULLSEND_FORGE_TOKEN: configured")
	} else {
		msgs = append(msgs, "shared credential FULLSEND_FORGE_TOKEN: unconfigured")
	}
	for _, rr := range rep.Roles {
		label := string(rr.Name)
		if rr.Kind == RoleKindCustom {
			label += " (custom)"
		}
		if rr.ReuseOf != "" {
			label += " reuse=" + string(rr.ReuseOf)
		}
		switch {
		case rr.State == RoleStateConfigured && mode.UsesSharedOnly():
			msgs = append(msgs, fmt.Sprintf("%s: configured but unused for converge readiness — fullsend poll/run still require this secret at runtime (%s)", label, rr.SecretName))
		case rr.State == RoleStateConfigured:
			msgs = append(msgs, fmt.Sprintf("%s: configured (%s)", label, rr.SecretName))
		case mode.RequiresRoleCredentials():
			msgs = append(msgs, fmt.Sprintf("%s: missing (required) (%s)", label, rr.SecretName))
		default:
			msgs = append(msgs, fmt.Sprintf("%s: unconfigured (not required for converge readiness; required by fullsend poll/run at runtime) (%s)", label, rr.SecretName))
		}
	}
	switch {
	case mode.UsesSharedOnly() && !rep.SharedPresent:
		msgs = append(msgs, "legacy path not ready: shared credential missing")
	case mode.UsesSharedOnly():
		msgs = append(msgs, "legacy shared-token path ready (install/converge state only — fullsend poll/run still require the registered role secret, not the shared token)")
	case configured == total && total > 0:
		msgs = append(msgs, "all role credentials configured")
	case rep.Partial:
		msgs = append(msgs, fmt.Sprintf("partial role configuration: %d/%d roles ready", configured, total))
	default:
		msgs = append(msgs, "no role credentials configured")
	}
	return msgs
}

func roleForJob(reg Registry, job Job) (Registration, error) {
	switch job.Kind {
	case KindPoller:
		rec, ok := reg.Lookup(RolePoller)
		if !ok {
			return Registration{}, ErrUnregistered
		}
		return rec, nil
	case KindAgent:
		if strings.TrimSpace(job.Name) == "" {
			return Registration{}, ErrUnknownJob
		}
		rec, ok := reg.RoleFor(job.Name)
		if !ok {
			return Registration{}, ErrUnregistered
		}
		return rec, nil
	default:
		return Registration{}, ErrUnknownJob
	}
}

func isPresent(present map[string]bool, name string) bool {
	return present[name]
}
