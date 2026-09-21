package gitlabroles

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// Role is a GitLab responsibility identity. Built-in and custom
// registered roles share this type; there is no closed enum.
type Role string

const (
	RolePoller  Role = "poller"
	RoleAnalyst Role = "analyst"
	RoleCoder   Role = "coder"
)

// RoleKind distinguishes built-in roles from administrator-registered
// custom roles. Both kinds are first-class Registry entries.
type RoleKind string

const (
	RoleKindBuiltin RoleKind = "builtin"
	RoleKindCustom  RoleKind = "custom"
)

// CredentialKind is how a registered role obtains its token.
type CredentialKind string

const (
	// CredentialOwn means the role has its own CI/CD secret.
	CredentialOwn CredentialKind = "own"
	// CredentialReuse means the role shares another registered role's
	// credential. The registry stores a reference, not a copy of the
	// secret value.
	CredentialReuse CredentialKind = "reuse"
)

// Capability is policy metadata used for validation. GitLab project
// tokens still use the coarse `api` scope; these flags are the
// contract routing (#7499) enforces, not GitLab ACL grants.
type Capability string

const (
	CapReadIssues          Capability = "read_issues"
	CapWriteIssues         Capability = "write_issues"
	CapWriteNotes          Capability = "write_notes"
	CapWriteLabels         Capability = "write_labels"
	CapDispatchPipeline    Capability = "dispatch_pipeline"
	CapWritePollState      Capability = "write_poll_state"
	CapWriteRepository     Capability = "write_repository"
	CapWriteMergeRequest   Capability = "write_merge_request"
	CapApproveMergeRequest Capability = "approve_merge_request"
)

// roleNamePattern mirrors mintcore.RolePattern without importing
// mintcore (module-boundary / WASM graph).
var roleNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

var envNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

var knownCapabilities = map[Capability]struct{}{
	CapReadIssues:          {},
	CapWriteIssues:         {},
	CapWriteNotes:          {},
	CapWriteLabels:         {},
	CapDispatchPipeline:    {},
	CapWritePollState:      {},
	CapWriteRepository:     {},
	CapWriteMergeRequest:   {},
	CapApproveMergeRequest: {},
}

// CredentialRef is a reference to a role credential. SecretName is a
// CI/CD variable name, never a token value.
type CredentialRef struct {
	Kind       CredentialKind
	SecretName string
	ReuseOf    Role
	TokenName  string
}

// Registration is one allowlisted role in the trusted registry.
type Registration struct {
	Name           Role
	Kind           RoleKind
	Responsibility string
	Credential     CredentialRef
	Capabilities   []Capability
	Agents         []string
}

// Has reports whether the registration declares capability c.
func (r Registration) Has(c Capability) bool {
	for _, got := range r.Capabilities {
		if got == c {
			return true
		}
	}
	return false
}

// Registry is the administrator-controlled allowlist of GitLab
// responsibility identities. Built-in Poller, Analyst, and Coder are
// always present. Custom roles come only from installation state
// (FULLSEND_GITLAB_ROLE_REGISTRY). Repository files, harness
// documents, and merge-request content are not sources of
// registration and cannot elevate a role.
type Registry struct {
	roles   []Registration
	byName  map[Role]int
	byAgent map[string]Role
}

// registryFile is the JSON document stored in
// FULLSEND_GITLAB_ROLE_REGISTRY. Unknown fields are rejected so a
// leaked token value cannot hide under a key such as "token".
type registryFile struct {
	Roles []registryRole `json:"roles"`
}

type registryRole struct {
	Name           string   `json:"name"`
	Responsibility string   `json:"responsibility"`
	Credential     string   `json:"credential"`
	Reuse          string   `json:"reuse,omitempty"`
	SecretName     string   `json:"secret_name,omitempty"`
	Capabilities   []string `json:"capabilities"`
	Agents         []string `json:"agents"`
}

// BuiltinRoles returns Poller, Analyst, and Coder in stable order.
func BuiltinRoles() []Role {
	return []Role{RolePoller, RoleAnalyst, RoleCoder}
}

// KnownCapabilities returns the capability vocabulary in stable order.
func KnownCapabilities() []Capability {
	return []Capability{
		CapReadIssues,
		CapWriteIssues,
		CapWriteNotes,
		CapWriteLabels,
		CapDispatchPipeline,
		CapWritePollState,
		CapWriteRepository,
		CapWriteMergeRequest,
		CapApproveMergeRequest,
	}
}

