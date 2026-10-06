package repos

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Effective workflow:rules admission for the webhook fast-path.
//
// GitLab evaluates workflow:rules in order and the first matching rule
// decides: when: never rejects the pipeline, any other when admits it,
// and no match rejects it. Readiness therefore evaluates the committed
// rules against the pipeline the webhook starts — a trigger pipeline on
// the protected default branch — rather than looking for one exact rule.
// The evaluation is three-valued: a condition that depends on anything
// unknown (a project variable, an unsupported keyword) cannot be
// established, and readiness defers rather than guessing.

// triadState is a three-valued truth value.
type triadState int

const (
	triadFalse triadState = iota
	triadTrue
	triadUnknown
)

func (a triadState) and(b triadState) triadState {
	if a == triadFalse || b == triadFalse {
		return triadFalse
	}
	if a == triadUnknown || b == triadUnknown {
		return triadUnknown
	}
	return triadTrue
}

func (a triadState) or(b triadState) triadState {
	if a == triadTrue || b == triadTrue {
		return triadTrue
	}
	if a == triadUnknown || b == triadUnknown {
		return triadUnknown
	}
	return triadFalse
}

func (a triadState) not() triadState {
	switch a {
	case triadTrue:
		return triadFalse
	case triadFalse:
		return triadTrue
	}
	return triadUnknown
}

// triggerPipelineEnv returns the predefined variables of a webhook-started
// pipeline on the protected default branch. A nil value is a variable
// known to be unset; variables absent from the map are unknown.
// CI_DEBUG_TRACE is taken as unset: fullsend's own deny-before-admit
// rule for it is a guard, not an admission defect. CI_OPEN_MERGE_REQUESTS
// is deliberately absent (unknown): GitLab populates it in branch
// pipelines whose branch is the source of an open merge request, which
// depends on project state this check cannot see.
func triggerPipelineEnv(defaultBranch string) map[string]*string {
	str := func(s string) *string { return &s }
	return map[string]*string{
		"CI_PIPELINE_SOURCE":       str("trigger"),
		"CI_COMMIT_REF_PROTECTED":  str("true"),
		"CI_COMMIT_REF_NAME":       str(defaultBranch),
		"CI_COMMIT_BRANCH":         str(defaultBranch),
		"CI_DEFAULT_BRANCH":        str(defaultBranch),
		"CI_COMMIT_TAG":            nil,
		"CI_DEBUG_TRACE":           nil,
		"CI_MERGE_REQUEST_IID":     nil,
		"CI_EXTERNAL_PULL_REQUEST": nil,
	}
}

// workflowValue is an operand of a rules:if expression.
type workflowValue struct {
	unknown bool
	null    bool
	str     string
	re      *regexp.Regexp
}

type workflowExprParser struct {
	src string
	pos int
	env map[string]*string
	err error
}

// evalWorkflowIf evaluates a GitLab rules:if expression against env. A
// parse failure or unsupported construct yields triadUnknown.
func evalWorkflowIf(expr string, env map[string]*string) triadState {
	// GitLab interpolates $[[ inputs.* ]] before evaluating the expression,
	// so text containing it cannot be compared literally.
	if hasInterpolationSyntax(expr) {
		return triadUnknown
	}
	p := &workflowExprParser{src: expr, env: env}
	got := p.parseOr()
	p.skipSpace()
	if p.err != nil || p.pos != len(p.src) {
		return triadUnknown
	}
	return got
}

func (p *workflowExprParser) skipSpace() {
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t' || p.src[p.pos] == '\n' || p.src[p.pos] == '\r') {
		p.pos++
	}
}

func (p *workflowExprParser) consume(tok string) bool {
	p.skipSpace()
	if strings.HasPrefix(p.src[p.pos:], tok) {
		p.pos += len(tok)
		return true
	}
	return false
}

func (p *workflowExprParser) fail(msg string) {
	if p.err == nil {
		p.err = fmt.Errorf("%s at offset %d", msg, p.pos)
	}
}

func (p *workflowExprParser) parseOr() triadState {
	left := p.parseAnd()
	for p.err == nil && p.consume("||") {
		left = left.or(p.parseAnd())
	}
	return left
}

