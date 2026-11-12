package harness

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOverlayGuardHandlesEveryNewOverlayEnvVariable fails the build when a
// variable is added to NewOverlayEnv (trigger.go) without a verdict here.
// overlayGuardReadsOnlyForgeOrConfig refuses only event and runtime (unless
// read only as .forge) and lets every other identifier pass, so a variable
// added to NewOverlayEnv would pass the guard unchecked (ADR 0136). Each
// entry maps a declared variable to the guard's verdict on a probe that
// reads only that variable: "<name> == <name>" compiles for any dyn
// variable and reads only that variable, unlike has(runtime.forge), which
// the guard would refuse for the wrong reason.
func TestOverlayGuardHandlesEveryNewOverlayEnvVariable(t *testing.T) {
	verdicts := map[string]bool{
		"event":   false, // refused
		"runtime": false, // refused unless read only as .forge
		"config":  true,  // allowed
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "trigger.go", nil, 0)
	require.NoError(t, err)

	var fn *ast.FuncDecl
	for _, d := range file.Decls {
		if f, ok := d.(*ast.FuncDecl); ok && f.Name.Name == "NewOverlayEnv" {
			fn = f
			break
		}
	}
	require.NotNil(t, fn, "NewOverlayEnv stays declared in trigger.go")

	var declared []string
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Variable" {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "cel" {
			return true
		}
		if len(call.Args) == 0 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		name, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		declared = append(declared, name)
		return true
	})
	require.NotEmpty(t, declared, "NewOverlayEnv declares at least one cel.Variable")

	for _, name := range declared {
		_, handled := verdicts[name]
		require.True(t, handled,
			"NewOverlayEnv declares %q, but overlayGuardReadsOnlyForgeOrConfig has no verdict for it: "+
				"either teach the guard to refuse it, or record here how the guard treats a probe reading only %q",
			name, name)
		probe := name + " == " + name
		assert.Equal(t, verdicts[name], overlayGuardReadsOnlyForgeOrConfig(probe),
			"guard verdict on a probe that reads only %q", name)
	}
}
