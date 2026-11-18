package mintcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Wire contract of the GitHub bot-role lookup (ADR 0107). The client side
// lives in internal/botidentity (mint.go); this module cannot import it, so
// the names are duplicated here. Change both together.
const (
	botRolePath = "/v1/bot-role"

	botRoleOutcomeRecognized = "recognized"
	botRoleOutcomeNoMatch    = "no_match"

	// maxBotRoleBodyBytes bounds the request body and the GitHub /apps
	// response body read by the lookup.
	maxBotRoleBodyBytes = 64 << 10

	// A transient GitHub failure (transport error, 5xx, rate limit) of the
	// /apps or installation lookup is retried at most botRoleMaxAttempts
	// times in all. Each wait honors Retry-After (or, for an exhausted
	// primary quota, X-RateLimit-Reset) and falls back to a doubling
	// backoff; a wait over botRoleMaxWait, or one the request deadline
	// cannot cover, ends the retries with the failure instead.
	botRoleMaxAttempts = 3
	botRoleMaxWait     = 5 * time.Second
	botRoleBackoffBase = 500 * time.Millisecond
)

var (
	// botRoleNowFn returns the current time for the retry logic. Override
	// in tests to freeze or advance the clock.
	botRoleNowFn = time.Now
	// botRoleSleepFn waits d or until ctx is done between retry attempts.
	// Override in tests to record or skip the wait instead of sleeping.
	botRoleSleepFn = func(ctx context.Context, d time.Duration) error {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
)

// botLoginPattern matches a GitHub App bot login: the App slug followed by
// the literal "[bot]" suffix. Matching is case-insensitive.
var botLoginPattern = regexp.MustCompile(`(?i)^[a-z0-9](?:[a-z0-9-]{0,98}[a-z0-9])?\[bot\]$`)

// errBotRoleNoMatch signals a completed lookup that found no registered role
// for the bot on the target repository.
var errBotRoleNoMatch = errors.New("no registered bot role")

// errAppsAuthRejected signals that GitHub rejected the installation token
// used to authenticate the GET /apps/{slug} lookup (401 or 403).
var errAppsAuthRejected = errors.New("GitHub rejected the App credential")

// botRoleRequest is the JSON body of POST /v1/bot-role. Unknown fields
// (for example a caller-supplied "role") are ignored: the role is always
// derived from the mint's own registration, never from the request.
type botRoleRequest struct {
	ActorLogin string `json:"actor_login"`
	Repo       string `json:"repo"`
}

// botRoleResponse is the 200 body of POST /v1/bot-role.
type botRoleResponse struct {
	Outcome string `json:"outcome"`
	Role    string `json:"role,omitempty"`
}

// githubAppResponse is the subset of GET /apps/{slug} the lookup reads.
type githubAppResponse struct {
	ID *int64 `json:"id"`
}

// handleBotRole serves POST /v1/bot-role: it resolves a GitHub App bot login
// to the mint role registered for that App, scoped to the caller's own
// repository (ADR 0107). It answers with role names only, never credentials.
func (h *Handler) handleBotRole(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBotRoleBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	var req botRoleRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !botLoginPattern.MatchString(req.ActorLogin) {
		writeError(w, http.StatusBadRequest, "invalid actor_login")
		return
	}
	if !validBotRoleRepo(req.Repo) {
		writeError(w, http.StatusBadRequest, "invalid repo")
		return
	}

	claims, oidcErr := h.verifyOIDCRequest(r.Context(), r)
	if oidcErr != nil {
		log.Printf("bot-role authentication failed: %v", oidcErr)
		writeError(w, http.StatusUnauthorized, "authentication failed")
		return
	}

	// Target binding: a caller may only ask about its own repository.
	if !strings.EqualFold(req.Repo, claims.Repository) {
		log.Printf("bot-role denied: caller_repo=%s requested_repo=%s actor_login=%s",
			claims.Repository, req.Repo, req.ActorLogin)
		writeError(w, http.StatusForbidden, "repo must be the requesting repository")
		return
	}

	// Scope the installation lookup to the authoritative OIDC repository
	// claim (equal to req.Repo up to case).
	owner, name, _ := strings.Cut(claims.Repository, "/")
	role, err := h.resolveBotRole(r.Context(), req.ActorLogin, owner, name)
	switch {
	case errors.Is(err, errBotRoleNoMatch):
		log.Printf("bot-role lookup: caller_repo=%s actor_login=%s outcome=%s role=",
			claims.Repository, req.ActorLogin, botRoleOutcomeNoMatch)
		writeBotRoleResponse(w, botRoleResponse{Outcome: botRoleOutcomeNoMatch})
	case err != nil:
		log.Printf("bot-role lookup: caller_repo=%s actor_login=%s outcome=error role= err=%v",
			claims.Repository, req.ActorLogin, err)
		writeError(w, http.StatusBadGateway, "bot-role lookup failed")
	default:
		log.Printf("bot-role lookup: caller_repo=%s actor_login=%s outcome=%s role=%s",
			claims.Repository, req.ActorLogin, botRoleOutcomeRecognized, role)
		writeBotRoleResponse(w, botRoleResponse{Outcome: botRoleOutcomeRecognized, Role: role})
	}
}

// validBotRoleRepo reports whether repo is a valid "owner/name" reference.
func validBotRoleRepo(repo string) bool {
	owner, name, found := strings.Cut(repo, "/")
	return found && ValidateOrgName(owner) == nil &&
		RepoNamePattern.MatchString(name) && !strings.Contains(name, "..")
}

// resolveBotRole maps a validated bot login to the registered role whose
// GitHub App is installed on owner/name. It returns errBotRoleNoMatch for a
// completed lookup with no match; any other error is a lookup failure.
func (h *Handler) resolveBotRole(ctx context.Context, actorLogin, owner, name string) (string, error) {
	slug := strings.TrimSuffix(strings.ToLower(actorLogin), "[bot]")

	// An invalid role registration is a configuration failure, not a
	// completed negative lookup: fail before any GitHub call.
	_, cfgSpan := startSpan(ctx, "mint.bot_role_config", stageBotRoleConfig)
	cfgErr := h.validateRoleAppIDs()
	if cfgErr != nil {
		cfgSpan.recordError(sanitizedErr("invalid role registration"))
	}
	cfgSpan.end()
	if cfgErr != nil {
		return "", cfgErr
	}

	// GET /apps/{slug} is public, but anonymous calls share GitHub's
	// per-IP rate limit, which the mint's shared egress addresses exhaust.
	// GitHub documents installation access tokens for this endpoint (an App
	// JWT is not accepted: it answers 401), so authenticate it with a
	// metadata-only installation token of one of the mint's own Apps on the
	// target repo, which is counted against that installation instead. A
	// failure to obtain that token, or a rejection of it by GitHub (401, or a
	// 403 that is not a rate limit), only degrades to the anonymous call. A
	// rate limit never does: the anonymous quota is the scarcer one, so the
	// lookup waits (bounded) and then fails.
	var appsJWT, appsJWTRole, appsToken string
	if authRole := h.firstAllowedRole(); authRole != "" {
		jwt, err := h.tracedAppJWT(ctx, authRole, false)
		if err != nil {
			log.Printf("bot-role lookup: authenticating /apps lookup as role %q failed, using anonymous request: %v", authRole, err)
		} else {
			appsJWT, appsJWTRole = jwt, authRole
			appsToken, err = h.tracedAppsToken(ctx, jwt, owner, name)
			if err != nil {
				log.Printf("bot-role lookup: no installation token for /apps lookup as role %q, using anonymous request: %v", authRole, err)
				appsToken = ""
			}
		}
	}

	lookupAppID := func(token string) (id string, err error) {
		_, sp := startSpan(ctx, "mint.github_apps_lookup", stageAppsLookup)
		auth := "anonymous"
		if token != "" {
			auth = "installation_token"
		}
		sp.setStr("fullsend.bot_role.apps_auth", auth)
		lt := &lookupTrace{span: sp}
		err = retryGitHubLookup(ctx, "/apps lookup", lt, func() (e error) {
			id, e = h.lookupGitHubAppID(ctx, slug, token)
			return e
		})
		lt.finish(err)
		return id, err
	}

	appID, err := lookupAppID(appsToken)
	if errors.Is(err, errAppsAuthRejected) {
		// A token that GitHub rejects (revoked, suspended installation)
		// must not gate every lookup. Retry anonymously and drop the
		// credential so the installation check below uses the matched
		// role's own.
		log.Printf("bot-role lookup: /apps lookup authenticated as role %q was rejected, using anonymous request: %v", appsJWTRole, err)
		appsJWT, appsJWTRole, appsToken = "", "", ""
		appID, err = lookupAppID("")
	}
	if err != nil {
		return "", err
	}

	role := h.roleForAppID(appID)
	if role == "" {
		return "", errBotRoleNoMatch
	}

	// Target scoping: the role's App must be installed on the target repo.
	jwt := appsJWT
	if role != appsJWTRole {
		jwt, err = h.tracedAppJWT(ctx, role, true)
		if err != nil {
			return "", err
		}
	}
	_, instSpan := startSpan(ctx, "mint.installation_lookup", stageInstallationLookup)
	instTrace := &lookupTrace{span: instSpan}
	instErr := retryGitHubLookup(ctx, "installation lookup", instTrace, func() error {
		_, err := FindInstallation(ctx, h.githubBaseURL, jwt, owner, name)
		return err
	})
	instTrace.finish(instErr)
	if err := instErr; err != nil {
		if errors.Is(err, ErrInstallationNotFound) {
			return "", errBotRoleNoMatch
		}
		return "", fmt.Errorf("finding installation for role %q: %v", role, err)
	}
	return role, nil
}

// tracedAppJWT is appJWT inside a span. A failure is attributed to the
// request only when required: a failure to authenticate the /apps lookup
// merely degrades it to an anonymous call, which the span records as a skip.
func (h *Handler) tracedAppJWT(ctx context.Context, role string, required bool) (string, error) {
	_, sp := startSpan(ctx, "mint.bot_role_credentials", stagePEMAccess)
	jwt, err := h.appJWT(ctx, role)
	sp.setBool("fullsend.bot_role.credential_required", required)
	switch {
	case err == nil:
		sp.setStr("fullsend.bot_role.result", "ok")
	case required:
		step, text := credentialFailure(err)
		sp.setStr("fullsend.bot_role.result", "error")
		sp.recordErrorAt(step, sanitizedErr(text))
	default:
		step, _ := credentialFailure(err)
		sp.setStr("fullsend.bot_role.result", "unavailable")
		sp.setStr("fullsend.bot_role.failure_step", step)
		sp.setStr("fullsend.bot_role.skip_reason", "credential_unavailable_using_anonymous")
	}
	sp.end()
	return jwt, err
}

// tracedAppsToken exchanges an App JWT for the metadata-only installation
// token that authenticates the /apps lookup, inside a span. Any failure,
// including the App not being installed on the target repo, only degrades the
// lookup to an anonymous call, which the span records as a skip.
func (h *Handler) tracedAppsToken(ctx context.Context, jwt, owner, name string) (string, error) {
	_, sp := startSpan(ctx, "mint.bot_role_apps_token", stageTokenIssuance)
	token, step, err := h.appsLookupToken(ctx, jwt, owner, name)
	if err != nil {
		sp.setStr("fullsend.bot_role.result", "unavailable")
		sp.setStr("fullsend.bot_role.failure_step", step)
		sp.setStr("fullsend.bot_role.skip_reason", "apps_token_unavailable_using_anonymous")
	} else {
		sp.setStr("fullsend.bot_role.result", "ok")
	}
	sp.end()
	return token, err
}

// appsLookupPermissions is all the /apps lookup needs: every GitHub App
// installation is granted metadata read, and the token reads nothing else.
var appsLookupPermissions = map[string]string{"metadata": "read"}

// appsLookupToken finds the installation of the App behind jwt on owner/name
// and creates an installation token for it, limited to that repository and to
// appsLookupPermissions. On failure it also returns the stage that failed.
func (h *Handler) appsLookupToken(ctx context.Context, jwt, owner, name string) (string, string, error) {
	instID, err := FindInstallation(ctx, h.githubBaseURL, jwt, owner, name)
	if err != nil {
		return "", stageInstallationLookup, err
	}
	token, err := createInstallationTokenWithPermissions(ctx, h.githubBaseURL, jwt, instID, appsLookupPermissions, []string{name})
	if err != nil {
		return "", stageTokenIssuance, err
	}
	return token, "", nil
}

// appJWT returns a signed GitHub App JWT for role.
func (h *Handler) appJWT(ctx context.Context, role string) (string, error) {
	roleAppID, err := h.lookupRoleAppID(role)
	if err != nil {
		return "", &credentialError{step: stageBotRoleConfig, text: "App ID registration unusable",
			err: fmt.Errorf("looking up app ID for role %q: %v", role, err)}
	}
	pemData, err := h.pemAccessor.AccessPEM(ctx, role)
	if err != nil {
		return "", &credentialError{step: stagePEMAccess, text: "PEM access failed",
			err: fmt.Errorf("reading PEM secret for role %q: %v", role, err)}
	}
	defer func() {
		for i := range pemData {
			pemData[i] = 0
		}
	}()
	jwt, err := GenerateAppJWT(roleAppID, pemData)
	if err != nil {
		return "", &credentialError{step: stageGitHubAppAuth, text: "App JWT generation failed",
			err: fmt.Errorf("generating app JWT for role %q: %v", role, err)}
	}
	return jwt, nil
}

// firstAllowedRole returns the lexicographically first allowed role with a
// configured App ID, or "" when there is none.
func (h *Handler) firstAllowedRole() string {
	first := ""
	for role := range h.roleAppIDs {
		if !h.checkAllowedRole(role) {
			continue
		}
		if _, err := h.lookupRoleAppID(role); err != nil {
			continue
		}
		if first == "" || role < first {
			first = role
		}
	}
	return first
}

// validateRoleAppIDs reports an error when an allowed role has no configured
// App ID or one that is not a positive decimal integer. Such a registration
// could never match GitHub's numeric App ID, so the lookup must fail rather
// than answer no_match. The check runs per lookup, not in NewHandler, so it
// does not change which configurations /v1/token accepts.
func (h *Handler) validateRoleAppIDs() error {
	for role := range h.roleAppIDs {
		if !h.checkAllowedRole(role) {
			continue
		}
		id, err := h.lookupRoleAppID(role)
		if err != nil {
			return fmt.Errorf("invalid app ID registration for role %q: %v", role, err)
		}
		if n, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64); err != nil || n <= 0 {
			return fmt.Errorf("invalid app ID registration for role %q: not a positive integer", role)
		}
	}
	return nil
}

