package harness

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

var (
	validAgentName  = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	validModelName  = regexp.MustCompile(`^[a-zA-Z0-9_.@-]+$`)
	validPluginName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	// validRoleName mirrors mintcore.RolePattern — duplicated to avoid coupling harness→mintcore.
	validRoleName = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	validSlugName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)
	envVarRef     = regexp.MustCompile(`\$\{([^}]+)\}`)
)

// HostFile describes a file on the host that must be copied into the sandbox
// during bootstrap. Src may contain ${VAR} references that are expanded from
// the host environment at bootstrap time. Use this for any file that must
// exist inside the sandbox (e.g. GCP service account JSON, CA certificates).
//
// When Expand is true, the file content is read and ${VAR} references in the
// content are expanded from the host environment before copying to the sandbox.
// Use this for env files that contain variable references which must be resolved
// on the host (because the sandbox does not have those variables set).
type HostFile struct {
	Src      string `yaml:"src"`                // host path (may use ${VAR} expansion)
	Dest     string `yaml:"dest"`               // destination path inside the sandbox
	Expand   bool   `yaml:"expand,omitempty"`   // expand ${VAR} in file content before copying
	Optional bool   `yaml:"optional,omitempty"` // skip if src path is missing or expands to empty
}

// ProviderDef is a declarative definition of an OpenShell provider. Files in
// the experiment's providers/ directory are loaded as ProviderDefs and
// reconciled against the gateway before sandbox creation.
type ProviderDef struct {
	Name        string            `yaml:"name"`
	Type        string            `yaml:"type"`
	Credentials map[string]string `yaml:"credentials"`      // KEY: VALUE or KEY: ${HOST_VAR}
	Config      map[string]string `yaml:"config,omitempty"` // e.g. OPENAI_BASE_URL
}

// LoadProviderDefs reads all YAML files from a providers/ directory and returns
// the parsed definitions. Returns nil (no error) if the directory does not exist.
func LoadProviderDefs(dir string) ([]ProviderDef, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading providers dir: %w", err)
	}

	var defs []ProviderDef
	for _, e := range entries {
		if e.IsDir() || (!strings.HasSuffix(e.Name(), ".yaml") && !strings.HasSuffix(e.Name(), ".yml")) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading provider file %s: %w", e.Name(), err)
		}
		var def ProviderDef
		if err := yaml.Unmarshal(data, &def); err != nil {
			return nil, fmt.Errorf("parsing provider file %s: %w", e.Name(), err)
		}
		if def.Name == "" {
			return nil, fmt.Errorf("provider file %s: name is required", e.Name())
		}
		if def.Type == "" {
			return nil, fmt.Errorf("provider file %s: type is required", e.Name())
		}
		defs = append(defs, def)
	}
	return defs, nil
}

// SecurityConfig configures security scanning for the agent run.
// Secure by default: omitting this block enables all scanners with fail_mode: closed.
type SecurityConfig struct {
	Enabled      *bool             `yaml:"enabled,omitempty"`   // nil = true (secure by default)
	FailMode     string            `yaml:"fail_mode,omitempty"` // "closed" or "open". Default: "closed"
	HostScanners *HostScanners     `yaml:"host_scanners,omitempty"`
	SandboxHooks *SandboxHooks     `yaml:"sandbox_hooks,omitempty"`
	Escalation   *EscalationConfig `yaml:"escalation,omitempty"`
	Trace        *TraceConfig      `yaml:"trace,omitempty"`
}

// HostScanners configures which scanners run on the host before sandbox creation
// (Path A: GHA workflow pre-step) or inside the sandbox before the agent starts
// (Path B: fullsend scan context).
type HostScanners struct {
	UnicodeNormalizer *bool           `yaml:"unicode_normalizer,omitempty"` // default: true
	ContextInjection  *bool           `yaml:"context_injection,omitempty"`  // default: true
	SSRFValidator     *bool           `yaml:"ssrf_validator,omitempty"`     // default: true
	SecretRedactor    *bool           `yaml:"secret_redactor,omitempty"`    // default: true
	LLMGuard          *LLMGuardConfig `yaml:"llm_guard,omitempty"`
}

