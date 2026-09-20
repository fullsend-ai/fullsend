// Package gitlabroles defines the GitLab Poller/Analyst/Coder credential
// contract and migration feature gates (#7497).
//
// This package is the internal contract for provisioning (#7498) and
// routing (#7499). It does not provision tokens, mutate CI configuration,
// or change job authentication. When migration mode is disabled (the
// default), Resolve selects the shared FULLSEND_FORGE_TOKEN exactly as
// existing installations do.
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

// Role is a GitLab responsibility identity. These are not one-to-one
// with harness role names: several agents share Analyst or Coder.
type Role string

const (
	RolePoller  Role = "poller"
	RoleAnalyst Role = "analyst"
	RoleCoder   Role = "coder"
)

// Mode is the explicit migration/rollback feature gate stored in
// FULLSEND_GITLAB_ROLE_MIGRATION. Absent or empty is ModeDisabled.
type Mode string

const (
	// ModeDisabled is the default. Jobs use only the shared
	// FULLSEND_FORGE_TOKEN. Role secrets, if present, are ignored.
	ModeDisabled Mode = "disabled"
	// ModeMigrating selects a role credential when it is provisioned
	// and falls back to the shared token only when that role is not
	// yet configured. An authentication failure of a configured role
	// credential does not fall back.
	ModeMigrating Mode = "migrating"
	// ModeRollback forces the shared token even when role credentials
	// exist. It is an operator-initiated rollback, not an implicit
	// recovery path.
	ModeRollback Mode = "rollback"
	// ModeEnforced requires a provisioned role credential. The shared
	// token is not used. Cutover (#7501) is what enables this mode.
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

// RoleState is the configured/unconfigured status of one role secret.
// Presence is boolean; this contract does not inspect expiry or
// authorization (those belong to #7500).
type RoleState string

const (
	RoleStateUnconfigured RoleState = "unconfigured"
	RoleStateConfigured   RoleState = "configured"
)

// Sentinel errors. Callers distinguish "not provisioned" from
// "authentication failed" with errors.Is. Error strings and the Error
// type carry secret *names* only, never values.
var (
	ErrInvalidMode        = errors.New("invalid GitLab role migration mode")
	ErrUnknownJob         = errors.New("job has no GitLab role mapping")
	ErrUnconfigured       = errors.New("GitLab role credential is not provisioned")
	ErrSharedUnconfigured = errors.New("shared GitLab credential is not provisioned")
	ErrAuthFailed         = errors.New("GitLab role credential authentication failed")
)

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
		fmt.Fprintf(&b, ": role %s", e.Role)
	}
	if e.Mode != "" {
		fmt.Fprintf(&b, ": mode %s", e.Mode)
	}
	if e.Secret != "" {
		fmt.Fprintf(&b, ": %s", e.Secret)
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
	SecretName string
	Shared     bool
	Fallback   bool
	Reason     string
}

// RoleReport is the status of one role for Diagnose.
type RoleReport struct {
	Role       Role
	SecretName string
	TokenName  string
	State      RoleState
}

// Report is the observable migration/role status. Diagnostics never
// include secret values.
type Report struct {
	Mode          Mode
	SharedPresent bool
	Roles         []RoleReport
	Partial       bool
	Ready         bool
	Missing       []Role
	Diagnostics   []string
}

// AllRoles returns Poller, Analyst, and Coder in stable order.
func AllRoles() []Role {
	return []Role{RolePoller, RoleAnalyst, RoleCoder}
}

// SecretName returns the masked CI/CD variable that stores the role's
// project access token. The empty string means role is not recognized.
func SecretName(role Role) string {
	switch role {
	case RolePoller:
		return forge.SecretGitLabPollerToken
	case RoleAnalyst:
		return forge.SecretGitLabAnalystToken
	case RoleCoder:
		return forge.SecretGitLabCoderToken
	default:
		return ""
	}
}

// SecretNames returns the three role secret names in AllRoles order.
func SecretNames() []string {
	roles := AllRoles()
	out := make([]string, len(roles))
	for i, role := range roles {
		out[i] = SecretName(role)
	}
	return out
}

// SharedSecretName is FULLSEND_FORGE_TOKEN.
func SharedSecretName() string {
	return forge.SecretForgeToken
}

// ModeVariableName is the non-masked migration-gate CI/CD variable.
func ModeVariableName() string {
	return forge.VarGitLabRoleMigration
}

