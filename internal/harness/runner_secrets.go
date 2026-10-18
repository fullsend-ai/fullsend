package harness

import (
	"fmt"
	"os"
	"sort"
	"strings"

	celast "github.com/google/cel-go/common/ast"
)

// EnvRefNames returns the variable names os.Expand would look up in value,
// in order of appearance. It covers both ${NAME} and $NAME forms, so a
// check built on it sees exactly what an expansion site would resolve.
func EnvRefNames(value string) []string {
	if !strings.Contains(value, "$") {
		return nil
	}
	var names []string
	os.Expand(value, func(name string) string {
		names = append(names, name)
		return ""
	})
	return names
}

// overlayRunnerSecretRefs returns the runner secret names (ADR 0136) an
// overlay entry references from any of its environment-bearing fields,
// sorted and de-duplicated.
func overlayRunnerSecretRefs(fc *ForgeConfig, names map[string]bool) []string {
	seen := make(map[string]bool)
	collect := func(value string) {
		for _, n := range EnvRefNames(value) {
			if names[n] {
				seen[n] = true
			}
		}
	}
	for _, v := range fc.RunnerEnv {
		collect(v)
	}
	if fc.Env != nil {
		for _, v := range fc.Env.Runner {
			collect(v)
		}
		for _, v := range fc.Env.Sandbox {
			collect(v)
		}
	}
	for _, hf := range fc.HostFiles {
		collect(hf.Src)
	}
	if fc.ValidationLoop != nil {
		collect(fc.ValidationLoop.Schema)
		collect(fc.ValidationLoop.PreflightCheck)
	}
	refs := make([]string, 0, len(seen))
	for n := range seen {
		refs = append(refs, n)
	}
	sort.Strings(refs)
	return refs
}

// overlayGuardReadsOnlyForgeOrConfig reports whether an overlay when:
// expression reads nothing but runtime.forge and config. Such a guard
// cannot be steered by event content, so an overlay it selects may carry a
// runner secret reference. Until ADR 0112's guarded-field check exists,
// every other guard is refused for those overlays (ADR 0136).
//
// The check is syntactic and conservative: any reference to event, to
// runtime other than through the .forge field, or a compile failure counts
// as reading something else.
func overlayGuardReadsOnlyForgeOrConfig(when string) bool {
	env, err := NewOverlayEnv()
	if err != nil {
		return false
	}
	checked, issues := env.Compile(strings.TrimSpace(when))
	if issues != nil && issues.Err() != nil {
		return false
	}
	forgeOperand := make(map[int64]bool)
	ok := true
	celast.PreOrderVisit(checked.NativeRep().Expr(), celast.NewExprVisitor(func(e celast.Expr) {
		switch e.Kind() {
		case celast.SelectKind:
			sel := e.AsSelect()
			op := sel.Operand()
			if op.Kind() == celast.IdentKind && op.AsIdent() == "runtime" && sel.FieldName() == "forge" && !sel.IsTestOnly() {
				forgeOperand[op.ID()] = true
			}
		case celast.IdentKind:
			switch e.AsIdent() {
			case "event":
				ok = false
			case "runtime":
				if !forgeOperand[e.ID()] {
					ok = false
				}
			}
		}
	}))
	return ok
}

// ValidateOverlayRunnerSecretRefs refuses an overlay that references a
// runner secret name when its when: guard reads anything other than
// runtime.forge or config (ADR 0136). A reference in an overlay only
// counts when that overlay matches; this check runs before resolution so
// a non-matching overlay is still held to the rule.
func ValidateOverlayRunnerSecretRefs(overlays []OverlayEntry, names map[string]bool) error {
	if len(names) == 0 {
		return nil
	}
	for i := range overlays {
		refs := overlayRunnerSecretRefs(&overlays[i].ForgeConfig, names)
		if len(refs) == 0 {
			continue
		}
		if !overlayGuardReadsOnlyForgeOrConfig(overlays[i].When) {
			return fmt.Errorf("overlays[%d] references runner secret(s) %s, but its when: reads more than runtime.forge and config", i, strings.Join(refs, ", "))
		}
	}
	return nil
}
