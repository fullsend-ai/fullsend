package runtime

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// claudeAliasDefaults are the model ids the Claude aliases run on when the
// repo maps none of them under models.aliases in .fullsend/config.yaml
// (#8426). Both Claude-family runtimes use it: Claude Code is given the id
// as --model instead of the bare alias, so the default does not move with
// the pinned CLAUDE_CODE_VERSION's own alias table, and pi's piModelAliases
// carries the same ids. fable is not listed: it stays on Claude Code's own
// mapping, and pi maps it in piModelAliases.
var claudeAliasDefaults = map[string]string{
	"opus":   "claude-opus-5-5",
	"sonnet": "claude-sonnet-5-5",
	"haiku":  "claude-haiku-5-5",
}

// claudeExplicitChainAliases are aliases with no built-in chain that still
// take an explicit FULLSEND_FALLBACK_MODELS list on Claude Code. The id is
// the one pi resolves the alias to, so the attempt names a concrete model
// that Claude Code's "model not available" message can be matched against.
var claudeExplicitChainAliases = map[string]string{
	"fable": "claude-fable-5-1",
}

// claudeAliasFallbacks is the built-in fallback chain per alias, tried in
// order when the default id is not served in the run's Vertex project
// (isVertexModelUnavailable, and on Claude Code also
// claudeSelectedModelUnavailable) and the run sets no FULLSEND_FALLBACK_MODELS.
// Each entry is the previous default, so a project that has not enabled
// the 5.5 models keeps running on what it ran on before the bump.
var claudeAliasFallbacks = map[string][]string{
	"opus":   {"claude-opus-4-6"},
	"sonnet": {"claude-sonnet-4-6"},
	"haiku":  {"claude-haiku-4-5"},
}

// ImplicitFallbackModels returns the built-in fallback chain for model, or
// nil when there is none. Only a bare alias that resolves to its built-in
// default has one: a pinned id or provider/id spec fails loudly when it is
// not served (#7026), and an alias the repo retargets with models.aliases
// keeps the repo's choice with no implicit chain behind it. The returned
// slice is a copy.
func ImplicitFallbackModels(model string, configAliases map[string]string) []string {
	model = strings.TrimSpace(model)
	if _, retargeted := configAliases[model]; retargeted {
		return nil
	}
	return slices.Clone(claudeAliasFallbacks[model])
}

// BuiltinAliasModel returns the model id runtimeName runs model on through
// its built-in alias table, or "" when it has none: model is not a bare
// alias that runtime maps, or the repo retargets it with models.aliases
// (the plan block shows that remap itself). Claude Code maps the aliases in
// claudeAliasDefaults (fable stays on Claude Code's own table); pi maps
// those in piModelAliases.
func BuiltinAliasModel(runtimeName, model string, configAliases map[string]string) string {
	model = strings.TrimSpace(model)
	if _, retargeted := configAliases[model]; retargeted {
		return ""
	}
	switch runtimeName {
	case "claude":
		return claudeAliasDefaults[model]
	case "pi":
		return piModelAliases[model]
	}
	return ""
}

// fallbackReasonNotServed is the RunMetrics.FallbackReasons entry for a
// model the runner left because the run's Vertex project does not serve it
// (claudeShouldFallBack, piShouldFallBack).
const fallbackReasonNotServed = "not_served"

// claudeCodeFallbackReasonPrefix starts the RunMetrics.FallbackReasons
// entry for a switch Claude Code made itself within the session
// (ModelFallbackEvent); its trigger follows.
const claudeCodeFallbackReasonPrefix = "claude_code"

// claudeCodeFallbackReason is the RunMetrics.FallbackReasons entry for a
// ModelFallbackEvent: "claude_code:<trigger>", or "claude_code" when Claude
// Code names no trigger.
func claudeCodeFallbackReason(trigger string) string {
	if t := strings.TrimSpace(trigger); t != "" {
		return claudeCodeFallbackReasonPrefix + ":" + t
	}
	return claudeCodeFallbackReasonPrefix
}

// fallbackReasonText is reason (a RunMetrics.FallbackReasons entry) as the
// run log prints it.
func fallbackReasonText(reason string) string {
	switch {
	case reason == fallbackReasonNotServed:
		return "not served in this project"
	case reason == claudeCodeFallbackReasonPrefix:
		return "Claude Code"
	case strings.HasPrefix(reason, claudeCodeFallbackReasonPrefix+":"):
		return "Claude Code: " + strings.TrimPrefix(reason, claudeCodeFallbackReasonPrefix+":")
	}
	return reason
}