// LLMGuardConfig configures the LLM Guard ML-based prompt injection scanner.
// Runs in Path A (GHA workflow pre-step) and Path B (sandbox) when the base
// sandbox image includes the pre-installed LLM Guard and DeBERTa-v3 model.
type LLMGuardConfig struct {
	Enabled   *bool   `yaml:"enabled,omitempty"`    // default: true
	Threshold float64 `yaml:"threshold,omitempty"`  // default: 0.92
	MatchType string  `yaml:"match_type,omitempty"` // "sentence" or "full". Default: "sentence"
}

// SandboxHooks configures Claude Code PreToolUse/PostToolUse hooks
// that run inside the sandbox during agent execution.
type SandboxHooks struct {
	Tirith                  *TirithConfig        `yaml:"tirith,omitempty"`
	SSRFPreTool             *bool                `yaml:"ssrf_pretool,omitempty"`              // default: true
	SecretRedactPostTool    *bool                `yaml:"secret_redact_posttool,omitempty"`    // default: true
	UnicodePostTool         *bool                `yaml:"unicode_posttool,omitempty"`          // default: true
	ContextSuppressPostTool *bool                `yaml:"context_suppress_posttool,omitempty"` // default: true
	CanaryPreTool           *bool                `yaml:"canary_pretool,omitempty"`            // default: true
	CanaryPostTool          *bool                `yaml:"canary_posttool,omitempty"`           // default: true
	ToolAllowlistPreTool    *ToolAllowlistConfig `yaml:"tool_allowlist_pretool,omitempty"`
}

// ToolAllowlistConfig configures the tool call allowlist PreToolUse hook.
// Disabled by default — requires FULLSEND_TOOL_ALLOWLIST env var to define
// the allowed tool set per agent role.
type ToolAllowlistConfig struct {
	Enabled *bool `yaml:"enabled,omitempty"` // default: false (opt-in)
}

// TirithConfig configures the Tirith Rust CLI scanner for terminal security.
type TirithConfig struct {
	Enabled *bool  `yaml:"enabled,omitempty"` // default: true
	FailOn  string `yaml:"fail_on,omitempty"` // "critical", "high", "medium". Default: "high"
}

// EscalationConfig controls what happens when critical findings are detected.
type EscalationConfig struct {
	OnCritical  string `yaml:"on_critical,omitempty"`  // "halt" or "review". Default: "halt"
	ReviewLabel string `yaml:"review_label,omitempty"` // Default: "requires-manual-review"
}

// TraceConfig controls trace ID generation for security finding correlation.
type TraceConfig struct {
	Enabled *bool `yaml:"enabled,omitempty"` // default: true
}

// BoolDefault returns the value of a *bool, or the default if nil.
func BoolDefault(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}

// SecurityEnabled returns true if security scanning is enabled (default: true).
func (h *Harness) SecurityEnabled() bool {
	if h.Security == nil {
		return true
	}
	return BoolDefault(h.Security.Enabled, true)
}

// FailModeClosed returns true if the security fail mode is "closed" (default).
func (h *Harness) FailModeClosed() bool {
	if h.Security == nil || h.Security.FailMode == "" || h.Security.FailMode == "closed" {
		return true
	}
	return false
}

// APIServer describes a host-side REST proxy server.
type APIServer struct {
	Name   string            `yaml:"name"`
	Script string            `yaml:"script"`
	Port   int               `yaml:"port"`
	Env    map[string]string `yaml:"env,omitempty"`
}

// ValidationLoop configures a deterministic validation step after the agent exits.
type ValidationLoop struct {
	Script        string `yaml:"script"`
	MaxIterations int    `yaml:"max_iterations"`
	FeedbackMode  string `yaml:"feedback_mode,omitempty"`
}

