package security

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeAgentText(t *testing.T) {
	t.Run("clean text passes through untouched", func(t *testing.T) {
		out, findings := SanitizeAgentText("re-check the migration in db/001.sql")
		assert.Equal(t, "re-check the migration in db/001.sql", out)
		assert.Zero(t, findings)
	})

	t.Run("compatibility characters are content, not an attack", func(t *testing.T) {
		// NFKC would rewrite these; the original bytes must survive so the
		// agent does not write the normalized form back into a file.
		in := "\uff41\uff42"
		out, findings := SanitizeAgentText(in)
		assert.Equal(t, in, out)
		assert.Zero(t, findings)
	})

	t.Run("non-rendering characters are stripped and reported", func(t *testing.T) {
		out, findings := SanitizeAgentText("ignore\u200b this")
		assert.Positive(t, findings)
		assert.NotContains(t, out, "\u200b")
	})
}
