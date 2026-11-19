package github

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseGitHubIssueNumberFromURL(t *testing.T) {
	tests := []struct {
		url  string
		want int
	}{
		{"https://api.github.com/repos/fullsend-ai/fullsend/issues/42", 42},
		{"https://api.github.com/repos/fullsend-ai/fullsend/issues/42/", 42},
		{"https://api.github.com/repos/o/r/issues/1", 1},
		{"https://api.github.com/repos/o/r/pulls/1", 0},
		{"", 0},
		{"not-a-url", 0},
		{"https://api.github.com/repos/o/r/issues/foo", 0},
		{"https://api.github.com/repos/o/r/issues/0", 0},
	}
	for _, tc := range tests {
		t.Run(tc.url, func(t *testing.T) {
			assert.Equal(t, tc.want, parseGitHubIssueNumberFromURL(tc.url))
		})
	}
}