// Harness is the per-agent configuration that the runner reads to provision
// a sandbox and launch one agent. It follows the ADR-0017 schema.
type Harness struct {
	Agent                  string                  `yaml:"agent"`
	Doc                    string                  `yaml:"doc,omitempty"` // source-repo-only; not resolved at runtime, used by lint-agent-docs
	Description            string                  `yaml:"description,omitempty"`
	Role                   string                  `yaml:"role,omitempty"`
	Slug                   string                  `yaml:"slug,omitempty"`
	Base                   string                  `yaml:"base,omitempty"`
	Image                  string                  `yaml:"image,omitempty"`
	Policy                 string                  `yaml:"policy,omitempty"`
	Skills                 []string                `yaml:"skills,omitempty"`
	Plugins                []string                `yaml:"plugins,omitempty"`
	Providers              []string                `yaml:"providers,omitempty"`
	HostFiles              []HostFile              `yaml:"host_files,omitempty"`
	APIServers             []APIServer             `yaml:"api_servers,omitempty"`
	Model                  string                  `yaml:"model,omitempty"`
	PreScript              string                  `yaml:"pre_script,omitempty"`
	PostScript             string                  `yaml:"post_script,omitempty"`
	AgentInput             string                  `yaml:"agent_input,omitempty"`
	ValidationLoop         *ValidationLoop         `yaml:"validation_loop,omitempty"`
	RunnerEnv              map[string]string       `yaml:"runner_env,omitempty"`
	TimeoutMinutes         int                     `yaml:"timeout_minutes,omitempty"`
	SandboxTimeoutSeconds  int                     `yaml:"sandbox_timeout_seconds,omitempty"`
	Security               *SecurityConfig         `yaml:"security,omitempty"`
	AllowedRemoteResources []string                `yaml:"allowed_remote_resources,omitempty"`
	AllowRuntimeFetch      bool                    `yaml:"allow_runtime_fetch,omitempty"` // opt-in to runtime skill fetching (default: false)
	MaxRuntimeFetches      *int                    `yaml:"max_runtime_fetches,omitempty"` // per-run fetch cap; nil = default (10), valid range 1-1000
	Forge                  map[string]*ForgeConfig `yaml:"forge,omitempty"`
}

// Load reads a harness YAML file from path, unmarshals it, and validates it.
func Load(path string) (*Harness, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading harness file: %w", err)
	}

	var h Harness
	if err := yaml.Unmarshal(data, &h); err != nil {
		return nil, fmt.Errorf("parsing harness YAML: %w", err)
	}

	if err := h.Validate(); err != nil {
		return nil, fmt.Errorf("invalid harness: %w", err)
	}

	return &h, nil
}

// LoadOpts configures forge-aware harness loading.
type LoadOpts struct {
	ForgePlatform string
}

// LoadWithOpts reads a harness YAML file and applies forge resolution before
// validation. The pipeline is: Unmarshal → validateForge → ResolveForge →
// Validate. validateForge runs first to reject malformed forge maps before
// ResolveForge consumes (nils out) the map. When ForgePlatform is empty,
// ResolveForge is a no-op but validateForge still runs.
func LoadWithOpts(path string, opts LoadOpts) (*Harness, error) {
	h, err := LoadRaw(path)
	if err != nil {
		return nil, err
	}

	if err := h.validateForge(); err != nil {
		return nil, fmt.Errorf("invalid harness: %w", err)
	}

	if err := h.ResolveForge(opts.ForgePlatform); err != nil {
		return nil, fmt.Errorf("resolving forge config: %w", err)
	}

	if err := h.Validate(); err != nil {
		return nil, fmt.Errorf("invalid harness: %w", err)
	}

	return h, nil
}

// LoadRaw reads and unmarshals a harness YAML file without calling Validate
// or ResolveForge. Used by base composition to load base harnesses without
// consuming their forge maps before merging, and by the lock command to
// discover forge keys without resolving them.
func LoadRaw(path string) (*Harness, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading harness file: %w", err)
	}

	var h Harness
	if err := yaml.Unmarshal(data, &h); err != nil {
		return nil, fmt.Errorf("parsing harness YAML: %w", err)
	}

	return &h, nil
}

