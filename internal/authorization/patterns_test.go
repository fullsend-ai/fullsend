package authorization

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMatchPattern_PrefixDirectory(t *testing.T) {
	patterns := WorkflowFilePatterns()
	assert.True(t, MatchesAny(".github/workflows/ci.yml", patterns))
	assert.True(t, MatchesAny(".github/workflows/nested/job.yml", patterns))
	assert.True(t, MatchesAny(".github/workflows", patterns))
	assert.False(t, MatchesAny(".github/actions/ci.yml", patterns))
}

func TestMatchPattern_Wildcard(t *testing.T) {
	assert.True(t, MatchesAny("workflows/ci.yml", []string{"workflows/*.yml"}))
	assert.False(t, MatchesAny("workflows/nested/ci.yml", []string{"workflows/*.yml"}))
}

func TestMatchPattern_Exact(t *testing.T) {
	assert.True(t, MatchesAny("Makefile", []string{"Makefile"}))
	assert.False(t, MatchesAny("Makefile", []string{"Dockerfile"}))
}
