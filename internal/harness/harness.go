package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	validAgentName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	validModelName = regexp.MustCompile(`^[a-zA-Z0-9_.@-]+$`)
	envVarRef      = regexp.MustCompile(`\$\{([^}]+)\}`)
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
	Src      string `yaml:"src"`              // host path (may use ${VAR} expansion)
	Dest     string `yaml:"dest"`             // destination path inside the sandbox
	Expand   bool   `yaml:"expand,omitempty"` // expand ${VAR} in file content before copying
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
	Enabled      *bool            `yaml:"enabled,omitempty"`       // nil = true (secure by default)
	FailMode     string           `yaml:"fail_mode,omitempty"`     // "closed" or "open". Default: "closed"
	HostScanners *HostScanners    `yaml:"host_scanners,omitempty"`
	SandboxHooks *SandboxHooks    `yaml:"sandbox_hooks,omitempty"`
	Escalation   *EscalationConfig `yaml:"escalation,omitempty"`
	Trace        *TraceConfig     `yaml:"trace,omitempty"`
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
	CanaryPreTool           *bool                `yaml:"canary_pretool,omitempty"`              // default: true
	CanaryPostTool          *bool                `yaml:"canary_posttool,omitempty"`              // default: true
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
	Agent          string            `yaml:"agent"`
	Description    string            `yaml:"description,omitempty"`
	Image          string            `yaml:"image,omitempty"`
	Policy         string            `yaml:"policy,omitempty"`
	Skills         []string          `yaml:"skills,omitempty"`
	Providers      []string          `yaml:"providers,omitempty"`
	HostFiles      []HostFile        `yaml:"host_files,omitempty"`
	APIServers     []APIServer       `yaml:"api_servers,omitempty"`
	Model          string            `yaml:"model,omitempty"`
	PreScript      string            `yaml:"pre_script,omitempty"`
	PostScript     string            `yaml:"post_script,omitempty"`
	AgentInput     string            `yaml:"agent_input,omitempty"`
	ValidationLoop *ValidationLoop   `yaml:"validation_loop,omitempty"`
	RunnerEnv      map[string]string `yaml:"runner_env,omitempty"`
	TimeoutMinutes int               `yaml:"timeout_minutes,omitempty"`
	Security       *SecurityConfig   `yaml:"security,omitempty"`
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

// Validate checks that required fields are present.
func (h *Harness) Validate() error {
	if h.Agent == "" {
		return fmt.Errorf("agent field is required")
	}
	// Agent name (filename without .md) must be safe for shell interpolation.
	agentBase := strings.TrimSuffix(filepath.Base(h.Agent), ".md")
	if !validAgentName.MatchString(agentBase) {
		return fmt.Errorf("agent name %q contains invalid characters (allowed: a-z, A-Z, 0-9, _, -)", agentBase)
	}
	if h.Model != "" && !validModelName.MatchString(h.Model) {
		return fmt.Errorf("model %q contains invalid characters (allowed: a-z, A-Z, 0-9, _, -, ., @)", h.Model)
	}
	if h.TimeoutMinutes < 0 {
		return fmt.Errorf("timeout_minutes must be non-negative, got %d", h.TimeoutMinutes)
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
		if p == "" || filepath.IsAbs(p) {
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
// HostFiles.Src expand to non-empty values using the provided expander function.
func (h *Harness) ValidateRunnerEnvWith(expander func(string) string) error {
	checkVarRefs := func(source, value string) error {
		for _, match := range envVarRef.FindAllStringSubmatch(value, -1) {
			varName := match[1]
			if expander(varName) == "" {
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
// HostFiles.Src expand to non-empty values in the host environment.
func (h *Harness) ValidateRunnerEnv() error {
	return h.ValidateRunnerEnvWith(os.Getenv)
}

// ValidateFilesExist checks that all file paths referenced by the harness
// exist on disk. Call after ResolveRelativeTo so paths are absolute.
// Pre/post scripts run on the host and must be file paths (no inline args).
func (h *Harness) ValidateFilesExist() error {
	check := func(label, path string) error {
		if path == "" {
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
	for i, hf := range h.HostFiles {
		// Skip ${VAR} paths — they are expanded at bootstrap time.
		if strings.Contains(hf.Src, "${") {
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