// roleForAppID returns the allowed role whose registered App ID equals appID
// (compared as canonical decimal integers when the registered value is
// numeric, so "00300" matches 300), or "" when none does. App IDs are
// resolved with lookupRoleAppID, the same mapping /v1/token uses. When several
// allowed roles share the App ID (for example fix reuses the coder App), the
// lexicographically first role name wins so the answer is deterministic.
func (h *Handler) roleForAppID(appID string) string {
	match := ""
	for role := range h.roleAppIDs {
		if !h.checkAllowedRole(role) {
			continue
		}
		id, err := h.lookupRoleAppID(role)
		if err != nil || canonicalAppID(id) != appID {
			continue
		}
		if match == "" || role < match {
			match = role
		}
	}
	return match
}

// canonicalAppID returns id as a canonical decimal integer when it parses as
// a positive one, so a registered "00300" compares equal to GitHub's 300. Any
// other value is returned unchanged and therefore never equals GitHub's ID.
func canonicalAppID(id string) string {
	n, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64)
	if err != nil || n <= 0 {
		return id
	}
	return strconv.FormatInt(n, 10)
}

// lookupGitHubAppID resolves an App slug to its numeric App ID via GitHub's
// GET /apps/{slug} endpoint. A non-empty token (an installation access token)
// authenticates the request; it never changes the answer. A 404 is a completed no-match.
func (h *Handler) lookupGitHubAppID(ctx context.Context, slug, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.githubBaseURL+"/apps/"+slug, nil)
	if err != nil {
		return "", fmt.Errorf("creating app request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", githubUserAgent())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := mintHTTP(req)
	if err != nil {
		return "", &githubStatusError{msg: fmt.Sprintf("getting app %q: %v", slug, err), err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return "", errBotRoleNoMatch
	}
	if resp.StatusCode != http.StatusOK {
		statusErr := newGitHubStatusError(resp, fmt.Sprintf("getting app %q returned status %d", slug, resp.StatusCode))
		// A 403 is rejected credentials only when it is not a rate limit.
		if token != "" && !statusErr.RateLimited &&
			(resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
			return "", fmt.Errorf("%w: %w", statusErr, errAppsAuthRejected)
		}
		return "", statusErr
	}

	var app githubAppResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBotRoleBodyBytes)).Decode(&app); err != nil {
		return "", &malformedAppError{msg: fmt.Sprintf("decoding app %q: %v", slug, err)}
	}
	if app.ID == nil || *app.ID <= 0 {
		return "", &malformedAppError{msg: fmt.Sprintf("app %q response has no id", slug)}
	}
	return strconv.FormatInt(*app.ID, 10), nil
}

