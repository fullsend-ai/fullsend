// Package gitlabroles defines the GitLab role-credential contract
// (#7497, #7559, #7782).
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
// not perform legacy credential cleanup.
//
// Runtime authentication is role-credential only: Resolve and Select
// select the registered role credential and never read a shared token or
// migration state.
//
// Canonical documentation: docs/contributing/gitlab-role-credentials.md.
package gitlabroles

import (
	"errors"
	"fmt"
	"os"
	"strings"
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

// PollerBootstrapTokenName names the installer-only personal access token the
// install creates on the Poller service account for the duration of one
// pipeline-trigger creation (#8083). It is never published to CI/CD
// variables, logs, or agent environments, and it is revoked before the
// replacement runtime Poller credential is published.
const PollerBootstrapTokenName = "fullsend-poller-bootstrap"

// IsManagedPollerTokenName reports whether Fullsend creates and may revoke
// a credential with this name on the managed Poller account.
func IsManagedPollerTokenName(name string) bool {
	return name == PollerTokenName || name == PollerBootstrapTokenName
}

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

// ErrAuthFailed indicates a role credential already failed
// authentication during this job; Resolve never switches identities
// after this.
var ErrAuthFailed = errors.New("GitLab role credential authentication failed")

// Error annotates a sentinel with the role and secret name
// involved. Secret is a CI/CD variable name, never a token value.
type Error struct {
	Role   Role
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
	Job Job
	// Registry is the trusted allowlist. The zero value means built-in
	// roles only (existing installations).
	Registry Registry
	// Present reports whether each named CI/CD variable is non-empty.
	Present map[string]bool
	// FailedSecret is a secret *name* that already failed authentication
	// during this job. When set, Resolve refuses to select any other
	// identity.
	FailedSecret string
}

// Source is the credential Resolve selected. SecretName is a CI/CD
// variable name, not a token value.
type Source struct {
	Role       Role
	Kind       RoleKind
	SecretName string
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

// Report is the observable role status. Diagnostics never
// include secret values.
//
// Ready is true only when every registered role credential is present,
// for every registered role: it does not treat a lingering shared token
// as sufficient. This
// matches runtime credential selection (Resolve / Select /
// SelectAgent), which always requires the registered per-role secret
// and never reads a shared credential.
type Report struct {
	Roles       []RoleReport
	Partial     bool
	Ready       bool
	Missing     []Role
	Diagnostics []string
}

// TokenScopes is the GitLab PAT scope list for every role token.
func TokenScopes() []string {
	return []string{"api"}
}

// PresenceFrom snapshots each registered role secret. A nil getenv uses
// os.Getenv. A zero
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
//   - The registered role secret is always required. There is no
//     shared-token path. Request.Mode is not consulted. Unconfigured
//     is distinct from unregistered and from authentication failure.
//   - FailedSecret set: fail closed with ErrAuthFailed. Never switch
//     identities after a runtime authentication failure.
func Resolve(req Request) (Source, error) {
	reg := req.Registry.effective()
	if req.FailedSecret != "" {
		role, _ := reg.roleForSecret(req.FailedSecret)
		return Source{}, &Error{Role: role, Secret: req.FailedSecret, Err: ErrAuthFailed}
	}

	rec, err := roleForJob(reg, req.Job)
	if err != nil {
		return Source{}, &Error{Err: err}
	}
	secret := rec.Credential.SecretName
	if isPresent(req.Present, secret) {
		return Source{
			Role:       rec.Name,
			Kind:       rec.Kind,
			SecretName: secret,
			Reused:     rec.Credential.Kind == CredentialReuse,
			Reason:     "role credential configured",
		}, nil
	}
	return Source{}, &Error{
		Role:   rec.Name,
		Secret: secret,
		Err:    ErrUnconfigured,
	}
}

// Diagnose reports per-role presence, partial configuration, and
// readiness. Custom roles in the
// registry are included; an empty registry reports built-ins only.
//
// See Report.Ready: this agrees with runtime credential selection
// (Resolve / Select / SelectAgent), which always requires the
// registered role secret, regardless of what Diagnose reports here.
func Diagnose(present map[string]bool, reg Registry) Report {
	reg = reg.effective()
	rep := Report{}

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
	rep.Ready = total > 0 && configured == total
	rep.Diagnostics = diagnoseMessages(rep, configured, total)
	return rep
}

func diagnoseMessages(rep Report, configured, total int) []string {
	msgs := []string{}
	for _, rr := range rep.Roles {
		label := string(rr.Name)
		if rr.Kind == RoleKindCustom {
			label += " (custom)"
		}
		if rr.ReuseOf != "" {
			label += " reuse=" + string(rr.ReuseOf)
		}
		switch rr.State {
		case RoleStateConfigured:
			msgs = append(msgs, fmt.Sprintf("%s: configured (%s)", label, rr.SecretName))
		default:
			msgs = append(msgs, fmt.Sprintf("%s: missing (required) (%s)", label, rr.SecretName))
		}
	}
	switch {
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