// modelFallbackWarning is the one warning line for every model fallback,
// whether the runner retries on the next model or Claude Code switches
// within the session. Model names and the reason come from the sandbox or
// the stream, so each is sanitized.
func modelFallbackWarning(from, to, reason string) string {
	return fmt.Sprintf("model %s is not available (%s); falling back to %s",
		sanitizeOutput(from), sanitizeOutput(fallbackReasonText(reason)), sanitizeOutput(to))
}

// recordFallback adds from to FallbackFrom with its reason, keeping
// FallbackReasons aligned with it; a model already listed keeps its first
// entry.
func (m *RunMetrics) recordFallback(from, reason string) {
	if from == "" || slices.Contains(m.FallbackFrom, from) {
		return
	}
	m.FallbackFrom = append(m.FallbackFrom, from)
	m.FallbackReasons = append(m.FallbackReasons, reason)
}

// effectiveFallbackModels is the fallback chain a run uses for model: the
// explicit FULLSEND_FALLBACK_MODELS list when the run sets one, else the
// built-in chain for model (ImplicitFallbackModels).
func effectiveFallbackModels(model string, explicit []string, configAliases map[string]string) []string {
	if len(explicit) > 0 {
		return explicit
	}
	return ImplicitFallbackModels(model, configAliases)
}

// modelFallbackLoop runs chain in order until an attempt is final and
// returns that attempt and its index in chain. All attempts share one
// deadline of timeout from the start, so a chain never runs longer than a
// single-model run could; a fallback is not started with less than
// minAttempt left. shouldFallBack decides whether an attempt was abandoned
// only because its model is not served. onFallback runs between an
// abandoned attempt and the next, with a budget of at least a second that
// leaves the next attempt at least minAttempt; when it reports false the
// abandoned attempt is final instead.
func modelFallbackLoop[T any](chain []string, timeout, minAttempt time.Duration, now func() time.Time,
	attempt func(model string, timeout time.Duration, mayFallBack bool) T,
	shouldFallBack func(T) bool,
	onFallback func(prev, next string, budget time.Duration) bool,
) (T, int) {
	deadline := now().Add(timeout)
	var result T
	served := 0
	for i, model := range chain {
		remaining := timeout
		if i > 0 {
			remaining = deadline.Sub(now())
		}
		last := i == len(chain)-1
		result, served = attempt(model, remaining, !last), i
		if last || !shouldFallBack(result) {
			break
		}
		budget := deadline.Sub(now()) - minAttempt
		if budget < time.Second || !onFallback(model, chain[i+1], budget) || deadline.Sub(now()) < minAttempt {
			break
		}
	}
	return result, served
}

// minFallbackAttemptTimeout is the least budget a fallback attempt is
// started with (modelFallbackLoop's minAttempt on every runtime); openshell
// takes --timeout in whole seconds.
const minFallbackAttemptTimeout = time.Second

// attemptGate forwards an attempt's events to next, except that while
// hold is set it withholds them until the model produces output. A Vertex
// "model not served" answer carries no output, so an attempt abandoned for
// a fallback leaves no ErrorEvent, token line or retry line behind on a run
// that then succeeds; the first output event flushes what was held, in
// order, and passes everything after it straight through.
//
// output, when set, decides which events count as model output; nil means
// any text, thinking or tool event (attemptOutput).
type attemptGate struct {
	next     func(AgentEvent)
	hold     bool
	held     []AgentEvent
	answered bool
	output   func(AgentEvent) bool
}

// attemptOutput reports whether evt is output only a served model
// produces.
func attemptOutput(evt AgentEvent) bool {
	switch evt.(type) {
	case TextEvent, ThinkingEvent, ToolUseEvent, ToolResultEvent:
		return true
	}
	return false
}

func (g *attemptGate) handle(evt AgentEvent) {
	output := g.output
	if output == nil {
		output = attemptOutput
	}
	if output(evt) {
		if !g.answered {
			g.answered = true
			for _, h := range g.held {
				g.next(h)
			}
			g.held = nil
		}
	}
	if g.hold && !g.answered {
		g.held = append(g.held, evt)
		return
	}
	g.next(evt)
}