// Validate checks that required fields are present.
func (h *Harness) Validate() error {
	// Base field must be consumed by LoadWithBase before validation.
	// If it's still set, the harness was loaded via Load/LoadWithOpts which
	// don't process base composition — a silent misconfiguration.
	if h.Base != "" {
		return fmt.Errorf("base field is set but harness was not loaded with LoadWithBase; use LoadWithBase to enable base composition")
	}
	if h.Agent == "" {
		return fmt.Errorf("agent field is required")
	}
	// Agent name (filename without .md) must be safe for shell interpolation.
	// Skip name validation when agent is a URL — the resolver will replace it
	// with a local cache path before it reaches the shell.
	if !IsURL(h.Agent) {
		agentBase := strings.TrimSuffix(filepath.Base(h.Agent), ".md")
		if !validAgentName.MatchString(agentBase) {
			return fmt.Errorf("agent name %q contains invalid characters (allowed: a-z, A-Z, 0-9, _, -)", agentBase)
		}
	}
	if h.Model != "" && !validModelName.MatchString(h.Model) {
		return fmt.Errorf("model %q contains invalid characters (allowed: a-z, A-Z, 0-9, _, -, ., @)", h.Model)
	}
	if h.Role != "" {
		if !validRoleName.MatchString(h.Role) {
			return fmt.Errorf("role %q contains invalid characters (allowed: a-z, 0-9, _, -; must start with a lowercase letter)", h.Role)
		}
		if strings.Contains(h.Role, "--") {
			return fmt.Errorf("role %q must not contain double hyphens", h.Role)
		}
	}
	if h.Slug != "" && !validSlugName.MatchString(h.Slug) {
		return fmt.Errorf("slug %q contains invalid characters (allowed: a-z, A-Z, 0-9, _, -; must start with a letter or digit)", h.Slug)
	}
	for i, p := range h.Plugins {
		pluginBase := filepath.Base(p)
		if !validPluginName.MatchString(pluginBase) {
			return fmt.Errorf("plugins[%d] name %q contains invalid characters (allowed: a-z, A-Z, 0-9, _, -)", i, pluginBase)
		}
	}
	if h.TimeoutMinutes < 0 {
		return fmt.Errorf("timeout_minutes must be non-negative, got %d", h.TimeoutMinutes)
	}
	if h.SandboxTimeoutSeconds != 0 && (h.SandboxTimeoutSeconds < 30 || h.SandboxTimeoutSeconds > 600) {
		return fmt.Errorf("sandbox_timeout_seconds must be 0 (default) or between 30 and 600, got %d", h.SandboxTimeoutSeconds)
	}
	for i, hf := range h.HostFiles {
		if hf.Src == "" {
			return fmt.Errorf("host_files[%d]: src is required", i)
		}
		if hf.Dest == "" {
			return fmt.Errorf("host_files[%d]: dest is required", i)
		}
	}
	if h.ValidationLoop != nil && h.ValidationLoop.Script == "" {
		return fmt.Errorf("validation_loop.script is required when validation_loop is set")
	}
	if err := h.validateSecurity(); err != nil {
		return err
	}
	if err := h.ValidateResourceTypes(); err != nil {
		return err
	}
	if h.AllowRuntimeFetch && len(h.AllowedRemoteResources) == 0 {
		return fmt.Errorf("allow_runtime_fetch requires at least one entry in allowed_remote_resources")
	}
	if h.MaxRuntimeFetches != nil {
		if !h.AllowRuntimeFetch {
			return fmt.Errorf("max_runtime_fetches requires allow_runtime_fetch to be true")
		}
		if *h.MaxRuntimeFetches <= 0 || *h.MaxRuntimeFetches > 1000 {
			return fmt.Errorf("max_runtime_fetches must be between 1 and 1000, got %d", *h.MaxRuntimeFetches)
		}
	}
	if err := h.validateForge(); err != nil {
		return err
	}
	// ValidateAllowedRemoteResources requires the org allowlist and is called
	// by the integration layer, not here.
	return nil
}

