package gitlabroles

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrCapabilityDenied indicates a registered role does not declare a
// required capability. Routing uses this so Analyst cannot perform
// code writes, Poller cannot change application code, and Coder cannot
// act as the Analyst approval identity through the normal role
// configuration.
var ErrCapabilityDenied = errors.New("GitLab role lacks required capability")

// ErrIdentityMismatch indicates the token that will actually authenticate
// a GitLab API call does not match the CI/CD variable value for the
// dispatch-selected role. A capability check against the selected role is
// meaningless if a different credential ends up authenticating the call,
// so callers must verify the two agree before trusting Require's result.
var ErrIdentityMismatch = errors.New("authenticating GitLab token does not match selected role credential")

// Selection is the dispatch-time result of loading the migration gate,
// trusted registry, and secret-presence map, then resolving a job to a
// credential. Diagnostics and Error values carry secret *names* only.
type Selection struct {
	Mode         Mode
	Job          Job
	Source       Source
	Registration Registration
	Registry     Registry
	Present      map[string]bool
}

// Select loads ModeFrom, LoadRegistry, and PresenceFrom via getenv
// (nil means os.Getenv), then resolves job. In migrating and enforced
// modes an unmapped agent name is rejected with ValidateAgent before
// Resolve so unregistered custom agents fail closed rather than
// guessing an identity. Disabled and rollback skip that pre-check so
// existing unmapped jobs keep using the shared token.
func Select(job Job, getenv func(string) string) (Selection, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	mode, err := ModeFrom(getenv)
	if err != nil {
		return Selection{}, err
	}
	reg, err := LoadRegistry(getenv)
	if err != nil {
		return Selection{}, err
	}
	return selectResolved(mode, job, reg, getenv)
}

// SelectAgent maps a running agent onto a registered identity and
// selects its credential. The agent name is preferred when it is a
// registered mapping (custom-role agents); otherwise the harness
// role: value is used so a custom agent may *reference* a registered
// identity without being listed in the registry document.
func SelectAgent(agentName, harnessRole string, getenv func(string) string) (Selection, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	mode, err := ModeFrom(getenv)
	if err != nil {
		return Selection{}, err
	}
	reg, err := LoadRegistry(getenv)
	if err != nil {
		return Selection{}, err
	}
	return selectResolved(mode, JobForAgent(reg, agentName, harnessRole), reg, getenv)
}

// JobForAgent maps a running agent onto a Job. Prefer the agent name
// when it is registered; otherwise use the harness role: field.
func JobForAgent(reg Registry, agentName, harnessRole string) Job {
	if strings.TrimSpace(agentName) != "" {
		if _, ok := reg.RoleFor(agentName); ok {
			return AgentJob(agentName)
		}
	}
	if strings.TrimSpace(harnessRole) != "" {
		if _, ok := reg.RoleFor(harnessRole); ok {
			return AgentJob(harnessRole)
		}
	}
	return AgentJob(agentName)
}

func selectResolved(mode Mode, job Job, reg Registry, getenv func(string) string) (Selection, error) {
	if !mode.UsesSharedOnly() {
		switch job.Kind {
		case KindPoller:
			// Always registered as RolePoller.
		case KindAgent:
			if err := reg.ValidateAgent(job.Name); err != nil {
				return Selection{}, &Error{Mode: mode, Err: err}
			}
		default:
			return Selection{}, &Error{Mode: mode, Err: ErrUnknownJob}
		}
	}
	present := PresenceFrom(getenv, reg)
	src, err := Resolve(Request{
		Mode:     mode,
		Job:      job,
		Registry: reg,
		Present:  present,
	})
	if err != nil {
		return Selection{}, err
	}
	return Selection{
		Mode:         mode,
		Job:          job,
		Source:       src,
		Registration: registrationForSource(reg, src),
		Registry:     reg,
		Present:      present,
	}, nil
}

func registrationForSource(reg Registry, src Source) Registration {
	if src.Role == "" {
		return Registration{}
	}
	rec, ok := reg.Lookup(src.Role)
	if !ok {
		return Registration{}
	}
	return rec
}

// Token reads the selected CI/CD variable via getenv. Values are
// returned to the caller for authentication and must never be logged.
func (s Selection) Token(getenv func(string) string) (string, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if s.Source.SecretName == "" {
		return "", &Error{Mode: s.Mode, Err: ErrUnconfigured}
	}
	token := strings.TrimSpace(getenv(s.Source.SecretName))
	if token == "" {
		err := ErrUnconfigured
		if s.Source.Shared {
			err = ErrSharedUnconfigured
		}
		return "", &Error{
			Role:   s.Source.Role,
			Mode:   s.Mode,
			Secret: s.Source.SecretName,
			Err:    err,
		}
	}
	return token, nil
}

// IdentitySource is a non-secret label for how the credential was
// chosen: "role", "shared", or "migration-fallback".
func (s Selection) IdentitySource() string {
	switch {
	case s.Source.Fallback:
		return "migration-fallback"
	case s.Source.Shared:
		return "shared"
	default:
		return "role"
	}
}

// Diagnostics returns human-readable identity lines with names only.
func (s Selection) Diagnostics() []string {
	role := string(s.Source.Role)
	if role == "" {
		role = "unmapped"
	}
	kind := string(s.Source.Kind)
	if kind == "" {
		kind = "none"
	}
	lines := []string{
		fmt.Sprintf("GitLab identity role=%s kind=%s source=%s secret=%s mode=%s",
			role, kind, s.IdentitySource(), s.Source.SecretName, s.Mode),
	}
	if s.Source.Reason != "" {
		lines = append(lines, "GitLab identity: "+s.Source.Reason)
	}
	if s.Source.Reused && s.Registration.Credential.ReuseOf != "" {
		lines = append(lines, fmt.Sprintf("GitLab identity reuses credential of role %s", s.Registration.Credential.ReuseOf))
	}
	return lines
}

// Require reports ErrCapabilityDenied when rec does not declare cap.
func Require(rec Registration, cap Capability) error {
	if rec.Has(cap) {
		return nil
	}
	return &Error{
		Role: rec.Name,
		Err:  fmt.Errorf("%w: %s", ErrCapabilityDenied, cap),
	}
}

// AuthFailed is the fail-closed annotation for a runtime 401/403 (or
// equivalent) of a selected credential. Callers must not Resolve again
// with a different job or a cleared FailedSecret.
func AuthFailed(role Role, mode Mode, secret string) error {
	return &Error{Role: role, Mode: mode, Secret: secret, Err: ErrAuthFailed}
}
