package normentity

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewID(t *testing.T) {
	id, err := NewID("github", "GitHub.com", "work_item", "I_kwDOExample53")
	require.NoError(t, err)
	assert.Equal(t, ID("fullsend:entity:v1:github:github.com:work_item:I_kwDOExample53"), id)
	assert.True(t, id.Valid())

	id, err = NewID("gitlab", "gitlab.example.com:8443", "change_proposal", "a:b/c é~")
	require.NoError(t, err)
	assert.Equal(t, ID("fullsend:entity:v1:gitlab:gitlab.example.com%3A8443:change_proposal:a%3Ab%2Fc%20%C3%A9~"), id)
	assert.True(t, id.Valid())

	for _, tc := range []struct{ system, host, kind, source string }{
		{"", "github.com", "work_item", "1"},
		{"github", "", "work_item", "1"},
		{"github", "github.com", "", "1"},
		{"github", "github.com", "work_item", ""},
	} {
		_, err := NewID(tc.system, tc.host, tc.kind, tc.source)
		assert.Error(t, err, "%+v", tc)
	}
}

func TestIDValid(t *testing.T) {
	for _, id := range []ID{
		"",
		"#123",
		"fullsend:actor:v1:github:github.com:12345",
		"fullsend:entity:v1:github:github.com:work_item",
		"fullsend:entity:v1:github::work_item:I_1",
		"fullsend:entity:v1:github:github.com:work_item:I_1:2",
		// Non-canonical spellings of otherwise well-formed IDs.
		"fullsend:entity:v1:github:GitHub.com:work_item:I_1",
		"fullsend:entity:v1:github:%67ithub.com:work_item:I_1",
		"fullsend:entity:v1:github:github.com:work_item:a/b",
		"fullsend:entity:v1:github:github.com:work_item:a b",
		"fullsend:entity:v1:github:github.com:work_item:a%2fb",
		"fullsend:entity:v1:github:github.com:work_item:a%2",
		"fullsend:entity:v1:github:github.com:work_item:a%ZZb",
	} {
		assert.False(t, id.Valid(), "%q", id)
	}
	assert.True(t, ID("fullsend:entity:v1:github:github.com:work_item:a%2Fb").Valid())
}