// CustomSecretName is the derived CI/CD variable for a custom role's
// own credential. Hyphens become underscores.
func CustomSecretName(role Role) string {
	return "FULLSEND_GITLAB_ROLE_" + roleIdentifier(string(role)) + "_TOKEN"
}

// customRoleTokenPrefix is the GitLab project access token name prefix
// for a custom role's own credential.
const customRoleTokenPrefix = "fullsend-role-"

// CustomTokenName is the GitLab project access token name for a
// custom role's own credential.
func CustomTokenName(role Role) string {
	return customRoleTokenPrefix + string(role)
}

// IsRoleProjectTokenName reports whether name is a built-in or custom
// role project access token (not the shared fullsend-bot token).
func IsRoleProjectTokenName(name string) bool {
	switch name {
	case PollerTokenName, AnalystTokenName, CoderTokenName:
		return true
	default:
		return strings.HasPrefix(name, customRoleTokenPrefix)
	}
}

// BuiltinRegistry returns the three built-in roles and no custom
// roles. Existing installations with an empty registry variable use
// this set.
func BuiltinRegistry() Registry {
	reg, err := newRegistry(nil)
	if err != nil {
		// Built-in table is static; a construction error is a bug.
		panic(err)
	}
	return reg
}

// ParseRegistry reads administrator registry JSON and merges it with
// the built-in roles. Empty or whitespace-only input is built-ins
// only. The document may contain credential references and policy,
// never raw secret values.
func ParseRegistry(raw string) (Registry, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return BuiltinRegistry(), nil
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(s)))
	dec.DisallowUnknownFields()
	var file registryFile
	if err := dec.Decode(&file); err != nil {
		return Registry{}, fmt.Errorf("%w: %v", ErrInvalidRegistry, err)
	}
	if dec.More() {
		return Registry{}, fmt.Errorf("%w: trailing data", ErrInvalidRegistry)
	}
	custom := make([]Registration, 0, len(file.Roles))
	for i, row := range file.Roles {
		rec, err := registrationFromFile(row)
		if err != nil {
			return Registry{}, fmt.Errorf("%w: roles[%d]: %v", ErrInvalidRegistry, i, err)
		}
		custom = append(custom, rec)
	}
	return newRegistry(custom)
}

// LoadRegistry reads FULLSEND_GITLAB_ROLE_REGISTRY via getenv. A nil
// getenv uses os.Getenv. This is the only supported way to load
// custom roles.
func LoadRegistry(getenv func(string) string) (Registry, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	return ParseRegistry(getenv(forge.VarGitLabRoleRegistry))
}

// RegistryVariableName is FULLSEND_GITLAB_ROLE_REGISTRY.
func RegistryVariableName() string {
	return forge.VarGitLabRoleRegistry
}

// Registrations returns a copy of the registered roles in stable
// order: built-ins first, then custom roles in document order.
func (r Registry) Registrations() []Registration {
	if len(r.roles) == 0 {
		return BuiltinRegistry().Registrations()
	}
	out := make([]Registration, len(r.roles))
	copy(out, r.roles)
	return out
}

// Lookup returns the registration for a role name.
func (r Registry) Lookup(name Role) (Registration, bool) {
	reg := r.effective()
	i, ok := reg.byName[name]
	if !ok {
		return Registration{}, false
	}
	return reg.roles[i], true
}

// RoleFor maps an agent name or harness role onto a registered GitLab
// identity. Built-in aliases (review → analyst, code → coder) and
// custom agent names share this lookup.
func (r Registry) RoleFor(name string) (Registration, bool) {
	reg := r.effective()
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return Registration{}, false
	}
	if role, ok := reg.byAgent[key]; ok {
		return reg.Lookup(role)
	}
	return reg.Lookup(Role(key))
}

// ValidateAgent reports whether a custom agent (or harness role) may
// use name. Unregistered names return ErrUnregistered. Empty names
// return ErrUnknownJob.
func (r Registry) ValidateAgent(name string) error {
	if strings.TrimSpace(name) == "" {
		return ErrUnknownJob
	}
	if _, ok := r.RoleFor(name); !ok {
		return ErrUnregistered
	}
	return nil
}

func (r Registry) effective() Registry {
	if len(r.roles) == 0 {
		return BuiltinRegistry()
	}
	return r
}

func (r Registry) roleForSecret(name string) (Role, bool) {
	reg := r.effective()
	for _, rec := range reg.roles {
		if rec.Credential.SecretName == name {
			return rec.Name, true
		}
	}
	return "", false
}