// validateSecurity checks that security config fields use valid values.
func (h *Harness) validateSecurity() error {
	if h.Security == nil {
		return nil
	}
	s := h.Security

	switch s.FailMode {
	case "", "closed", "open":
	default:
		return fmt.Errorf("security.fail_mode must be \"closed\" or \"open\", got %q", s.FailMode)
	}

	if s.HostScanners != nil && s.HostScanners.LLMGuard != nil {
		lg := s.HostScanners.LLMGuard
		if lg.Threshold != 0 && (lg.Threshold < 0 || lg.Threshold > 1) {
			return fmt.Errorf("security.host_scanners.llm_guard.threshold must be between 0 and 1, got %v", lg.Threshold)
		}
		switch lg.MatchType {
		case "", "sentence", "full":
		default:
			return fmt.Errorf("security.host_scanners.llm_guard.match_type must be \"sentence\" or \"full\", got %q", lg.MatchType)
		}
	}

	if s.SandboxHooks != nil && s.SandboxHooks.Tirith != nil {
		switch s.SandboxHooks.Tirith.FailOn {
		case "", "critical", "high", "medium":
		default:
			return fmt.Errorf("security.sandbox_hooks.tirith.fail_on must be \"critical\", \"high\", or \"medium\", got %q", s.SandboxHooks.Tirith.FailOn)
		}
	}

	if s.Escalation != nil {
		switch s.Escalation.OnCritical {
		case "", "halt", "review":
		default:
			return fmt.Errorf("security.escalation.on_critical must be \"halt\" or \"review\", got %q", s.Escalation.OnCritical)
		}
	}

	return nil
}

// ResolveRelativeTo resolves all relative paths in the harness against baseDir.
// Relative paths that resolve outside baseDir are rejected to prevent directory
// traversal (e.g. ../../etc/shadow). Absolute paths and ${VAR} paths are allowed.
func (h *Harness) ResolveRelativeTo(baseDir string) error {
	cleanBase := filepath.Clean(baseDir) + string(filepath.Separator)

	resolve := func(field, p string) (string, error) {
		if p == "" || filepath.IsAbs(p) || IsURL(p) {
			return p, nil
		}
		resolved := filepath.Join(baseDir, p)
		if !strings.HasPrefix(filepath.Clean(resolved), cleanBase) {
			return "", fmt.Errorf("%s: path %q resolves outside fullsend directory", field, p)
		}
		return resolved, nil
	}

	var err error
	if h.Agent, err = resolve("agent", h.Agent); err != nil {
		return err
	}
	if h.Policy, err = resolve("policy", h.Policy); err != nil {
		return err
	}
	if h.PreScript, err = resolve("pre_script", h.PreScript); err != nil {
		return err
	}
	if h.PostScript, err = resolve("post_script", h.PostScript); err != nil {
		return err
	}
	if h.AgentInput, err = resolve("agent_input", h.AgentInput); err != nil {
		return err
	}

	for i := range h.Skills {
		if h.Skills[i], err = resolve(fmt.Sprintf("skills[%d]", i), h.Skills[i]); err != nil {
			return err
		}
	}
	for i := range h.Plugins {
		if h.Plugins[i], err = resolve(fmt.Sprintf("plugins[%d]", i), h.Plugins[i]); err != nil {
			return err
		}
	}
	for i, hf := range h.HostFiles {
		if !strings.Contains(hf.Src, "${") {
			if h.HostFiles[i].Src, err = resolve(fmt.Sprintf("host_files[%d].src", i), hf.Src); err != nil {
				return err
			}
		}
	}
	for i := range h.APIServers {
		if h.APIServers[i].Script, err = resolve(fmt.Sprintf("api_servers[%d].script", i), h.APIServers[i].Script); err != nil {
			return err
		}
	}
	if h.ValidationLoop != nil {
		if h.ValidationLoop.Script, err = resolve("validation_loop.script", h.ValidationLoop.Script); err != nil {
			return err
		}
	}
	return nil
}

