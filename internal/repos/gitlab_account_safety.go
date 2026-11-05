package repos

import "time"

// GitLabAuthenticationKey is the non-secret key metadata used to decide
// whether an account can still authenticate after its PATs are revoked.
type GitLabAuthenticationKey struct {
	ID        int
	UsageType string
	ExpiresAt string
}

// UsableGitLabSSHKeyIDs returns authentication paths that prevent temporary
// elevation or verified containment. Unknown usage or malformed expiry is
// conservatively usable; signing-only and expired keys cannot authenticate.
func UsableGitLabSSHKeyIDs(keys []GitLabAuthenticationKey, now time.Time) []int {
	var ids []int
	for _, key := range keys {
		if key.UsageType == "signing" {
			continue
		}
		if key.ExpiresAt != "" {
			if expiry, err := time.Parse(time.RFC3339, key.ExpiresAt); err == nil && !expiry.After(now) {
				continue
			}
		}
		ids = append(ids, key.ID)
	}
	return ids
}