func (r Registry) secretNames() []string {
	reg := r.effective()
	seen := make(map[string]struct{}, len(reg.roles)+1)
	out := make([]string, 0, len(reg.roles)+1)
	add := func(name string) {
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	add(forge.SecretForgeToken)
	for _, rec := range reg.roles {
		add(rec.Credential.SecretName)
	}
	return out
}

func newRegistry(custom []Registration) (Registry, error) {
	builtins := builtinRegistrations()
	r := Registry{
		roles:   make([]Registration, 0, len(builtins)+len(custom)),
		byName:  make(map[Role]int, len(builtins)+len(custom)),
		byAgent: make(map[string]Role, 16),
	}
	for _, rec := range builtins {
		if err := r.add(rec); err != nil {
			return Registry{}, fmt.Errorf("%w: %v", ErrInvalidRegistry, err)
		}
	}
	for _, rec := range custom {
		rec.Kind = RoleKindCustom
		if err := r.add(rec); err != nil {
			return Registry{}, fmt.Errorf("%w: %v", ErrInvalidRegistry, err)
		}
	}
	if err := r.resolveReuse(); err != nil {
		return Registry{}, err
	}
	if err := r.checkSecretNameCollisions(); err != nil {
		return Registry{}, err
	}
	return r, nil
}

// checkSecretNameCollisions rejects a registry where two CredentialOwn
// roles resolve to the same SecretName. CustomSecretName is not
// injective (hyphens and underscores both normalize to underscore), so
// distinct role names such as "ci-check" and "ci_check" could otherwise
// silently share one CI/CD variable outside the explicit reuse path.
func (r *Registry) checkSecretNameCollisions() error {
	seen := make(map[string]Role, len(r.roles))
	for _, rec := range r.roles {
		if rec.Credential.Kind != CredentialOwn {
			continue
		}
		if existing, ok := seen[rec.Credential.SecretName]; ok {
			return fmt.Errorf("%w: role %q and role %q derive the same secret name %q", ErrInvalidRegistry, existing, rec.Name, rec.Credential.SecretName)
		}
		seen[rec.Credential.SecretName] = rec.Name
	}
	return nil
}

func (r *Registry) add(rec Registration) error {
	if !validRoleName(string(rec.Name)) {
		return fmt.Errorf("invalid role name %q", rec.Name)
	}
	if _, exists := r.byName[rec.Name]; exists {
		return fmt.Errorf("role %q is already registered", rec.Name)
	}
	if existing, ok := r.byAgent[string(rec.Name)]; ok {
		return fmt.Errorf("role %q: name collides with agent mapping already assigned to role %q", rec.Name, existing)
	}
	switch rec.Credential.Kind {
	case CredentialOwn, CredentialReuse:
	default:
		return fmt.Errorf("role %q: invalid credential kind %q", rec.Name, rec.Credential.Kind)
	}
	if rec.Credential.Kind == CredentialReuse && rec.Credential.ReuseOf == "" {
		return fmt.Errorf("role %q: reuse requires a target role", rec.Name)
	}
	if rec.Credential.Kind == CredentialOwn && rec.Credential.ReuseOf != "" {
		return fmt.Errorf("role %q: own credential cannot name a reuse target", rec.Name)
	}
	for _, cap := range rec.Capabilities {
		if _, ok := knownCapabilities[cap]; !ok {
			return fmt.Errorf("role %q: unknown capability %q", rec.Name, cap)
		}
	}

	// Every role's own name occupies its slot in the RoleFor lookup
	// space even when it is not explicitly listed in Agents (builtins
	// always list their own name; custom roles do not have to). Fold
	// it into the same key set as the explicit agent list so both the
	// byAgent-collision check below and the byName check catch a later
	// role that tries to claim this name via its own agents list,
	// regardless of registration order.
	agentKeys := make([]string, 0, len(rec.Agents)+1)
	agentKeySeen := make(map[string]struct{}, len(rec.Agents)+1)
	ownKey := string(rec.Name)
	agentKeys = append(agentKeys, ownKey)
	agentKeySeen[ownKey] = struct{}{}
	for _, agent := range rec.Agents {
		key := strings.TrimSpace(agent)
		if !validRoleName(key) {
			return fmt.Errorf("role %q: invalid agent name %q", rec.Name, agent)
		}
		if _, dup := agentKeySeen[key]; dup {
			continue
		}
		agentKeySeen[key] = struct{}{}
		agentKeys = append(agentKeys, key)
	}
	for _, key := range agentKeys {
		if existing, ok := r.byAgent[key]; ok {
			return fmt.Errorf("agent %q is already mapped to role %q", key, existing)
		}
		if existing, ok := r.byName[Role(key)]; ok {
			return fmt.Errorf("agent %q collides with role %q already registered", key, r.roles[existing].Name)
		}
		r.byAgent[key] = rec.Name
	}
	r.byName[rec.Name] = len(r.roles)
	r.roles = append(r.roles, rec)
	return nil
}

func (r *Registry) resolveReuse() error {
	for i := range r.roles {
		rec := &r.roles[i]
		if rec.Credential.Kind != CredentialReuse {
			continue
		}
		seen := map[Role]struct{}{rec.Name: {}}
		cur := rec.Credential.ReuseOf
		for {
			if _, loop := seen[cur]; loop {
				return fmt.Errorf("%w: role %q: credential reuse cycle", ErrInvalidRegistry, rec.Name)
			}
			seen[cur] = struct{}{}
			idx, ok := r.byName[cur]
			if !ok {
				return fmt.Errorf("%w: role %q reuses unregistered role %q", ErrInvalidRegistry, rec.Name, cur)
			}
			target := r.roles[idx]
			if target.Credential.Kind == CredentialOwn {
				if rec.Credential.SecretName != "" && rec.Credential.SecretName != target.Credential.SecretName {
					return fmt.Errorf("%w: role %q: secret_name %q does not match reused secret %q", ErrInvalidRegistry, rec.Name, rec.Credential.SecretName, target.Credential.SecretName)
				}
				rec.Credential.SecretName = target.Credential.SecretName
				rec.Credential.TokenName = ""
				break
			}
			cur = target.Credential.ReuseOf
		}
	}
	return nil
}

func registrationFromFile(row registryRole) (Registration, error) {
	name := strings.TrimSpace(row.Name)
	if looksLikeSecretValue(name) {
		return Registration{}, fmt.Errorf("name must be a role name, not a secret value")
	}
	if !validRoleName(name) {
		return Registration{}, fmt.Errorf("invalid role name %q", row.Name)
	}
	credentialRaw := strings.TrimSpace(row.Credential)
	if looksLikeSecretValue(credentialRaw) {
		return Registration{}, fmt.Errorf("credential must be \"own\" or \"reuse\", not a secret value")
	}
	kind := CredentialKind(strings.ToLower(credentialRaw))
	if kind == "" {
		kind = CredentialOwn
	}
	caps := make([]Capability, 0, len(row.Capabilities))
	for _, c := range row.Capabilities {
		capName := strings.TrimSpace(c)
		if looksLikeSecretValue(capName) {
			return Registration{}, fmt.Errorf("capabilities must not contain a secret value")
		}
		caps = append(caps, Capability(capName))
	}
	agents := make([]string, 0, len(row.Agents))
	for _, a := range row.Agents {
		agentName := strings.TrimSpace(a)
		if looksLikeSecretValue(agentName) {
			return Registration{}, fmt.Errorf("agents must not contain a secret value")
		}
		agents = append(agents, agentName)
	}
	secret := strings.TrimSpace(row.SecretName)
	if looksLikeSecretValue(secret) {
		return Registration{}, fmt.Errorf("secret_name must be a variable name, not a secret value")
	}
	if secret != "" && !envNamePattern.MatchString(secret) {
		return Registration{}, fmt.Errorf("invalid secret_name %q", secret)
	}
	reuse := strings.ToLower(strings.TrimSpace(row.Reuse))
	if looksLikeSecretValue(reuse) {
		return Registration{}, fmt.Errorf("reuse must be a role name, not a secret value")
	}
	responsibility := strings.TrimSpace(row.Responsibility)
	if looksLikeSecretValue(responsibility) {
		return Registration{}, fmt.Errorf("responsibility must not contain a secret value")
	}
	rec := Registration{
		Name:           Role(name),
		Kind:           RoleKindCustom,
		Responsibility: responsibility,
		Credential: CredentialRef{
			Kind:       kind,
			SecretName: secret,
			ReuseOf:    Role(reuse),
		},
		Capabilities: caps,
		Agents:       agents,
	}
	switch kind {
	case CredentialOwn:
		derived := CustomSecretName(rec.Name)
		if rec.Credential.SecretName == "" {
			rec.Credential.SecretName = derived
		} else if rec.Credential.SecretName != derived {
			return Registration{}, fmt.Errorf("secret_name %q must be %q for an own credential", rec.Credential.SecretName, derived)
		}
		rec.Credential.TokenName = CustomTokenName(rec.Name)
		if rec.Credential.ReuseOf != "" {
			return Registration{}, fmt.Errorf("own credential cannot set reuse")
		}
	case CredentialReuse:
		if rec.Credential.ReuseOf == "" {
			return Registration{}, fmt.Errorf("reuse requires a target role")
		}
		rec.Credential.TokenName = ""
	default:
		return Registration{}, fmt.Errorf("invalid credential %q", row.Credential)
	}
	return rec, nil
}

func builtinRegistrations() []Registration {
	return []Registration{
		{
			Name:           RolePoller,
			Kind:           RoleKindBuiltin,
			Responsibility: "Event/issue reads, pipeline dispatch, and poll-state writes",
			Credential: CredentialRef{
				Kind:       CredentialOwn,
				SecretName: forge.SecretGitLabPollerToken,
				TokenName:  PollerTokenName,
			},
			Capabilities: []Capability{
				CapReadIssues,
				CapDispatchPipeline,
				CapWritePollState,
			},
			Agents: []string{"poller"},
		},
		{
			Name:           RoleAnalyst,
			Kind:           RoleKindBuiltin,
			Responsibility: "Review, triage, prioritization, retrospectives, and issue/reporting work",
			Credential: CredentialRef{
				Kind:       CredentialOwn,
				SecretName: forge.SecretGitLabAnalystToken,
				TokenName:  AnalystTokenName,
			},
			Capabilities: []Capability{
				CapReadIssues,
				CapWriteIssues,
				CapWriteNotes,
				CapWriteLabels,
				CapApproveMergeRequest,
			},
			Agents: []string{"analyst", "review", "triage", "prioritize", "retro", "scribe"},
		},
		{
			Name:           RoleCoder,
			Kind:           RoleKindBuiltin,
			Responsibility: "Repository writes, code/fix work, and merge-request updates",
			Credential: CredentialRef{
				Kind:       CredentialOwn,
				SecretName: forge.SecretGitLabCoderToken,
				TokenName:  CoderTokenName,
			},
			Capabilities: []Capability{
				CapReadIssues,
				CapWriteRepository,
				CapWriteMergeRequest,
			},
			Agents: []string{"coder", "code", "fix"},
		},
	}
}

func validRoleName(name string) bool {
	return roleNamePattern.MatchString(name) && !strings.Contains(name, "--")
}

func roleIdentifier(role string) string {
	return strings.ToUpper(strings.ReplaceAll(role, "-", "_"))
}

func looksLikeSecretValue(s string) bool {
	if s == "" {
		return false
	}
	lower := strings.ToLower(s)
	return strings.HasPrefix(lower, "glpat-") ||
		strings.HasPrefix(lower, "glptt-") ||
		strings.HasPrefix(lower, "gldt-")
}

// MarshalCustomRoles serializes administrator-registered custom roles
// as FULLSEND_GITLAB_ROLE_REGISTRY JSON. Built-in roles are omitted;
// an empty custom set is {"roles":[]}, which ParseRegistry treats as
// built-ins only. The document contains credential references and
// policy, never token values.
func MarshalCustomRoles(reg Registry) (string, error) {
	reg = reg.effective()
	file := registryFile{Roles: make([]registryRole, 0)}
	for _, rec := range reg.roles {
		if rec.Kind != RoleKindCustom {
			continue
		}
		row := registryRole{
			Name:           string(rec.Name),
			Responsibility: rec.Responsibility,
			Credential:     string(rec.Credential.Kind),
			Capabilities:   make([]string, len(rec.Capabilities)),
			Agents:         append([]string(nil), rec.Agents...),
		}
		for i, cap := range rec.Capabilities {
			row.Capabilities[i] = string(cap)
		}
		if rec.Credential.Kind == CredentialReuse {
			row.Reuse = string(rec.Credential.ReuseOf)
		} else if rec.Credential.SecretName != "" && rec.Credential.SecretName != CustomSecretName(rec.Name) {
			row.SecretName = rec.Credential.SecretName
		}
		file.Roles = append(file.Roles, row)
	}
	raw, err := json.Marshal(file)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidRegistry, err)
	}
	return string(raw), nil
}
