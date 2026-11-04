package repos

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// gitlabMaxExtendsDepth is GitLab's limit on extends: nesting; a deeper chain
// is a configuration error, so the scan fails closed on it.
const gitlabMaxExtendsDepth = 11

// gitlabMaxReferenceDepth is GitLab's limit on nested !reference tags.
const gitlabMaxReferenceDepth = 10

// gitlabMaxReferenceWork bounds the total work of expanding one job's rules:
// the !reference lookups followed plus the rule entries copied. The depth
// limit alone does not bound it, because each level can reference the
// previous one many times, so the work grows exponentially with the depth.
// Real configurations need a few dozen; exceeding this fails closed.
const gitlabMaxReferenceWork = 10000

const budgetErr = "rules expand !reference tags into too much work (the references fan out too far)"

// ciTemplateScope is what extends: and !reference are resolved against when
// the trigger extra-work scan evaluates the jobs of one committed CI file
// (#8691): that file's own top-level keys. GitLab resolves both against the
// whole merged configuration, so a name the file does not define (a template
// from an include) cannot be resolved, and a name another file loaded into
// the same pipeline also defines cannot be trusted either, because GitLab
// deep-merges the definitions and the other file could add rules, when, only
// or except. Both fail closed.
type ciTemplateScope struct {
	keys   map[string]*yaml.Node
	shared func(string) bool
}

func newCITemplateScope(root *yaml.Node, shared func(string) bool) ciTemplateScope {
	keys := make(map[string]*yaml.Node, len(root.Content)/2)
	for i := 0; i+1 < len(root.Content); i += 2 {
		name, ok := classifyTopLevelKey(root.Content[i])
		if !ok || hasInterpolationSyntax(name) {
			continue
		}
		keys[name] = root.Content[i+1]
	}
	return ciTemplateScope{keys: keys, shared: shared}
}

// lookup returns the top-level value name refers to. why is non-empty when it
// cannot be used; it completes "…has job %q whose …".
func (s ciTemplateScope) lookup(name, what string) (node *yaml.Node, why string) {
	node, ok := s.keys[name]
	if !ok {
		return nil, fmt.Sprintf("%s %q is not defined in the same file (templates from includes are not resolved)", what, name)
	}
	if s.shared != nil && s.shared(name) {
		return nil, fmt.Sprintf("%s %q is also defined in another file the pipeline loads (GitLab merges the definitions)", what, name)
	}
	return node, ""
}

// jobAdmission is the effective configuration that decides whether GitLab
// adds a job to a pipeline. A nil field means the key is absent.
type jobAdmission struct {
	rules *yaml.Node
	when  *yaml.Node
}

// effectiveJobAdmission resolves a job's effective rules: and when: the way
// GitLab does: extends: targets are merged in order (a later target wins over
// an earlier one, and the job's own keys win over all of them; rules is a
// list, so it is replaced rather than merged), nested extends resolve
// recursively, and !reference tags in the resulting rules are expanded.
// Anything this cannot establish fails closed with a non-empty why that
// completes "…has job %q whose …": a target outside the file or shared with
// another file, a cycle or over-deep chain, a non-literal extends value, an
// unresolvable merge key or alias, and only/except anywhere in the chain
// (their admission semantics are not modelled).
func effectiveJobAdmission(job *yaml.Node, scope ciTemplateScope) (adm jobAdmission, why string) {
	adm, _, why = resolveExtendsAdmission(job, "configuration", scope, 0, map[*yaml.Node]bool{}, map[*yaml.Node]resolvedExtends{})
	if why != "" || adm.rules == nil {
		return adm, why
	}
	budget := gitlabMaxReferenceWork
	adm.rules, why = expandRuleReferences(adm.rules, scope, 0, &budget)
	return adm, why
}

// resolvedExtends is a memoized successful resolution of one extends: node:
// its effective admission and the height of its extends chain (0 for a node
// that extends nothing), so a cached result can still be checked against the
// depth limit at its new position.
type resolvedExtends struct {
	adm    jobAdmission
	height int
}

// resolveExtendsAdmission resolves one node's admission, returning it with the
// height of the node's extends chain. memo holds the nodes already resolved
// successfully, so a template shared by many extends: entries is resolved once
// and the total work stays linear in the number of templates and extends
// entries rather than in the number of paths through them. Failures are not
// cached: the first one aborts the whole resolution.
func resolveExtendsAdmission(node *yaml.Node, label string, scope ciTemplateScope, depth int, visiting map[*yaml.Node]bool, memo map[*yaml.Node]resolvedExtends) (adm jobAdmission, height int, why string) {
	const chainErr = "extends chain is cyclic or nested more than %d levels"
	mapping := resolveAlias(node)
	if mapping == nil || mapping.Kind != yaml.MappingNode || hasNonCoreTag(mapping) {
		return adm, 0, label + " is not a mapping"
	}
	if visiting[mapping] || depth > gitlabMaxExtendsDepth {
		return adm, 0, fmt.Sprintf(chainErr, gitlabMaxExtendsDepth)
	}
	// A cached node was resolved without meeting any node now being visited
	// (it would have been a cycle), so only the depth limit can differ here.
	if cached, ok := memo[mapping]; ok {
		if depth+cached.height > gitlabMaxExtendsDepth {
			return adm, 0, fmt.Sprintf(chainErr, gitlabMaxExtendsDepth)
		}
		return cached.adm, cached.height, ""
	}
	visiting[mapping] = true
	defer delete(visiting, mapping)

	values := make(map[string]*yaml.Node, 5)
	for _, key := range []string{"rules", "when", "only", "except", "extends"} {
		v, ok := effectiveMappingValue(mapping, key)
		if !ok {
			return adm, 0, label + " has a merge key or alias that cannot be resolved"
		}
		values[key] = v
	}
	if values["only"] != nil || values["except"] != nil {
		return adm, 0, label + " uses only or except (directly or through extends), which the scan does not evaluate"
	}
	if ext := values["extends"]; ext != nil {
		names, ok := extendsNames(ext)
		if !ok {
			return adm, 0, "extends value is not a literal template name or list of names"
		}
		for _, name := range names {
			target, why := scope.lookup(name, "extends target")
			if why != "" {
				return adm, 0, why
			}
			parent, parentHeight, why := resolveExtendsAdmission(target, fmt.Sprintf("extends target %q", name), scope, depth+1, visiting, memo)
			if why != "" {
				return adm, 0, why
			}
			height = max(height, parentHeight+1)
			if parent.rules != nil {
				adm.rules = parent.rules
			}
			if parent.when != nil {
				adm.when = parent.when
			}
		}
	}
	if values["rules"] != nil {
		adm.rules = values["rules"]
	}
	if values["when"] != nil {
		adm.when = values["when"]
	}
	memo[mapping] = resolvedExtends{adm: adm, height: height}
	return adm, height, ""
}

