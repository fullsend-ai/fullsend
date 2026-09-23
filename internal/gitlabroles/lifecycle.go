package gitlabroles

import (
	"fmt"
	"strings"
	"time"
)

// DefaultRotationLead is how far ahead of expiry a role credential is
// treated as due for rotation. GitLab PATs expire in at most one year.
const DefaultRotationLead = 30 * 24 * time.Hour

// IdempotentRotationWindow is how long a just-distributed replacement
// is treated as the current credential for concurrent/retry attempts.
const IdempotentRotationWindow = 5 * time.Minute

// DefaultRotationGrace is how long the previous PAT stays active after
// a successful distribution so in-flight jobs that already hold it in
// their environment can finish.
const DefaultRotationGrace = 24 * time.Hour

// LifecycleState is presence plus expiry/revocation for one role.
type LifecycleState string

const (
	// LifecycleUnknown means no token inventory was supplied.
	LifecycleUnknown LifecycleState = ""
	// LifecycleOK is a present secret with a matching active PAT that
	// is not inside the rotation lead time.
	LifecycleOK LifecycleState = "ok"
	// LifecycleExpiring is an active PAT whose expiry is within the lead.
	LifecycleExpiring LifecycleState = "expiring"
	// LifecycleExpired is a PAT whose expiry date has passed.
	LifecycleExpired LifecycleState = "expired"
	// LifecycleRevoked is a listed PAT that is inactive, or every
	// matching PAT is inactive, while the CI secret is still present.
	LifecycleRevoked LifecycleState = "revoked"
	// LifecycleUnverified is a present secret with no matching project
	// access token (enrolled PAT, or a token GitLab no longer lists).
	LifecycleUnverified LifecycleState = "unverified"
	// LifecycleOverlapping means two or more active PATs share this
	// role's token name: a replacement has been distributed and the
	// previous credential remains usable for in-flight jobs.
	LifecycleOverlapping LifecycleState = "overlapping"
	// LifecycleUnconfigured is a registered role whose secret is absent.
	LifecycleUnconfigured LifecycleState = "unconfigured"
)

// TokenSnapshot is non-secret metadata for one GitLab project access
// token. Token values must never be stored here.
type TokenSnapshot struct {
	ID        int
	Name      string
	Active    bool
	ExpiresAt string
	Revoked   bool
}

// DiagnoseLifecycle extends Diagnose with expiry, revocation, and
// overlapping-token classification. A nil tokens slice means inventory
// was not supplied and leaves Lifecycle empty; a non-nil empty slice is a
// successful empty inventory and classifies configured roles as unverified.
// lead of 0
// uses DefaultRotationLead. now zero uses time.Now in UTC.
//
// Diagnostics still carry names, dates, and token IDs only — never
// secret values. Runtime authentication failures stay ErrAuthFailed
// and never fall back to the shared token.
func DiagnoseLifecycle(mode Mode, present map[string]bool, reg Registry, tokens []TokenSnapshot, now time.Time, lead time.Duration) Report {
	rep := Diagnose(mode, present, reg)
	if tokens == nil {
		return rep
	}
	if lead <= 0 {
		lead = DefaultRotationLead
	}
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	byName := make(map[string][]TokenSnapshot, len(tokens))
	for _, tok := range tokens {
		if tok.Name == "" {
			continue
		}
		byName[tok.Name] = append(byName[tok.Name], tok)
	}
	tokenNameByRole := make(map[Role]string, len(rep.Roles))
	reuseOfByRole := make(map[Role]Role, len(rep.Roles))
	for _, rr := range rep.Roles {
		if rr.TokenName != "" {
			tokenNameByRole[rr.Name] = rr.TokenName
		}
		if rr.ReuseOf != "" {
			reuseOfByRole[rr.Name] = rr.ReuseOf
		}
	}
	for i := range rep.Roles {
		rr := &rep.Roles[i]
		name := rr.TokenName
		if name == "" && rr.ReuseOf != "" {
			name = tokenNameForRole(rr.ReuseOf, tokenNameByRole, reuseOfByRole)
		}
		if rr.State == RoleStateUnconfigured {
			rr.Lifecycle = LifecycleUnconfigured
			continue
		}
		annotateRoleLifecycle(rr, byName[name], now, lead)
	}
	rep.Diagnostics = append(rep.Diagnostics, lifecycleMessages(rep)...)
	return rep
}

func tokenNameForRole(role Role, tokenNameByRole map[Role]string, reuseOfByRole map[Role]Role) string {
	seen := map[Role]bool{}
	for role != "" && !seen[role] {
		seen[role] = true
		if name := tokenNameByRole[role]; name != "" {
			return name
		}
		role = reuseOfByRole[role]
	}
	return ""
}

// RoleDueForRotation reports whether a role should be rotated: expired,
// expiring, revoked, or unverified (recovery). Overlapping is cleanup,
// not a new mint. Unconfigured is provisioning, not rotation.
func RoleDueForRotation(rr RoleReport) bool {
	switch rr.Lifecycle {
	case LifecycleExpiring, LifecycleExpired, LifecycleRevoked, LifecycleUnverified:
		return true
	default:
		return false
	}
}

