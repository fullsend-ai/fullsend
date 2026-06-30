package mintcore

import (
	"fmt"
	"strings"
)

// BuildRepoProviderID generates a GCP WIF provider ID scoped to a single repo.
// GCP requires 4-32 chars, [a-z][a-z0-9-]*, no trailing hyphen.
func BuildRepoProviderID(owner, repo string) string {
	raw := fmt.Sprintf("gh-%s-%s", owner, repo)
	raw = strings.ToLower(raw)
	raw = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return '-'
	}, raw)
	if len(raw) > 32 {
		raw = raw[:32]
	}
	raw = strings.TrimRight(raw, "-")
	return raw
}