// ValidateRunnerEnvWith checks that all ${VAR} references in RunnerEnv and
// HostFiles.Src are defined in the host environment using the provided lookup
// function. Variables set to an empty string are allowed; only truly unset
// variables produce an error.
func (h *Harness) ValidateRunnerEnvWith(lookup func(string) (string, bool)) error {
	checkVarRefs := func(source, value string) error {
		for _, match := range envVarRef.FindAllStringSubmatch(value, -1) {
			varName := match[1]
			if _, ok := lookup(varName); !ok {
				return fmt.Errorf("%s: host variable %s is not set (referenced in %q)", source, varName, value)
			}
		}
		return nil
	}

	for k, v := range h.RunnerEnv {
		if err := checkVarRefs(fmt.Sprintf("runner_env[%s]", k), v); err != nil {
			return err
		}
	}
	for i, hf := range h.HostFiles {
		if hf.Optional {
			continue
		}
		if err := checkVarRefs(fmt.Sprintf("host_files[%d].src", i), hf.Src); err != nil {
			return err
		}
	}
	return nil
}

// ValidateRunnerEnv checks that all ${VAR} references in RunnerEnv and
// HostFiles.Src are defined in the host environment.
func (h *Harness) ValidateRunnerEnv() error {
	return h.ValidateRunnerEnvWith(os.LookupEnv)
}

// ValidateFilesExist checks that all file paths referenced by the harness
// exist on disk. Callers must invoke ResolveRelativeTo first (to make
// paths absolute), then resolve.ResolveHarness (to replace any URL
// references with local cache paths). The IsURL guard inside is
// defense-in-depth in case the ordering is violated.
func (h *Harness) ValidateFilesExist() error {
	check := func(label, path string) error {
		if path == "" || IsURL(path) {
			return nil
		}
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		return nil
	}

	if err := check("agent", h.Agent); err != nil {
		return err
	}
	if err := check("policy", h.Policy); err != nil {
		return err
	}
	if err := check("pre_script", h.PreScript); err != nil {
		return err
	}
	if err := check("post_script", h.PostScript); err != nil {
		return err
	}
	if err := check("agent_input", h.AgentInput); err != nil {
		return err
	}
	for i, s := range h.Skills {
		if err := check(fmt.Sprintf("skills[%d]", i), s); err != nil {
			return err
		}
	}
	for i, p := range h.Plugins {
		if err := check(fmt.Sprintf("plugins[%d]", i), p); err != nil {
			return err
		}
	}
	for i, hf := range h.HostFiles {
		// Skip ${VAR} paths — they are expanded at bootstrap time.
		if strings.Contains(hf.Src, "${") {
			continue
		}
		// Skip optional host files — they may not exist until runtime
		// (e.g., files created by the pre-script).
		if hf.Optional {
			continue
		}
		if err := check(fmt.Sprintf("host_files[%d].src", i), hf.Src); err != nil {
			return err
		}
	}
	if h.ValidationLoop != nil {
		if err := check("validation_loop.script", h.ValidationLoop.Script); err != nil {
			return err
		}
	}
	return nil
}

// Scripts returns all script paths configured in the harness.
func (h *Harness) Scripts() []string {
	var scripts []string
	if h.PreScript != "" {
		scripts = append(scripts, h.PreScript)
	}
	if h.PostScript != "" {
		scripts = append(scripts, h.PostScript)
	}
	if h.ValidationLoop != nil && h.ValidationLoop.Script != "" {
		scripts = append(scripts, h.ValidationLoop.Script)
	}
	return scripts
}

