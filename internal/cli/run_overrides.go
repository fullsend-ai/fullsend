package cli

import (
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
)

// Runtime-neutral override environment variables. They are overrides of the
// same values the config file and the harness carry; the CLI resolves them
// once (flag > env > config/harness > default), prints the source, records it
// in metrics.json, and hands the result to the runtime — runtimes never read
// these themselves (#6526).
const (
	envRuntime        = "FULLSEND_RUNTIME"
	envModel          = "FULLSEND_MODEL"
	envEffort         = "FULLSEND_EFFORT"
	envFallbackModels = "FULLSEND_FALLBACK_MODELS"
	// envPiModel is the pre-#6526 pi-only model override, kept as an alias of
	// FULLSEND_MODEL for pi runs. FULLSEND_PI_PROVIDER stays pi-only (it is
	// the provider prefix for bare ids, not a model choice).
	envPiModel = "FULLSEND_PI_MODEL"
	// envCodexModel is the same idea for codex (#6920). Codex serves OpenAI
	// models only and needs one named (the runtime enforces which ids it
	// accepts), while fleet harnesses say `model: opus` — so a repo moving
	// to codex needs one place to name a model for every agent without
	// editing each harness.
	envCodexModel = "FULLSEND_CODEX_MODEL"

	sourceFlagRuntime = "--runtime flag"
	sourceFlagModel   = "--model flag"
	sourceFlagEffort  = "--effort flag"
	sourceHarness     = "harness"
	sourceDefault     = "default"
)

// runOverrideFlags are the per-run override flags of `fullsend run`.
type runOverrideFlags struct {
	runtime string
	model   string
	effort  string
}

// runOverrides is the resolved per-run override set. Empty *Source fields
// mean "not overridden" — the config file (runtime) or the composed harness
// (model, effort) stays in charge.
type runOverrides struct {
	runtime       string
	runtimeSource string

	model       string
	modelSource string

	effort       string
	effortSource string

	fallbackModels []string
	fallbackSource string
}

// runtimeModelEnv returns the runtime-scoped model override variable for a
// runtime, or "" when it has none. Only runtimes that actually need one are
// listed: a knob is user-visible surface, and inventing FULLSEND_<NAME>_MODEL
// for every runtime would both document variables nobody asked for and
// produce an unusable name for dummy-playback (a hyphen cannot appear in an
// environment variable name). FULLSEND_MODEL is the runtime-neutral override
// and outranks all of these.
func runtimeModelEnv(runtimeName string) string {
	switch runtimeName {
	case "pi":
		return envPiModel
	case "codex":
		return envCodexModel
	}
	return ""
}

// resolveRunOverrides applies the precedence flag > env for each override.
// runtimeName is the runtime that will run (after the runtime override, if
// any) and gates only the runtime-scoped model aliases (runtimeModelEnv).
func resolveRunOverrides(flags runOverrideFlags, getenv func(string) string, runtimeName string) (runOverrides, error) {
	var o runOverrides
	env := func(name string) string { return strings.TrimSpace(getenv(name)) }

	switch {
	case strings.TrimSpace(flags.runtime) != "":
		o.runtime, o.runtimeSource = strings.TrimSpace(flags.runtime), sourceFlagRuntime
	case env(envRuntime) != "":
		o.runtime, o.runtimeSource = env(envRuntime), envRuntime
	}
	if o.runtime != "" {
		if err := validateRuntimeName(o.runtime); err != nil {
			return runOverrides{}, fmt.Errorf("%s: %w", o.runtimeSource, err)
		}
		runtimeName = o.runtime
	}

	runtimeEnv := runtimeModelEnv(runtimeName)
	switch {
	case strings.TrimSpace(flags.model) != "":
		o.model, o.modelSource = strings.TrimSpace(flags.model), sourceFlagModel
	case env(envModel) != "":
		o.model, o.modelSource = env(envModel), envModel
	case runtimeEnv != "" && env(runtimeEnv) != "":
		o.model, o.modelSource = env(runtimeEnv), runtimeEnv
	}

	switch {
	case strings.TrimSpace(flags.effort) != "":
		o.effort, o.effortSource = strings.TrimSpace(flags.effort), sourceFlagEffort
	case env(envEffort) != "":
		o.effort, o.effortSource = env(envEffort), envEffort
	}

	if v := env(envFallbackModels); v != "" {
		for _, m := range strings.Split(v, ",") {
			if m = strings.TrimSpace(m); m != "" {
				o.fallbackModels = append(o.fallbackModels, m)
			}
		}
		if len(o.fallbackModels) > 0 {
			o.fallbackSource = envFallbackModels
		}
	}
	return o, nil
}

// validateRuntimeName mirrors the config validation so a flag/env override
// cannot select a runtime the config file could not.
func validateRuntimeName(name string) error {
	for _, v := range config.ValidRuntimes() {
		if name == v {
			return nil
		}
	}
	return fmt.Errorf("invalid runtime %q: must be one of %s", name, strings.Join(config.ValidRuntimes(), ", "))
}

// resolveBackend returns the runtime backend for the run and a human-readable
// source: the override (flag/env) when set, else the config file path (or the
// built-in default when no config exists). When agentName is non-empty and
// no flag/env override is set, the agents: entry's runtime from the
// config file takes precedence over the repo-wide runtime: key.
func resolveBackend(o runOverrides, configPath, agentName string) (agentruntime.Backend, string, error) {
	rc, err := loadRunConfig(configPath)
	if err != nil {
		return agentruntime.Backend{}, rc.source, err
	}
	return resolveBackendFrom(o, rc, agentName)
}

// resolveBackendFrom is resolveBackend over an already loaded config, so a
// run loads config.yaml once for runtime selection and agent settings.
func resolveBackendFrom(o runOverrides, rc runConfig, agentName string) (agentruntime.Backend, string, error) {
	if o.runtime == "" {
		return rc.backend(agentName)
	}
	backend, err := agentruntime.Resolve(o.runtime)
	if err != nil {
		return agentruntime.Backend{}, o.runtimeSource, fmt.Errorf("%w: %w", errResolvingRuntime, err)
	}
	return backend, o.runtimeSource, nil
}

// modelOverrideSource is the metrics.json override_source value for the
// effective model: the override source when one applied, "harness" when the
// composed harness/agent supplied the model, "default" otherwise.
func modelOverrideSource(o runOverrides, effectiveModel string) string {
	if o.modelSource != "" {
		return o.modelSource
	}
	if effectiveModel != "" {
		return sourceHarness
	}
	return sourceDefault
}

// aliasOverrideSource appends the models.aliases remap to an
// override_source value when the effective model was an alias with a
// per-repo entry (#6882): "<base>, remapped by <config path>
// models.aliases". base is never empty — modelOverrideSource returns
// "harness" or "default" when no override applied — so the remap is
// always a suffix, never the whole value.
func aliasOverrideSource(base string, remapped bool, configSource string) string {
	if !remapped {
		return base
	}
	return base + ", remapped by " + configSource + " models.aliases"
}
