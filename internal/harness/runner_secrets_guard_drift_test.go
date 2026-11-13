package harness

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
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
//
// The walk fails closed: every option NewOverlayEnv passes to cel.NewEnv,
// cel.NewCustomEnv or a chained env.Extend must be a cel.Variable call
// with a string-literal name, so declaring a variable any other way (a
// non-literal name, an import alias, cel.VariableDecls or
// cel.Declarations, a helper returning cel.EnvOption, a spread) fails
// this test instead of passing silently with the variable unchecked by
// the guard.
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
		if !ok {
			return true
		}
		// The env constructors take the bare cel identifier only: an
		// import alias is itself a declaration style this test must be
		// updated for (with no bare-cel constructor left, the NotEmpty
		// check below fails). Extend is checked whatever its receiver:
		// a chained env.Extend(...) declares variables the same way.
		switch sel.Sel.Name {
		case "NewEnv", "NewCustomEnv":
			if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "cel" {
				return true
			}
		case "Extend":
		default:
			return true
		}
		for _, arg := range call.Args {
			name, ok := celVariableName(arg)
			require.True(t, ok,
				"%s is passed %s, which is not cel.Variable with a string-literal name: "+
					"every variable NewOverlayEnv declares must be spelled cel.Variable(\"<name>\", ...) "+
					"so this test can check the guard has a verdict for it (ADR 0136); either keep that "+
					"shape, or rework this test together with the declaration",
				sel.Sel.Name, renderNode(fset, arg))
			declared = append(declared, name)
		}
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

// celVariableName reports whether arg is a call cel.Variable(<string
// literal>, ...) on the bare cel identifier, and returns the declared name.
func celVariableName(arg ast.Expr) (string, bool) {
	call, ok := arg.(*ast.CallExpr)
	if !ok {
		return "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Variable" {
		return "", false
	}
	if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "cel" {
		return "", false
	}
	if len(call.Args) == 0 {
		return "", false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	name, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return name, true
}

// renderNode renders an AST node for a failure message.
func renderNode(fset *token.FileSet, n ast.Node) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, n); err != nil {
		return "<unprintable expression>"
	}
	return buf.String()
}
