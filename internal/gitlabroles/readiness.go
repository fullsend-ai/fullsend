package gitlabroles

import (
	"errors"
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// BuiltinRoleCheck is the cutover-readiness result for one built-in
// GitLab role. Reasons carry secret *names* only.
type BuiltinRoleCheck struct {
	Name         Role
	SecretName   string
	TokenName    string
	Present      bool
	Ready        bool
	Capabilities []Capability
	Jobs         []string
	Reasons      []string
}

// BuiltinReadiness is the #7501 verification for Poller, Analyst, and
// Coder. It does not enable ModeEnforced and does not retire the
// shared token. Ready is true only when every built-in role is
// provisioned, declares its required capabilities, omits the
// capabilities it must not hold, and maps its jobs onto that identity.
// A present FULLSEND_FORGE_TOKEN never makes a missing built-in role
// ready.
type BuiltinReadiness struct {
	Roles         []BuiltinRoleCheck
	Ready         bool
	Missing       []Role
	SharedPresent bool
	Diagnostics   []string
}

// RegisteredRoleReadiness is the cutover-readiness result for one registered
// role. Unlike BuiltinRoleCheck, it does not assume a fixed capability
// contract: administrator-registered roles are verified against the policy
// they declared in the trusted registry and against every agent they claim.
type RegisteredRoleReadiness struct {
	Name       Role
	SecretName string
	Present    bool
	Ready      bool
	Agents     []string
	Reasons    []string
}

// RegisteredReadiness verifies every role in the registry, including custom
// roles. It is the final authorization check used before cutover. A custom
// role with no agent mapping is not cutover-ready, and every mapped agent must
// resolve to that role's own credential in enforced mode.
type RegisteredReadiness struct {
	Roles       []RegisteredRoleReadiness
	Ready       bool
	Missing     []Role
	Diagnostics []string
}

// WithLifecycle applies token-inventory health to registered-role readiness.
// It uses the same blocking lifecycle states as built-in readiness while
// leaving expiring and overlapping credentials usable for a controlled
// transition.
func (rr RegisteredReadiness) WithLifecycle(lifecycle map[Role]LifecycleState) RegisteredReadiness {
	if len(lifecycle) == 0 {
		return rr
	}
	out := rr
	out.Roles = make([]RegisteredRoleReadiness, len(rr.Roles))
	copy(out.Roles, rr.Roles)
	out.Diagnostics = nil
	out.Missing = nil
	out.Ready = true
	for i := range out.Roles {
		role := &out.Roles[i]
		if state, ok := lifecycle[role.Name]; ok && role.Ready && lifecycleBlocksReadiness(state) {
			role.Ready = false
			role.Reasons = append(append([]string(nil), role.Reasons...), fmt.Sprintf("credential lifecycle is %s", state))
		}
		if !role.Ready {
			out.Ready = false
			out.Missing = append(out.Missing, role.Name)
		}
		if role.Ready {
			out.Diagnostics = append(out.Diagnostics, fmt.Sprintf("registered role %s: ready", role.Name))
		} else {
			out.Diagnostics = append(out.Diagnostics, fmt.Sprintf("registered role %s: not ready: %s", role.Name, strings.Join(role.Reasons, "; ")))
		}
	}
	return out
}

// CheckRegisteredReadiness verifies all registered roles without inspecting
// credential values. Built-in capability invariants are checked separately by
// CheckBuiltinReadiness; this function verifies registration completeness and
// enforced routing for both built-in and custom roles.
func CheckRegisteredReadiness(present map[string]bool, reg Registry) RegisteredReadiness {
	reg = reg.effective()
	out := RegisteredReadiness{
		Roles: make([]RegisteredRoleReadiness, 0, len(reg.Registrations())),
		Ready: true,
	}
	for _, rec := range reg.Registrations() {
		check := RegisteredRoleReadiness{
			Name:       rec.Name,
			SecretName: rec.Credential.SecretName,
			Present:    isPresent(present, rec.Credential.SecretName),
			Agents:     append([]string(nil), rec.Agents...),
		}
		if !check.Present {
			check.Reasons = append(check.Reasons, fmt.Sprintf("credential not provisioned (%s)", rec.Credential.SecretName))
		}
		if len(rec.Agents) == 0 {
			check.Reasons = append(check.Reasons, "role has no agent mapping")
		}
		for _, agent := range rec.Agents {
			src, err := Resolve(Request{
				Mode:     ModeEnforced,
				Job:      AgentJob(agent),
				Registry: reg,
				Present:  present,
			})
			if err != nil {
				check.Reasons = append(check.Reasons, fmt.Sprintf("agent %q cannot resolve in enforced mode", agent))
				continue
			}
			if src.Role != rec.Name || src.SecretName != rec.Credential.SecretName || src.Shared || src.Fallback {
				check.Reasons = append(check.Reasons, fmt.Sprintf("agent %q resolves to %s %s (want %s %s)", agent, src.Role, src.SecretName, rec.Name, rec.Credential.SecretName))
			}
		}
		check.Ready = len(check.Reasons) == 0
		out.Roles = append(out.Roles, check)
		if !check.Ready {
			out.Ready = false
			out.Missing = append(out.Missing, rec.Name)
		}
		if check.Ready {
			out.Diagnostics = append(out.Diagnostics, fmt.Sprintf("registered role %s: ready", rec.Name))
		} else {
			out.Diagnostics = append(out.Diagnostics, fmt.Sprintf("registered role %s: not ready: %s", rec.Name, strings.Join(check.Reasons, "; ")))
		}
	}
	return out
}

// builtinSpec is the verification contract for one built-in role.
type builtinSpec struct {
	role      Role
	job       Job
	agents    []string
	required  []Capability
	forbidden []Capability
}

func builtinReadinessSpecs() []builtinSpec {
	return []builtinSpec{
		{
			role: RolePoller,
			job:  PollerJob(),
			agents: []string{
				"poller",
			},
			required: []Capability{
				CapReadIssues,
				CapDispatchPipeline,
				CapWritePollState,
			},
			forbidden: []Capability{
				CapWriteRepository,
				CapApproveMergeRequest,
			},
		},
		{
			role: RoleAnalyst,
			job:  AgentJob("review"),
			agents: []string{
				"analyst",
				"review",
				"triage",
				"prioritize",
				"retro",
				"scribe",
			},
			required: []Capability{
				CapReadIssues,
				CapWriteIssues,
				CapWriteNotes,
				CapWriteLabels,
				CapApproveMergeRequest,
			},
			forbidden: []Capability{
				CapWriteRepository,
				CapWritePollState,
			},
		},
		{
			role: RoleCoder,
			job:  AgentJob("code"),
			agents: []string{
				"coder",
				"code",
				"fix",
			},
			required: []Capability{
				CapReadIssues,
				CapWriteRepository,
				CapWriteMergeRequest,
			},
			forbidden: []Capability{
				CapApproveMergeRequest,
				CapWritePollState,
			},
		},
	}
}

// CheckBuiltinReadiness verifies Poller, Analyst, and Coder against the
// identity and capability contract. Custom roles are ignored; shared-
// token presence is reported and never used as a substitute. A zero
// registry means built-ins only.
func CheckBuiltinReadiness(present map[string]bool, reg Registry) BuiltinReadiness {
	reg = reg.effective()
	specs := builtinReadinessSpecs()
	out := BuiltinReadiness{
		Roles:         make([]BuiltinRoleCheck, 0, len(specs)),
		SharedPresent: isPresent(present, forge.SecretForgeToken),
	}
	readyCount := 0
	for _, spec := range specs {
		check := checkBuiltinRole(present, reg, spec)
		out.Roles = append(out.Roles, check)
		if check.Ready {
			readyCount++
			continue
		}
		out.Missing = append(out.Missing, spec.role)
	}
	out.Ready = readyCount == len(specs) && len(specs) > 0
	out.Diagnostics = builtinReadinessMessages(out)
	return out
}

func checkBuiltinRole(present map[string]bool, reg Registry, spec builtinSpec) BuiltinRoleCheck {
	c := BuiltinRoleCheck{
		Name: spec.role,
		Jobs: append([]string(nil), spec.agents...),
	}
	rec, ok := reg.Lookup(spec.role)
	if !ok {
		c.Reasons = append(c.Reasons, "role is not registered")
		return finishBuiltinCheck(c)
	}
	c.SecretName = rec.Credential.SecretName
	c.TokenName = rec.Credential.TokenName
	c.Capabilities = rec.Capabilities
	c.Present = isPresent(present, rec.Credential.SecretName)
	if !c.Present {
		c.Reasons = append(c.Reasons, fmt.Sprintf("credential not provisioned (%s)", rec.Credential.SecretName))
	}
	for _, cap := range spec.required {
		if err := Require(rec, cap); err != nil {
			c.Reasons = append(c.Reasons, fmt.Sprintf("lacks required capability %s", cap))
		}
	}
	for _, cap := range spec.forbidden {
		if rec.Has(cap) {
			c.Reasons = append(c.Reasons, fmt.Sprintf("must not declare capability %s", cap))
		}
	}
	for _, agent := range spec.agents {
		mapped, mappedOK := reg.RoleFor(agent)
		if !mappedOK {
			c.Reasons = append(c.Reasons, fmt.Sprintf("agent %q is not mapped", agent))
			continue
		}
		if mapped.Name != spec.role {
			c.Reasons = append(c.Reasons, fmt.Sprintf("agent %q maps to role %q (want %q)", agent, mapped.Name, spec.role))
		}
	}
	appendEnforcedResolveReasons(&c, rec, present, reg, spec)
	return finishBuiltinCheck(c)
}

func appendEnforcedResolveReasons(c *BuiltinRoleCheck, rec Registration, present map[string]bool, reg Registry, spec builtinSpec) {
	src, err := Resolve(Request{
		Mode:     ModeEnforced,
		Job:      spec.job,
		Registry: reg,
		Present:  present,
	})
	if !c.Present {
		if err == nil || !errors.Is(err, ErrUnconfigured) {
			c.Reasons = append(c.Reasons, "missing credential did not fail closed as unconfigured")
		}
		return
	}
	if err != nil {
		c.Reasons = append(c.Reasons, "enforced resolve failed for a provisioned role")
		return
	}
	if src.Role != spec.role || src.SecretName != rec.Credential.SecretName || src.Shared || src.Fallback {
		var cause string
		switch {
		case src.Shared:
			cause = "; selected the shared credential instead of a role credential"
		case src.Fallback:
			cause = "; selected via migration fallback instead of the role credential"
		}
		c.Reasons = append(c.Reasons, fmt.Sprintf("enforced resolve selected role %q secret %q (want role %q secret %q)%s",
			src.Role, src.SecretName, spec.role, rec.Credential.SecretName, cause))
	}
}

func finishBuiltinCheck(c BuiltinRoleCheck) BuiltinRoleCheck {
	c.Ready = len(c.Reasons) == 0
	return c
}

// WithLifecycle downgrades a role's readiness to not-ready when its
// credential lifecycle is expired, revoked, or unverified. A present secret
// is not sufficient for cutover if its project access token is unusable.
func (br BuiltinReadiness) WithLifecycle(lifecycle map[Role]LifecycleState) BuiltinReadiness {
	if len(lifecycle) == 0 {
		return br
	}
	out := br
	out.Roles = make([]BuiltinRoleCheck, len(br.Roles))
	copy(out.Roles, br.Roles)
	out.Missing = nil
	readyCount := 0
	for i := range out.Roles {
		c := &out.Roles[i]
		if state, ok := lifecycle[c.Name]; ok && c.Ready && lifecycleBlocksReadiness(state) {
			c.Ready = false
			c.Reasons = append(append([]string(nil), c.Reasons...), fmt.Sprintf("credential lifecycle is %s", state))
		}
		if c.Ready {
			readyCount++
		} else {
			out.Missing = append(out.Missing, c.Name)
		}
	}
	out.Ready = readyCount == len(out.Roles) && len(out.Roles) > 0
	out.Diagnostics = builtinReadinessMessages(out)
	return out
}

func lifecycleBlocksReadiness(state LifecycleState) bool {
	switch state {
	case LifecycleExpired, LifecycleRevoked, LifecycleUnverified:
		return true
	default:
		return false
	}
}

func builtinReadinessMessages(rep BuiltinReadiness) []string {
	msgs := make([]string, 0, len(rep.Roles)+2)
	for _, c := range rep.Roles {
		msgs = append(msgs, c.diagnostic())
	}
	total := len(rep.Roles)
	readyCount := 0
	for _, c := range rep.Roles {
		if c.Ready {
			readyCount++
		}
	}
	if rep.Ready {
		msgs = append(msgs, fmt.Sprintf("builtin roles ready: %d/%d", readyCount, total))
	} else {
		names := make([]string, len(rep.Missing))
		for i, role := range rep.Missing {
			names[i] = string(role)
		}
		msgs = append(msgs, fmt.Sprintf("builtin roles ready: %d/%d; missing=%s", readyCount, total, strings.Join(names, ",")))
	}
	if rep.SharedPresent && !rep.Ready {
		msgs = append(msgs, "shared credential FULLSEND_FORGE_TOKEN is present and is not a substitute for missing built-in roles")
	}
	return msgs
}

func (c BuiltinRoleCheck) diagnostic() string {
	if c.Ready {
		return fmt.Sprintf("builtin %s: ready secret=%s jobs=%s", c.Name, c.SecretName, strings.Join(c.Jobs, ","))
	}
	return fmt.Sprintf("builtin %s: not ready: %s", c.Name, strings.Join(c.Reasons, "; "))
}
