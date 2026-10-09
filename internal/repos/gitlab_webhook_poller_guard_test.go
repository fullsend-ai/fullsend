package repos

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// moduleImporter type-checks packages of this module from source and stubs
// every other import (standard library and third-party) with an empty
// package. A verifier can only come from this module, and stubbing keeps the
// guard fast: it never compiles or source-imports the dependency tree.
type moduleImporter struct {
	fset    *token.FileSet
	modPath string
	root    string
	method  string
	cache   map[string]*types.Package
}

func (m *moduleImporter) Import(path string) (*types.Package, error) {
	if pkg, ok := m.cache[path]; ok {
		return pkg, nil
	}
	rel, inModule := strings.CutPrefix(path, m.modPath+"/")
	if !inModule {
		pkg := types.NewPackage(path, filepath.Base(path))
		pkg.MarkComplete()
		m.cache[path] = pkg
		return pkg, nil
	}
	dir := filepath.Join(m.root, filepath.FromSlash(rel))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if ok, matchErr := build.Default.MatchFile(dir, name); matchErr != nil || !ok {
			continue
		}
		file, parseErr := parser.ParseFile(m.fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return nil, parseErr
		}
		files = append(files, file)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no Go files in %s", dir)
	}
	pkg, _ := m.check(path, files)
	m.cache[path] = pkg
	return pkg, nil
}

// check type-checks files as one package. Type errors are tolerated: stubbed
// imports leave unresolved names, and that must not let a claim go unnoticed.
func (m *moduleImporter) check(path string, files []*ast.File) (*types.Package, error) {
	conf := types.Config{Importer: m, Error: func(error) {}}
	return conf.Check(path, m.fset, files, nil)
}

// claims returns the concrete types of files whose method set (value or
// pointer receiver, including methods promoted through embedded fields)
// contains the method. Interfaces only declare the method, so they are
// skipped.
func (m *moduleImporter) claims(path string, files []*ast.File) []string {
	pkg, _ := m.check(path, files)
	var claims []string
	for _, name := range pkg.Scope().Names() {
		tn, ok := pkg.Scope().Lookup(name).(*types.TypeName)
		if !ok || types.IsInterface(tn.Type()) {
			continue
		}
		for _, t := range []types.Type{tn.Type(), types.NewPointer(tn.Type())} {
			if types.NewMethodSet(t).Lookup(pkg, m.method) != nil {
				claims = append(claims, name)
				break
			}
		}
	}
	return claims
}

// TestLiveTriggerOwnerDoesNotClaimQuiescence guards the fail-closed
// deferral seam in mintPollerOwnedTrigger: no production code may satisfy
// GitLabPollerQuiescenceVerifier. GitLab offers no server-side guarantee that
// requests accepted with revoked Poller credentials have drained, so every
// live trigger owner must fail the type assertion, which always defers
// temporary Maintainer elevation and keeps the polling schedules in effect.
// Only the live fresh-identity handoff adapter may replace this seam, and it
// must remove this guard deliberately when it does.
//
// The guard inspects type-checked method sets, so a type that inherits the
// method by embedding a verifier is caught as well as one that declares it.
func TestLiveTriggerOwnerDoesNotClaimQuiescence(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)

	const method = "VerifyPollerQuiescence"
	fset := token.NewFileSet()
	imp := &moduleImporter{fset: fset, modPath: "github.com/fullsend-ai/fullsend", root: root, method: method, cache: map[string]*types.Package{}}

	pkgFiles := map[string][]*ast.File{}
	scanned := 0
	for _, dir := range []string{"cmd", "internal", "pkg"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				switch d.Name() {
				case "testdata", "vendor", "node_modules":
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			if ok, matchErr := build.Default.MatchFile(filepath.Dir(path), d.Name()); matchErr != nil || !ok {
				return matchErr
			}
			file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if parseErr != nil {
				return parseErr
			}
			scanned++
			pkgFiles[filepath.Dir(path)] = append(pkgFiles[filepath.Dir(path)], file)
			return nil
		})
		require.NoError(t, err)
	}
	require.NotZero(t, scanned, "the guard must scan production sources")

	var claims []string
	for dir, files := range pkgFiles {
		rel, _ := filepath.Rel(root, dir)
		for _, name := range imp.claims("github.com/fullsend-ai/fullsend/"+filepath.ToSlash(rel), files) {
			claims = append(claims, rel+"."+name)
		}
	}
	sort.Strings(claims)
	assert.Empty(t, claims, "production code must not implement GitLabPollerQuiescenceVerifier (%s); elevation must always defer and keep polling", method)
}

// The guard must reject a declared method, a method promoted by embedding a
// verifier interface, and one promoted from an embedded implementation, while
// accepting a type that has no such method.
func TestQuiescenceGuardDetectsPromotedMethods(t *testing.T) {
	const src = `package fixture

import "context"

type Verifier interface {
	VerifyPollerQuiescence(ctx context.Context, owner, repo string, userID int64) error
}

type impl struct{}

func (impl) VerifyPollerQuiescence(context.Context, string, string, int64) error { return nil }

type Declared struct{}

func (*Declared) VerifyPollerQuiescence(context.Context, string, string, int64) error { return nil }

type EmbedsInterface struct{ Verifier }

type EmbedsImpl struct{ impl }

type Clean struct{ name string }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", src, parser.SkipObjectResolution)
	require.NoError(t, err)

	imp := &moduleImporter{fset: fset, modPath: "example.com/fixture", root: t.TempDir(), method: "VerifyPollerQuiescence", cache: map[string]*types.Package{}}
	claims := imp.claims("example.com/fixture/fixture", []*ast.File{file})
	assert.ElementsMatch(t, []string{"Declared", "EmbedsInterface", "EmbedsImpl", "impl"}, claims)
}

// With no verifier, the fast path defers before any credential is touched
// and polling stays in effect.
func TestPollerElevationDefersWithoutVerifier(t *testing.T) {
	c := newWebhookFake()
	po := newPollerOwner(c)
	var owner GitLabTriggerOwner = noQuiescenceOwner{po}
	_, ok := owner.(GitLabPollerQuiescenceVerifier)
	require.False(t, ok, "the trigger owner under test must not claim quiescence")

	pm, err := mintPollerOwnedTrigger(t.Context(), c, owner, webhookTestOwner, webhookTestRepo, &credentialRedactor{})

	require.NoError(t, err)
	assert.Nil(t, pm.minted)
	assert.Empty(t, pm.revokedIDs)
	assert.Contains(t, pm.deferReason, "polling schedules remain in effect")
	assert.Zero(t, po.published, "the runtime credential is never revoked, so nothing is republished")
	assert.Empty(t, po.sets, "the Poller's membership is never changed")
}