// expandRuleReferences returns rules with GitLab !reference tags resolved
// against scope: a rules value that is itself a !reference, and !reference
// entries in a rules list, whose target list GitLab flattens into the
// surrounding list. The original nodes are not modified. Anything else is
// returned unchanged for evaluateRules to judge. budget is the work left for
// the whole expansion (see gitlabMaxReferenceWork); each call and each rule
// entry copied spends from it, and running out fails closed.
func expandRuleReferences(rules *yaml.Node, scope ciTemplateScope, depth int, budget *int) (*yaml.Node, string) {
	if depth > gitlabMaxReferenceDepth {
		return nil, fmt.Sprintf("rules nest !reference tags more than %d levels deep", gitlabMaxReferenceDepth)
	}
	if *budget--; *budget < 0 {
		return nil, budgetErr
	}
	resolved := resolveAlias(rules)
	if resolved == nil {
		return rules, ""
	}
	if resolved.Tag == "!reference" {
		target, why := resolveReference(resolved, scope)
		if why != "" {
			return nil, why
		}
		return expandRuleReferences(target, scope, depth+1, budget)
	}
	if resolved.Kind != yaml.SequenceNode {
		return resolved, ""
	}
	out := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, item := range resolved.Content {
		r := resolveAlias(item)
		if r == nil || r.Tag != "!reference" {
			if *budget--; *budget < 0 {
				return nil, budgetErr
			}
			out.Content = append(out.Content, item)
			continue
		}
		target, why := resolveReference(r, scope)
		if why != "" {
			return nil, why
		}
		expanded, why := expandRuleReferences(target, scope, depth+1, budget)
		if why != "" {
			return nil, why
		}
		if expanded.Kind == yaml.SequenceNode && !hasNonCoreTag(expanded) {
			if *budget -= len(expanded.Content); *budget < 0 {
				return nil, budgetErr
			}
			out.Content = append(out.Content, expanded.Content...)
		} else {
			out.Content = append(out.Content, expanded)
		}
	}
	return out, ""
}

// resolveReference resolves a !reference [key, …] path against scope. GitLab
// resolves !reference before extends:, so the path is followed through the
// file's own keys (YAML merge keys included) and never through extends.
func resolveReference(ref *yaml.Node, scope ciTemplateScope) (*yaml.Node, string) {
	if ref.Kind != yaml.SequenceNode || len(ref.Content) == 0 {
		return nil, "rules use a !reference that is not a list of keys"
	}
	path := make([]string, 0, len(ref.Content))
	for _, item := range ref.Content {
		key, ok := isLiteralScalarValue(item)
		if !ok {
			return nil, "rules use a !reference whose path is not a list of literal keys"
		}
		path = append(path, key)
	}
	node, why := scope.lookup(path[0], "!reference target")
	if why != "" {
		return nil, why
	}
	for _, key := range path[1:] {
		v, ok := effectiveMappingValue(node, key)
		if !ok || v == nil {
			return nil, fmt.Sprintf("rules use !reference [%s], which does not resolve in the same file", strings.Join(path, ", "))
		}
		node = v
	}
	return node, ""
}

// sharedTopLevelKeys reports the top-level names defined by more than one of
// the given CI files (nil entries are absent files). GitLab deep-merges
// same-named keys across the files of one pipeline, so a template defined in
// more than one of them is not resolved from a single file.
func sharedTopLevelKeys(contents ...[]byte) func(string) bool {
	counts := map[string]int{}
	for _, content := range contents {
		if content == nil {
			continue
		}
		docs, err := decodeGitLabDocuments(content)
		if err != nil || len(docs) == 0 || docs[len(docs)-1].Kind != yaml.DocumentNode || len(docs[len(docs)-1].Content) == 0 {
			continue
		}
		root := docs[len(docs)-1].Content[0]
		if root.Kind != yaml.MappingNode {
			continue
		}
		seen := map[string]bool{}
		for i := 0; i+1 < len(root.Content); i += 2 {
			name, ok := classifyTopLevelKey(root.Content[i])
			if !ok || seen[name] {
				continue
			}
			seen[name] = true
			counts[name]++
		}
	}
	return func(name string) bool { return counts[name] > 1 }
}