// ValidateAllowedRemoteResources checks that each entry in AllowedRemoteResources
// is a valid HTTPS URL ending with "/" and is covered by at least one entry in the
// org-level allowlist. Org allowlist entries are also validated: each must be a
// valid HTTPS URL ending with "/" and must not contain double-encoded sequences.
func (h *Harness) ValidateAllowedRemoteResources(orgAllowlist []string) error {
	for i, orgEntry := range orgAllowlist {
		if !IsURL(orgEntry) {
			return fmt.Errorf("org allowlist[%d]: %q is not a valid HTTPS URL", i, orgEntry)
		}
		if !strings.HasSuffix(orgEntry, "/") {
			return fmt.Errorf("org allowlist[%d]: %q must end with /", i, orgEntry)
		}
		if strings.Contains(strings.ToLower(orgEntry), "%25") {
			return fmt.Errorf("org allowlist[%d]: %q contains double-encoded sequence", i, orgEntry)
		}
	}
	for i, entry := range h.AllowedRemoteResources {
		if !IsURL(entry) {
			return fmt.Errorf("allowed_remote_resources[%d]: %q is not a valid HTTPS URL", i, entry)
		}
		if !strings.HasSuffix(entry, "/") {
			return fmt.Errorf("allowed_remote_resources[%d]: %q must end with /", i, entry)
		}
		if strings.Contains(strings.ToLower(entry), "%25") {
			return fmt.Errorf("allowed_remote_resources[%d]: %q contains double-encoded sequence", i, entry)
		}
		normEntry, entryOK := normalizeURLPath(strings.ToLower(entry))
		if !entryOK {
			return fmt.Errorf("allowed_remote_resources[%d]: %q cannot be normalized", i, entry)
		}
		covered := false
		for _, orgEntry := range orgAllowlist {
			normOrg, orgOK := normalizeURLPath(strings.ToLower(orgEntry))
			if !orgOK {
				continue
			}
			if strings.HasPrefix(normEntry, normOrg) {
				covered = true
				break
			}
		}
		if !covered {
			return fmt.Errorf("allowed_remote_resources[%d]: %q is not covered by the org allowlist", i, entry)
		}
	}
	return nil
}

// ValidateResourceTypes checks that executable fields (pre_script, post_script,
// validation_loop.script, host_files[].src, api_servers[].script) are local paths
// and not URLs, and that declarative fields (agent, policy, skills[]) that are URLs
// include an integrity hash (#sha256=...).
func (h *Harness) ValidateResourceTypes() error {
	// Executable fields must be local paths, not URLs.
	execFields := []struct {
		name  string
		value string
	}{
		{"pre_script", h.PreScript},
		{"post_script", h.PostScript},
		{"agent_input", h.AgentInput},
	}
	for _, f := range execFields {
		if f.value != "" && IsURL(f.value) {
			return fmt.Errorf("%s must be a local path, not a URL", f.name)
		}
	}
	if h.ValidationLoop != nil && h.ValidationLoop.Script != "" && IsURL(h.ValidationLoop.Script) {
		return fmt.Errorf("validation_loop.script must be a local path, not a URL")
	}
	for i, hf := range h.HostFiles {
		if IsURL(hf.Src) {
			return fmt.Errorf("host_files[%d].src must be a local path, not a URL", i)
		}
	}
	for i, as := range h.APIServers {
		if IsURL(as.Script) {
			return fmt.Errorf("api_servers[%d].script must be a local path, not a URL", i)
		}
	}

	// Declarative fields: if a URL, must include integrity hash.
	// Check URL fields require integrity hashes.
	// Note: "base" is included as defense-in-depth. In normal flow, Validate()
	// rejects non-empty base before reaching here, and LoadWithBase clears base
	// before calling Validate. This check catches edge cases where
	// ValidateResourceTypes is called standalone.
	declFields := []struct {
		name  string
		value string
	}{
		{"agent", h.Agent},
		{"policy", h.Policy},
		{"base", h.Base},
	}
	for _, f := range declFields {
		if f.value != "" && IsURL(f.value) {
			if _, _, hasHash := ParseIntegrityHash(f.value); !hasHash {
				return fmt.Errorf("%s URL must include #sha256=... integrity hash", f.name)
			}
		}
	}
	for i, s := range h.Skills {
		if IsURL(s) {
			if _, _, hasHash := ParseIntegrityHash(s); !hasHash {
				return fmt.Errorf("skills[%d] URL must include #sha256=... integrity hash", i)
			}
			cleanURL, _, _ := ParseIntegrityHash(s)
			if _, err := forge.ParseForgeURL(cleanURL); err != nil {
				return fmt.Errorf("skills[%d] URL must be hosted on a supported forge (github.com): %w", i, err)
			}
		}
	}

	return nil
}