func annotateRoleLifecycle(rr *RoleReport, matches []TokenSnapshot, now time.Time, lead time.Duration) {
	ids := make([]int, 0, len(matches))
	var active []TokenSnapshot
	var inactive []TokenSnapshot
	for _, tok := range matches {
		ids = append(ids, tok.ID)
		if tok.Active && !tok.Revoked {
			active = append(active, tok)
		} else {
			inactive = append(inactive, tok)
		}
	}
	rr.TokenIDs = ids
	if len(matches) == 0 {
		rr.Lifecycle = LifecycleUnverified
		return
	}
	if len(active) == 0 {
		rr.Lifecycle = LifecycleRevoked
		if exp, ok := latestExpiry(inactive); ok {
			rr.ExpiresAt = exp.Format("2006-01-02")
		}
		return
	}
	if len(active) > 1 {
		rr.Overlapping = true
	}
	current := currentToken(active)
	if exp, ok := parseExpiryDate(current.ExpiresAt); ok {
		rr.ExpiresAt = exp.Format("2006-01-02")
		switch {
		case expiryExpired(exp, now):
			rr.Lifecycle = LifecycleExpired
		case expiryExpiring(exp, now, lead):
			rr.Lifecycle = LifecycleExpiring
		case rr.Overlapping:
			rr.Lifecycle = LifecycleOverlapping
		default:
			rr.Lifecycle = LifecycleOK
		}
		return
	}
	if rr.Overlapping {
		rr.Lifecycle = LifecycleOverlapping
		return
	}
	rr.Lifecycle = LifecycleOK
}

func currentToken(active []TokenSnapshot) TokenSnapshot {
	best := active[0]
	bestExp, bestHas := parseExpiryDate(best.ExpiresAt)
	for _, tok := range active[1:] {
		exp, ok := parseExpiryDate(tok.ExpiresAt)
		switch {
		case ok && bestHas && exp.After(bestExp):
			best, bestExp, bestHas = tok, exp, true
		case ok && !bestHas:
			best, bestExp, bestHas = tok, exp, true
		case !ok && !bestHas && tok.ID > best.ID:
			best = tok
		}
	}
	return best
}

func latestExpiry(tokens []TokenSnapshot) (time.Time, bool) {
	var best time.Time
	found := false
	for _, tok := range tokens {
		exp, ok := parseExpiryDate(tok.ExpiresAt)
		if !ok {
			continue
		}
		if !found || exp.After(best) {
			best = exp
			found = true
		}
	}
	return best, found
}

func parseExpiryDate(raw string) (time.Time, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func expiryExpired(exp, now time.Time) bool {
	nowDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return nowDay.After(exp)
}

func expiryExpiring(exp, now time.Time, lead time.Duration) bool {
	if expiryExpired(exp, now) {
		return false
	}
	nowDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return !exp.After(nowDay.Add(lead))
}

func lifecycleMessages(rep Report) []string {
	var msgs []string
	for _, rr := range rep.Roles {
		if rr.Lifecycle == "" || rr.Lifecycle == LifecycleUnconfigured || rr.Lifecycle == LifecycleOK {
			continue
		}
		label := string(rr.Name)
		if rr.Kind == RoleKindCustom {
			label += " (custom)"
		}
		switch rr.Lifecycle {
		case LifecycleExpiring:
			msgs = append(msgs, fmt.Sprintf("%s: expiring (%s) expires %s", label, rr.SecretName, rr.ExpiresAt))
		case LifecycleExpired:
			msgs = append(msgs, fmt.Sprintf("%s: expired (%s) expired %s", label, rr.SecretName, rr.ExpiresAt))
		case LifecycleRevoked:
			msgs = append(msgs, fmt.Sprintf("%s: revoked or inactive (%s)", label, rr.SecretName))
		case LifecycleUnverified:
			msgs = append(msgs, fmt.Sprintf("%s: secret present but no matching project access token (%s)", label, rr.SecretName))
		case LifecycleOverlapping:
			msgs = append(msgs, fmt.Sprintf("%s: overlapping replacement in place (%s); previous credential remains usable for in-flight jobs", label, rr.SecretName))
		}
	}
	return msgs
}

// RefreshLifecycleDiagnostics rebuilds the lifecycle portion of a report's
// diagnostics after callers change a role lifecycle based on trusted proof.
// oldCount is the lifecycle-message count before those changes. DiagnoseLifecycle
// appends lifecycle messages after the base diagnosis, so trimming that count
// preserves the base diagnostics.
func RefreshLifecycleDiagnostics(rep *Report, oldCount int) {
	if rep == nil {
		return
	}
	if oldCount >= 0 && oldCount <= len(rep.Diagnostics) {
		rep.Diagnostics = rep.Diagnostics[:len(rep.Diagnostics)-oldCount]
	}
	rep.Diagnostics = append(rep.Diagnostics, lifecycleMessages(*rep)...)
}
