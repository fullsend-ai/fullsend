package secretarial

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractTopicsHeuristic_Headings(t *testing.T) {
	notes := "# Attendees\nAlice, Bob\n\n## Refactor API client\nWe should do this.\n\n## Update CI pipeline\nNeed faster builds."
	topics := ExtractTopicsHeuristic(notes, "https://example.com/doc")

	require.Len(t, topics, 2)
	assert.Equal(t, "Refactor API client", topics[0].Title)
	assert.Equal(t, "Update CI pipeline", topics[1].Title)
	assert.Contains(t, topics[0].Summary, "https://example.com/doc")
}

func TestExtractTopicsHeuristic_ActionItems(t *testing.T) {
	notes := "Action item: migrate database to new cluster\nTodo: update the documentation for v2"
	topics := ExtractTopicsHeuristic(notes, "https://example.com/doc")

	require.Len(t, topics, 2)
	assert.Equal(t, "migrate database to new cluster", topics[0].Title)
}

func TestExtractTopicsHeuristic_SkipsBoilerplate(t *testing.T) {
	notes := "# Attendees\nAlice\n# Agenda\n- stuff\n# Notes\njust notes\n# Minutes\nminutes"
	topics := ExtractTopicsHeuristic(notes, "https://example.com/doc")
	assert.Empty(t, topics)
}

func TestExtractTopicsHeuristic_EmptyNotes(t *testing.T) {
	topics := ExtractTopicsHeuristic("", "https://example.com/doc")
	assert.Empty(t, topics)
}