const defaultMaxRuntimeFetches = 10 // must match fetchsvc.DefaultMaxFetches

// EffectiveMaxRuntimeFetches returns the configured max runtime fetches,
// or defaultMaxRuntimeFetches (10) when the field is omitted.
func (h *Harness) EffectiveMaxRuntimeFetches() int {
	if h.MaxRuntimeFetches == nil {
		return defaultMaxRuntimeFetches
	}
	return *h.MaxRuntimeFetches
}

// HasURLSkills reports whether any skill field contains a URL. Used to determine
// whether a forge client is needed for resolution.
func (h *Harness) HasURLSkills() bool {
	for _, s := range h.Skills {
		if IsURL(s) {
			return true
		}
	}
	return false
}

// HasURLReferences reports whether any declarative field (agent, policy, skills)
// contains a URL. Used to skip remote resource validation and resolution when
// the harness references only local paths.
func (h *Harness) HasURLReferences() bool {
	if IsURL(h.Agent) || IsURL(h.Policy) {
		return true
	}
	for _, s := range h.Skills {
		if IsURL(s) {
			return true
		}
	}
	return false
}

// MatchesAllowedPrefix reports whether rawURL starts with any entry in
// AllowedRemoteResources (case-insensitive). The URL path is percent-decoded
// and normalized (resolving ".." and "." segments) before prefix matching to
// prevent path-traversal bypasses via both literal and encoded dot segments.
// Returns false if the URL contains "%25" (double-encoded percent sign) or
// cannot be parsed.
func (h *Harness) MatchesAllowedPrefix(rawURL string) bool {
	return h.MatchingAllowedPrefix(rawURL) != ""
}

// MatchingAllowedPrefix returns the first AllowedRemoteResources entry that
// matches rawURL, or "" if none match. It applies the same normalization as
// MatchesAllowedPrefix.
func (h *Harness) MatchingAllowedPrefix(rawURL string) string {
	lower := strings.ToLower(rawURL)
	if strings.Contains(lower, "%25") {
		return ""
	}
	normalized, ok := normalizeURLPath(lower)
	if !ok {
		return ""
	}
	for _, prefix := range h.AllowedRemoteResources {
		normPrefix, prefixOK := normalizeURLPath(strings.ToLower(prefix))
		if !prefixOK {
			continue
		}
		if strings.HasPrefix(normalized, normPrefix) {
			return prefix
		}
	}
	return ""
}

// MatchingAllowedPrefixInList checks if a URL matches any prefix in the given allowlist.
// Returns the matching prefix or "" if none match. Standalone version of MatchingAllowedPrefix.
func MatchingAllowedPrefixInList(rawURL string, allowlist []string) string {
	lower := strings.ToLower(rawURL)
	if strings.Contains(lower, "%25") {
		return ""
	}
	normalized, ok := normalizeURLPath(lower)
	if !ok {
		return ""
	}
	for _, prefix := range allowlist {
		normPrefix, prefixOK := normalizeURLPath(strings.ToLower(prefix))
		if !prefixOK {
			continue
		}
		if strings.HasPrefix(normalized, normPrefix) {
			return prefix
		}
	}
	return ""
}

// normalizeURLPath parses a URL, percent-decodes and cleans its path, and
// returns the reconstructed URL string. Returns false if parsing fails.
func normalizeURLPath(rawURL string) (string, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", false
	}
	unescaped, err := url.PathUnescape(parsed.Path)
	if err != nil {
		return "", false
	}
	if strings.ContainsRune(unescaped, '\\') {
		return "", false
	}
	rawPath := parsed.Path
	parsed.Path = path.Clean(unescaped)
	parsed.RawPath = ""
	if parsed.Path == "." {
		parsed.Path = "/"
	} else if strings.HasSuffix(rawPath, "/") && !strings.HasSuffix(parsed.Path, "/") {
		parsed.Path += "/"
	}
	return parsed.String(), true
}