func (p *workflowExprParser) parseAnd() triadState {
	left := p.parseComparison()
	for p.err == nil && p.consume("&&") {
		left = left.and(p.parseComparison())
	}
	return left
}

func (p *workflowExprParser) parseComparison() triadState {
	p.skipSpace()
	if p.consume("(") {
		inner := p.parseOr()
		if !p.consume(")") {
			p.fail("missing )")
		}
		return inner
	}
	left := p.parseOperand()
	if p.err != nil {
		return triadUnknown
	}
	var op string
	for _, candidate := range []string{"==", "!=", "=~", "!~"} {
		if p.consume(candidate) {
			op = candidate
			break
		}
	}
	if op == "" {
		// A bare operand is a truthiness test: set and non-empty.
		if left.unknown || left.re != nil {
			return triadUnknown
		}
		if left.null || left.str == "" {
			return triadFalse
		}
		return triadTrue
	}
	right := p.parseOperand()
	if p.err != nil {
		return triadUnknown
	}
	if left.unknown || right.unknown {
		return triadUnknown
	}
	switch op {
	case "==", "!=":
		if left.re != nil || right.re != nil {
			return triadUnknown
		}
		eq := left.null == right.null && left.str == right.str
		if (op == "==") == eq {
			return triadTrue
		}
		return triadFalse
	default:
		if left.re != nil || right.re == nil {
			return triadUnknown
		}
		matched := !left.null && right.re.MatchString(left.str)
		if (op == "=~") == matched {
			return triadTrue
		}
		return triadFalse
	}
}

func (p *workflowExprParser) parseOperand() workflowValue {
	p.skipSpace()
	if p.pos >= len(p.src) {
		p.fail("unexpected end")
		return workflowValue{unknown: true}
	}
	switch c := p.src[p.pos]; {
	case c == '$':
		start := p.pos + 1
		end := start
		for end < len(p.src) && (p.src[end] == '_' || p.src[end] >= '0' && p.src[end] <= '9' || p.src[end] >= 'a' && p.src[end] <= 'z' || p.src[end] >= 'A' && p.src[end] <= 'Z') {
			end++
		}
		if end == start {
			p.fail("empty variable name")
			return workflowValue{unknown: true}
		}
		name := p.src[start:end]
		p.pos = end
		val, known := p.env[name]
		switch {
		case !known:
			return workflowValue{unknown: true}
		case val == nil:
			return workflowValue{null: true}
		}
		return workflowValue{str: *val}
	case c == '"' || c == '\'':
		end := strings.IndexByte(p.src[p.pos+1:], c)
		if end < 0 {
			p.fail("unterminated string")
			return workflowValue{unknown: true}
		}
		s := p.src[p.pos+1 : p.pos+1+end]
		p.pos += end + 2
		return workflowValue{str: s}
	case c == '/':
		// Regex literal /.../flags; an escaped slash does not terminate it.
		i := p.pos + 1
		for i < len(p.src) && p.src[i] != '/' {
			if p.src[i] == '\\' {
				i++
			}
			i++
		}
		if i >= len(p.src) {
			p.fail("unterminated regex")
			return workflowValue{unknown: true}
		}
		pattern := p.src[p.pos+1 : i]
		i++
		flagStart := i
		for i < len(p.src) && (p.src[i] == 'i' || p.src[i] == 'm' || p.src[i] == 's') {
			i++
		}
		flags := p.src[flagStart:i]
		p.pos = i
		if flags != "" {
			pattern = "(?" + flags + ")" + pattern
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return workflowValue{unknown: true}
		}
		return workflowValue{re: re}
	case strings.HasPrefix(p.src[p.pos:], "null"):
		p.pos += len("null")
		return workflowValue{null: true}
	}
	p.fail("unsupported operand")
	return workflowValue{unknown: true}
}

// ruleOutcome is the result of evaluating an ordered rules: list.
type ruleOutcome int

const (
	// ruleAdmitted: the first matching rule admits (any when except never).
	ruleAdmitted ruleOutcome = iota
	// ruleRejected: the first matching rule is when: never.
	ruleRejected
	// ruleNoMatch: no rule matches.
	ruleNoMatch
	// ruleUnknown: a rule at or before the decision cannot be evaluated.
	ruleUnknown
)