// retryGitHubLookup runs call, repeating it after a bounded wait while it
// fails with a transient githubStatusError (see the botRoleMaxAttempts
// constants). Any other error, including errBotRoleNoMatch and
// errAppsAuthRejected, is returned at once. Every attempt and the decision
// taken after it is recorded in lt (see bot_role_trace.go).
func retryGitHubLookup(ctx context.Context, what string, lt *lookupTrace, call func() error) error {
	for attempt := 1; ; attempt++ {
		err := call()
		var statusErr *githubStatusError
		isStatus := errors.As(err, &statusErr)

		var wait time.Duration
		hasWait := false
		var decision string
		switch {
		case err == nil:
			decision = retrySucceeded
		case !isStatus || !statusErr.Transient():
			decision = retryNotRetryable
		case ctx.Err() != nil:
			decision = retryContextDone
		case attempt >= botRoleMaxAttempts:
			decision = retryAttemptsExhausted
		default:
			var ok bool
			wait, ok = statusErr.retryWait(botRoleNowFn(), attempt)
			hasWait = true
			switch deadline, has := ctx.Deadline(); {
			case !ok:
				decision = retryWaitExceedsBound
			case has && wait >= deadline.Sub(botRoleNowFn()):
				decision = retryWaitExceedsDeadline
			default:
				decision = retryRetrying
			}
		}
		lt.record(attempt, err, decision, wait, hasWait)

		switch decision {
		case retryRetrying:
		case retryWaitExceedsBound, retryWaitExceedsDeadline:
			log.Printf("bot-role lookup: %s failed and the wait it needs exceeds the retry bound, not retrying: %v", what, err)
			return err
		default:
			return err
		}
		log.Printf("bot-role lookup: %s failed (attempt %d/%d), retrying in %s: %v", what, attempt, botRoleMaxAttempts, wait, err)
		if sleepErr := botRoleSleepFn(ctx, wait); sleepErr != nil {
			lt.decision = retrySleepInterrupted
			return err
		}
	}
}

// retryWait is how long to wait before repeating the failed call, and false
// when that is longer than botRoleMaxWait. Retry-After wins; otherwise an
// exhausted primary quota waits for its reset; otherwise the wait doubles
// from botRoleBackoffBase.
func (e *githubStatusError) retryWait(now time.Time, attempt int) (time.Duration, bool) {
	var wait time.Duration
	switch {
	case e.RetryAfter > 0:
		wait = e.RetryAfter
	case e.RateLimited && e.Exhausted && !e.ResetAt.IsZero():
		// The extra second covers clock skew against GitHub's reset time.
		wait = max(e.ResetAt.Sub(now), 0) + time.Second
	default:
		wait = botRoleBackoffBase << (attempt - 1)
	}
	return wait, wait <= botRoleMaxWait
}

func writeBotRoleResponse(w http.ResponseWriter, resp botRoleResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}
