package repos

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLiveTriggerOwnerDoesNotClaimQuiescence guards the fail-closed
// deferral seam in mintPollerOwnedTrigger: no production code may satisfy
// GitLabPollerQuiescenceVerifier. GitLab offers no server-side guarantee that
// requests accepted with revoked Poller credentials have drained, so every
// live trigger owner must fail the type assertion, which always defers
// temporary Maintainer elevation and keeps the polling schedules in effect.
// Only the live fresh-identity handoff adapter may replace this seam, and it
// must remove this guard deliberately when it does.
func TestLiveTriggerOwnerDoesNotClaimQuiescence(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)

	const method = "VerifyPollerQuiescence"
	var claims []string
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
			file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
			if parseErr != nil {
				return parseErr
			}
			scanned++
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if ok && fn.Recv != nil && fn.Name.Name == method {
					rel, _ := filepath.Rel(root, path)
					claims = append(claims, rel)
				}
			}
			return nil
		})
		require.NoError(t, err)
	}

	require.NotZero(t, scanned, "the guard must scan production sources")
	assert.Empty(t, claims, "production code must not implement GitLabPollerQuiescenceVerifier (%s); elevation must always defer and keep polling", method)
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
