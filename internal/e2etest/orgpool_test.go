package e2etest_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fullsend-ai/fullsend/internal/e2etest"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
)

// TestOrgPoolMatchesDevPoolOrgCount keeps the hand-written orgPool in sync
// with the behaviour slot pool, whose count hack/setup-e2e-inference-gateway.sh
// also checks against its POOL_ORG_COUNT. The test lives in an external test
// package because install imports e2etest.
func TestOrgPoolMatchesDevPoolOrgCount(t *testing.T) {
	want := make([]string, 0, install.DevPoolOrgCount)
	for i := 1; i <= install.DevPoolOrgCount; i++ {
		want = append(want, fmt.Sprintf("halfsend-%02d", i))
	}
	assert.Equal(t, want, e2etest.OrgPool())
}