// ProjectAccessTokenName is the GitLab PAT name created for a role.
func ProjectAccessTokenName(role Role) string {
	switch role {
	case RolePoller:
		return "fullsend-poller"
	case RoleAnalyst:
		return "fullsend-analyst"
	case RoleCoder:
		return "fullsend-coder"
	default:
		return ""
	}
}

// TokenScopes is the GitLab PAT scope list for every role token.
func TokenScopes() []string {
	return []string{"api"}
}

// RoleFor maps an agent name or harness role onto a GitLab identity.
// The mapping is the #7424 decision: Analyst covers review/issue work,
// Coder covers code/fix, Poller is the controller.
func RoleFor(name string) (Role, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "poller":
		return RolePoller, true
	case "analyst", "review", "triage", "prioritize", "retro", "scribe":
		return RoleAnalyst, true
	case "coder", "code", "fix":
		return RoleCoder, true
	default:
		return "", false
	}
}

// ParseMode interprets FULLSEND_GITLAB_ROLE_MIGRATION. Empty or
// whitespace-only is ModeDisabled so existing installations stay on
// the shared token. Unknown values fail closed.
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

// UsesSharedOnly reports whether jobs must use FULLSEND_FORGE_TOKEN
// regardless of role-secret presence.
func (m Mode) UsesSharedOnly() bool {
	return m == ModeDisabled || m == ModeRollback
}

// AllowsSharedFallback reports whether an unconfigured role may use
// the shared token. Authentication failures never use this path.
func (m Mode) AllowsSharedFallback() bool {
	return m == ModeMigrating
}

// RequiresRoleCredentials reports whether a missing role credential is
// an error (no shared-token fallback).
func (m Mode) RequiresRoleCredentials() bool {
	return m == ModeEnforced
}

// ModeFrom reads the migration gate via getenv. A nil getenv uses
// os.Getenv.
func ModeFrom(getenv func(string) string) (Mode, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	return ParseMode(getenv(forge.VarGitLabRoleMigration))
}

// PresenceFrom snapshots whether the shared token and each role secret
// are non-empty. A nil getenv uses os.Getenv. Values are not retained.
func PresenceFrom(getenv func(string) string) map[string]bool {
	if getenv == nil {
		getenv = os.Getenv
	}
	names := append([]string{forge.SecretForgeToken}, SecretNames()...)
	out := make(map[string]bool, len(names))
	for _, name := range names {
		out[name] = strings.TrimSpace(getenv(name)) != ""
	}
	return out
}

// Resolve selects the CI/CD variable a job should authenticate with.
//
// Rules:
//   - ModeDisabled / ModeRollback: shared token only.
//   - ModeMigrating: role secret if present, otherwise explicit shared
//     fallback. Unconfigured is distinct from authentication failure.
//   - ModeEnforced: role secret required; no shared fallback.
//   - FailedSecret set: fail closed with ErrAuthFailed. Never switch
//     identities after a runtime authentication failure.
func Resolve(req Request) (Source, error) {
	if !req.Mode.Valid() {
		return Source{}, &Error{Mode: req.Mode, Err: ErrInvalidMode}
	}
	if req.FailedSecret != "" {
		role, _ := roleForSecret(req.FailedSecret)
		return Source{}, &Error{
			Role:   role,
			Mode:   req.Mode,
			Secret: req.FailedSecret,
			Err:    ErrAuthFailed,
		}
	}
	if req.Mode.UsesSharedOnly() {
		src, err := resolveShared(req)
		if err != nil {
			return Source{}, err
		}
		if role, rerr := roleForJob(req.Job); rerr == nil {
			src.Role = role
		}
		src.Fallback = false
		src.Reason = sharedOnlyReason(req.Mode)
		return src, nil
	}

	role, err := roleForJob(req.Job)
	if err != nil {
		return Source{}, &Error{Mode: req.Mode, Err: err}
	}
	secret := SecretName(role)
	if isPresent(req.Present, secret) {
		return Source{
			Role:       role,
			SecretName: secret,
			Shared:     false,
			Fallback:   false,
			Reason:     "role credential configured",
		}, nil
	}
	if req.Mode.AllowsSharedFallback() {
		src, sharedErr := resolveShared(req)
		if sharedErr != nil {
			return Source{}, &Error{
				Role:   role,
				Mode:   req.Mode,
				Secret: secret,
				Err:    ErrUnconfigured,
			}
		}
		src.Role = role
		src.Fallback = true
		src.Reason = "role credential unconfigured; explicit migration fallback to shared token"
		return src, nil
	}
	return Source{}, &Error{
		Role:   role,
		Mode:   req.Mode,
		Secret: secret,
		Err:    ErrUnconfigured,
	}
}

