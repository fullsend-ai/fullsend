package repos

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestUsableGitLabSSHKeyIDs(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, usage, expiry string
		usable              bool
	}{
		{name: "authentication", usage: "auth", usable: true},
		{name: "authentication and signing", usage: "auth_and_signing", usable: true},
		{name: "old server without usage", usable: true},
		{name: "unknown usage", usage: "new-kind", usable: true},
		{name: "signing only", usage: "signing"},
		{name: "expired", expiry: now.Add(-time.Second).Format(time.RFC3339)},
		{name: "expires exactly now", expiry: now.Format(time.RFC3339)},
		{name: "future expiry", expiry: now.Add(time.Second).Format(time.RFC3339), usable: true},
		{name: "unparseable expiry", expiry: "invalid", usable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ids := UsableGitLabSSHKeyIDs([]GitLabAuthenticationKey{{ID: 77, UsageType: tc.usage, ExpiresAt: tc.expiry}}, now)
			if tc.usable {
				assert.Equal(t, []int{77}, ids)
			} else {
				assert.Empty(t, ids)
			}
		})
	}
	assert.Empty(t, UsableGitLabSSHKeyIDs(nil, now))
	assert.Equal(t, []int{1, 3}, UsableGitLabSSHKeyIDs([]GitLabAuthenticationKey{{ID: 1}, {ID: 2, UsageType: "signing"}, {ID: 3}}, now))
}