// evaluateRules evaluates an ordered GitLab rules: sequence first-match
// against env. It returns the outcome and the 1-based index of the rule
// that decided it (or could not be evaluated); the index is 0 for
// ruleNoMatch. Aliases are resolved for the sequence and each rule.
// workflow:rules and include:rules share these semantics.
func evaluateRules(rules *yaml.Node, env map[string]*string) (ruleOutcome, int) {
	rules = resolveAlias(rules)
	if rules == nil || rules.Kind != yaml.SequenceNode {
		return ruleUnknown, 0
	}
	for i, rule := range rules.Content {
		n := resolveAlias(rule)
		if n == nil || n.Kind != yaml.MappingNode {
			return ruleUnknown, i + 1
		}
		matched := triadTrue
		for j := 0; j+1 < len(n.Content); j += 2 {
			switch key := n.Content[j].Value; key {
			case "if":
				cond := resolveAlias(n.Content[j+1])
				if cond == nil || cond.Kind != yaml.ScalarNode {
					return ruleUnknown, i + 1
				}
				matched = matched.and(evalWorkflowIf(cond.Value, env))
			case "when", "variables", "auto_cancel", "inputs":
			default:
				// changes, exists, and anything else depend on state
				// that is not known here.
				matched = matched.and(triadUnknown)
			}
		}
		switch matched {
		case triadFalse:
			continue
		case triadUnknown:
			return ruleUnknown, i + 1
		}
		if when := findMappingValue(n, "when"); when != nil {
			if w := resolveAlias(when); w == nil || w.Kind != yaml.ScalarNode || hasInterpolationSyntax(w.Value) {
				return ruleUnknown, i + 1
			} else if w.Value == "never" {
				return ruleRejected, i + 1
			}
		}
		return ruleAdmitted, i + 1
	}
	return ruleNoMatch, 0
}

// rulesProblem describes a non-admitting outcome; kind names the rules
// list ("workflow rule", "include rule"). It returns "" for ruleAdmitted.
func rulesProblem(outcome ruleOutcome, idx int, kind string) string {
	switch outcome {
	case ruleRejected:
		return fmt.Sprintf("(%s %d rejects them first)", kind, idx)
	case ruleNoMatch:
		return fmt.Sprintf("(no %s matches them)", kind)
	case ruleUnknown:
		if idx == 0 {
			return fmt.Sprintf("(the %ss cannot be evaluated, so admission cannot be established)", kind)
		}
		return fmt.Sprintf("(%s %d cannot be evaluated, so admission cannot be established)", kind, idx)
	}
	return ""
}

// workflowAdmissionProblem evaluates workflow:rules first-match for a
// protected-default-branch trigger pipeline. It returns "" when the
// pipeline is admitted, or a diagnostic fragment (completing "…on the
// default branch admits protected-default-branch trigger pipelines")
// when it is rejected or admission cannot be established.
func workflowAdmissionProblem(rules *yaml.Node, defaultBranch string) string {
	const subject = "admits protected-default-branch trigger pipelines"
	outcome, idx := evaluateRules(rules, triggerPipelineEnv(defaultBranch))
	if p := rulesProblem(outcome, idx, "workflow rule"); p != "" {
		return subject + " " + p
	}
	return ""
}

// includeAdmissionProblem evaluates an include item's rules: for a
// protected-default-branch trigger pipeline. It returns "" when the item
// has no rules: or its rules include it, or a diagnostic fragment when
// the include is rejected or its inclusion cannot be established.
// includeName names the include in the diagnostic.
func includeAdmissionProblem(item *yaml.Node, defaultBranch, includeName string) string {
	item = resolveAlias(item)
	if item == nil || item.Kind != yaml.MappingNode {
		return ""
	}
	// Rules may be inherited through YAML merge keys, where GitLab applies
	// them; defer when the merge cannot be evaluated.
	rules, ok := effectiveMappingValue(item, "rules")
	if !ok {
		return "applies the " + includeName + " include to protected-default-branch trigger pipelines (the include rules cannot be evaluated, so admission cannot be established)"
	}
	if rules == nil {
		return ""
	}
	outcome, idx := evaluateRules(rules, triggerPipelineEnv(defaultBranch))
	if p := rulesProblem(outcome, idx, "include rule"); p != "" {
		return "applies the " + includeName + " include to protected-default-branch trigger pipelines " + p
	}
	return ""
}