// Diagnose reports migration mode, per-role presence, partial
// configuration, and readiness. Missing role secrets are not drift
// when the mode does not require them.
func Diagnose(mode Mode, present map[string]bool) Report {
	rep := Report{
		Mode:          mode,
		SharedPresent: isPresent(present, forge.SecretForgeToken),
	}
	if !mode.Valid() {
		rep.Diagnostics = []string{fmt.Sprintf("invalid migration mode %q", mode)}
		return rep
	}

	roles := AllRoles()
	rep.Roles = make([]RoleReport, 0, len(roles))
	configured := 0
	for _, role := range roles {
		secret := SecretName(role)
		state := RoleStateUnconfigured
		if isPresent(present, secret) {
			state = RoleStateConfigured
			configured++
		} else {
			rep.Missing = append(rep.Missing, role)
		}
		rep.Roles = append(rep.Roles, RoleReport{
			Role:       role,
			SecretName: secret,
			TokenName:  ProjectAccessTokenName(role),
			State:      state,
		})
	}
	rep.Partial = configured > 0 && configured < len(roles)
	switch {
	case mode.UsesSharedOnly():
		rep.Ready = rep.SharedPresent
	default:
		rep.Ready = configured == len(roles)
	}
	rep.Diagnostics = diagnoseMessages(mode, rep, configured, len(roles))
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
		switch {
		case rr.State == RoleStateConfigured && mode.UsesSharedOnly():
			msgs = append(msgs, fmt.Sprintf("%s: configured but unused (%s)", rr.Role, rr.SecretName))
		case rr.State == RoleStateConfigured:
			msgs = append(msgs, fmt.Sprintf("%s: configured (%s)", rr.Role, rr.SecretName))
		case mode.RequiresRoleCredentials():
			msgs = append(msgs, fmt.Sprintf("%s: missing (required) (%s)", rr.Role, rr.SecretName))
		case mode.AllowsSharedFallback():
			msgs = append(msgs, fmt.Sprintf("%s: pending (%s)", rr.Role, rr.SecretName))
		default:
			msgs = append(msgs, fmt.Sprintf("%s: unconfigured (not required) (%s)", rr.Role, rr.SecretName))
		}
	}
	switch {
	case mode.UsesSharedOnly() && !rep.SharedPresent:
		msgs = append(msgs, "legacy path not ready: shared credential missing")
	case mode.UsesSharedOnly():
		msgs = append(msgs, "legacy shared-token path ready")
	case configured == total:
		msgs = append(msgs, "all role credentials configured")
	case rep.Partial:
		msgs = append(msgs, fmt.Sprintf("partial role configuration: %d/%d roles ready", configured, total))
	default:
		msgs = append(msgs, "no role credentials configured")
	}
	return msgs
}

func roleForJob(job Job) (Role, error) {
	switch job.Kind {
	case KindPoller:
		return RolePoller, nil
	case KindAgent:
		if job.Name == "" {
			return "", ErrUnknownJob
		}
		role, ok := RoleFor(job.Name)
		if !ok {
			return "", ErrUnknownJob
		}
		return role, nil
	default:
		return "", ErrUnknownJob
	}
}

func roleForSecret(name string) (Role, bool) {
	for _, role := range AllRoles() {
		if SecretName(role) == name {
			return role, true
		}
	}
	return "", false
}

func resolveShared(req Request) (Source, error) {
	if !isPresent(req.Present, forge.SecretForgeToken) {
		return Source{}, &Error{
			Mode:   req.Mode,
			Secret: forge.SecretForgeToken,
			Err:    ErrSharedUnconfigured,
		}
	}
	return Source{
		SecretName: forge.SecretForgeToken,
		Shared:     true,
	}, nil
}

func sharedOnlyReason(mode Mode) string {
	if mode == ModeRollback {
		return "rollback: shared credential selected explicitly"
	}
	return "migration disabled: shared credential selected"
}

func isPresent(present map[string]bool, name string) bool {
	return present[name]
}
