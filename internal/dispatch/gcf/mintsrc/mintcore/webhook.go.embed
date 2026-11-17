package mintcore

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GitHub webhook route that wakes a repository's fork PR review poll
// (ADR 0171). The route only sends one workflow_dispatch of the poll
// workflow with no inputs; the poll decides everything else exactly as on
// a scheduled tick, so no webhook field reaches a job.
const (
	webhookPath = "/v1/webhooks/github"

	// webhookSecretEnv names the HMAC secret shared with the orchestrator
	// App's webhook. The route is off (404) while it is unset or while
	// ALLOWED_ROLES does not allow webhookRole.
	webhookSecretEnv = "FULLSEND_WEBHOOK_SECRET"

	// maxWebhookBodyBytes bounds the webhook body the route reads.
	maxWebhookBodyBytes = 1 << 20

	// webhookRole is the mint role whose GitHub App (the orchestrator App)
	// sends the webhook and dispatches the poll.
	webhookRole = "fullsend"

	// pollWorkflowFile is the workflow the route dispatches.
	pollWorkflowFile = "fullsend-poll-prs.yml"

	webhookSignaturePrefix = "sha256="

	// webhookDebounce is how long, after a dispatch for a repository,
	// further deliveries for it are answered without a GitHub call. One
	// more dispatch would add nothing: the poll's concurrency group keeps
	// at most one queued run, and every run scans every open PR.
	webhookDebounce = 30 * time.Second

	// webhookNegativeTTL is how long app_not_installed and
	// workflow_not_dispatchable are remembered for a repository.
	webhookNegativeTTL = 5 * time.Minute

	// webhookLimiterMaxEntries bounds the repositories webhookLimiter
	// remembers.
	webhookLimiterMaxEntries = 10000
)

// Outcomes logged for each webhook request and returned as the reason of a
// dropped or undispatched delivery.
const (
	webhookOutcomeDispatched       = "dispatched"
	webhookOutcomeNotDispatchable  = "workflow_not_dispatchable"
	webhookOutcomeNotInstalled     = "app_not_installed"
	webhookOutcomePing             = "ping"
	webhookOutcomeNotOurApp        = "not_orchestrator_app"
	webhookOutcomeEventIgnored     = "event_ignored"
	webhookOutcomeActionIgnored    = "action_ignored"
	webhookOutcomeNotFork          = "not_fork"
	webhookOutcomeInvalidRepo      = "invalid_repository"
	webhookOutcomeNotEnrolled      = "repo_not_enrolled"
	webhookOutcomeDebounced        = "debounced"
	webhookOutcomeBadSignature     = "invalid_signature"
	webhookOutcomeTooLarge         = "body_too_large"
	webhookOutcomeBadBody          = "invalid_body"
	webhookOutcomeMethodNotAllowed = "method_not_allowed"
	webhookOutcomeCredentialError  = "credential_error"
	webhookOutcomeGitHubError      = "github_error"
)

// webhookDispatchPermissions is all a workflow_dispatch needs. /v1/token
// never serves this permission set: the token exists only inside one
// webhook request.
var webhookDispatchPermissions = map[string]string{"actions": "write", "metadata": "read"}

// webhookPRActions are the pull_request actions that can bring new fork
// commits to review.
var webhookPRActions = map[string]bool{
	"opened":           true,
	"synchronize":      true,
	"reopened":         true,
	"ready_for_review": true,
}

// webhookRepo is the subset of a webhook repository object the route reads.
// The payload's default_branch is deliberately not read: a replayed old
// delivery could name a former default branch, so the dispatch ref comes
// from a live GET /repos/{owner}/{repo} instead.
type webhookRepo struct {
	FullName string `json:"full_name"`
}

// webhookPullRequestEvent is the subset of a pull_request webhook payload
// the route reads. Head.Repo is nil when the fork was deleted.
type webhookPullRequestEvent struct {
	Action      string `json:"action"`
	PullRequest struct {
		Head struct {
			Repo *webhookRepo `json:"repo"`
		} `json:"head"`
		Base struct {
			Repo webhookRepo `json:"repo"`
		} `json:"base"`
	} `json:"pull_request"`
}

// webhookLog is the one log line written per webhook request. It never
// holds the body, the signature or a token.
type webhookLog struct {
	delivery, event, action, repo string
}

func (l *webhookLog) write(outcome string, err error) {
	msg := fmt.Sprintf("github webhook: delivery=%q event=%q action=%q repo=%q outcome=%s",
		webhookLogValue(l.delivery), webhookLogValue(l.event),
		webhookLogValue(l.action), webhookLogValue(l.repo), outcome)
	if err != nil {
		msg += " err=" + err.Error()
	}
	log.Print(msg)
}

// webhookLogValue bounds a header or payload value before it is logged.
func webhookLogValue(s string) string {
	const maxLen = 128
	if len(s) > maxLen {
		return s[:maxLen]
	}
	return s
}

// handleGitHubWebhook serves POST /v1/webhooks/github. The caller has
// already checked that the route is configured (webhookSecret non-empty).
func (h *Handler) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	l := &webhookLog{delivery: r.Header.Get("X-GitHub-Delivery")}

	if r.Method != http.MethodPost {
		l.write(webhookOutcomeMethodNotAllowed, nil)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBodyBytes+1))
	if err != nil {
		l.write(webhookOutcomeBadBody, nil)
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	if len(body) > maxWebhookBodyBytes {
		l.write(webhookOutcomeTooLarge, nil)
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}

	// Authentication is the signature only: nothing else is read before it
	// is verified.
	if !validWebhookSignature(h.webhookSecret, body, r.Header.Get("X-Hub-Signature-256")) {
		l.write(webhookOutcomeBadSignature, nil)
		writeError(w, http.StatusUnauthorized, "invalid signature")
		return
	}
	l.event = r.Header.Get("X-GitHub-Event")

	if !h.fromOrchestratorApp(r.Header) {
		l.write(webhookOutcomeNotOurApp, nil)
		writeWebhookDrop(w, webhookOutcomeNotOurApp)
		return
	}

	if l.event == "ping" {
		l.write(webhookOutcomePing, nil)
		writeWebhookResponse(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if l.event != "pull_request" {
		l.write(webhookOutcomeEventIgnored, nil)
		writeWebhookDrop(w, webhookOutcomeEventIgnored)
		return
	}

	var ev webhookPullRequestEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		l.write(webhookOutcomeBadBody, nil)
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	base := ev.PullRequest.Base.Repo
	l.action, l.repo = ev.Action, base.FullName

	if !webhookPRActions[ev.Action] {
		l.write(webhookOutcomeActionIgnored, nil)
		writeWebhookDrop(w, webhookOutcomeActionIgnored)
		return
	}
	// A missing head repository (deleted fork) or one equal to the base
	// repository is not a fork PR.
	head := ev.PullRequest.Head.Repo
	if head == nil || head.FullName == "" || strings.EqualFold(head.FullName, base.FullName) {
		l.write(webhookOutcomeNotFork, nil)
		writeWebhookDrop(w, webhookOutcomeNotFork)
		return
	}
	if !validBotRoleRepo(base.FullName) {
		l.write(webhookOutcomeInvalidRepo, nil)
		writeWebhookDrop(w, webhookOutcomeInvalidRepo)
		return
	}
	if !IsPerRepoMode(base.FullName, h.perRepoWIFRepos) {
		l.write(webhookOutcomeNotEnrolled, nil)
		writeWebhookDrop(w, webhookOutcomeNotEnrolled)
		return
	}
	// Answer from the per-instance limiter when it can, without any GitHub
	// call. A miss reserves the repository, so concurrent deliveries for
	// it are debounced while this one dispatches.
	if reason, ok := h.webhookLimits.reserve(base.FullName); !ok {
		l.write(reason+" source=cache", nil)
		writeWebhookDrop(w, reason)
		return
	}

	owner, name, _ := strings.Cut(base.FullName, "/")
	outcome, status, err := h.dispatchPollWorkflow(r.Context(), owner, name)
	h.webhookLimits.settle(base.FullName, outcome)
	l.write(outcome, err)
	switch status {
	case http.StatusAccepted:
		resp := map[string]any{"dispatched": outcome == webhookOutcomeDispatched}
		if outcome != webhookOutcomeDispatched {
			resp["reason"] = outcome
		}
		writeWebhookResponse(w, http.StatusAccepted, resp)
	case http.StatusBadGateway:
		writeError(w, http.StatusBadGateway, "dispatch failed")
	default:
		writeError(w, http.StatusInternalServerError, "dispatch failed")
	}
}

// validWebhookSignature reports whether header is "sha256=" followed by the
// hex HMAC-SHA256 of body keyed with secret.
func validWebhookSignature(secret, body []byte, header string) bool {
	sigHex, ok := strings.CutPrefix(header, webhookSignaturePrefix)
	if !ok {
		return false
	}
	got, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// fromOrchestratorApp reports whether the webhook was sent for the GitHub
// App registered for webhookRole in ROLE_APP_IDS.
func (h *Handler) fromOrchestratorApp(header http.Header) bool {
	if header.Get("X-GitHub-Hook-Installation-Target-Type") != "integration" {
		return false
	}
	appID, err := h.lookupRoleAppID(webhookRole)
	if err != nil {
		return false
	}
	target, err := strconv.ParseInt(strings.TrimSpace(header.Get("X-GitHub-Hook-Installation-Target-ID")), 10, 64)
	if err != nil || target <= 0 {
		return false
	}
	return strconv.FormatInt(target, 10) == canonicalAppID(appID)
}

// dispatchPollWorkflow sends one workflow_dispatch of pollWorkflowFile on
// the current default branch of owner/name with a token limited to that
// repository and to webhookDispatchPermissions. It reads the default
// branch from GitHub with the same token rather than trusting the payload.
// It returns the outcome to log and the status class to answer with: 202
// for a dispatch or a repository that cannot take one, 502 for any other
// GitHub failure, 500 for a mint credential failure. The token is never
// returned, logged or kept beyond this call.
func (h *Handler) dispatchPollWorkflow(ctx context.Context, owner, name string) (string, int, error) {
	jwt, err := h.appJWT(ctx, webhookRole)
	if err != nil {
		return webhookOutcomeCredentialError, http.StatusInternalServerError, err
	}
	instID, err := FindInstallation(ctx, h.githubBaseURL, jwt, owner, name)
	if errors.Is(err, ErrInstallationNotFound) {
		return webhookOutcomeNotInstalled, http.StatusAccepted, nil
	}
	if err != nil {
		return webhookOutcomeGitHubError, http.StatusBadGateway, err
	}
	token, err := createInstallationTokenWithPermissions(ctx, h.githubBaseURL, jwt, instID, webhookDispatchPermissions, []string{name})
	if err != nil {
		return webhookOutcomeGitHubError, http.StatusBadGateway, err
	}

	ref, status, err := getDefaultBranch(ctx, h.githubBaseURL, token, owner, name)
	switch {
	case err != nil:
		return webhookOutcomeGitHubError, http.StatusBadGateway, err
	case status == http.StatusNotFound:
		return webhookOutcomeNotDispatchable, http.StatusAccepted, nil
	case status != http.StatusOK:
		return webhookOutcomeGitHubError, http.StatusBadGateway,
			fmt.Errorf("reading repository returned status %d", status)
	case ref == "":
		return webhookOutcomeNotDispatchable, http.StatusAccepted, nil
	}

	status, err = postWorkflowDispatch(ctx, h.githubBaseURL, token, owner, name, ref)
	switch {
	case err != nil:
		return webhookOutcomeGitHubError, http.StatusBadGateway, err
	case status >= 200 && status < 300:
		return webhookOutcomeDispatched, http.StatusAccepted, nil
	case status == http.StatusNotFound || status == http.StatusUnprocessableEntity:
		// No poll workflow, no workflow_dispatch trigger, Actions off or
		// a bad ref: the scheduled poll remains the backstop.
		return webhookOutcomeNotDispatchable, http.StatusAccepted, nil
	default:
		return webhookOutcomeGitHubError, http.StatusBadGateway,
			fmt.Errorf("dispatching %s returned status %d", pollWorkflowFile, status)
	}
}

// postWorkflowDispatch calls POST
// /repos/{owner}/{name}/actions/workflows/{pollWorkflowFile}/dispatches with
// body {"ref": ref} and returns GitHub's status.
func postWorkflowDispatch(ctx context.Context, githubBaseURL, token, owner, name, ref string) (int, error) {
	payload, err := json.Marshal(map[string]string{"ref": ref})
	if err != nil {
		return 0, fmt.Errorf("marshaling dispatch request: %w", err)
	}
	reqURL := fmt.Sprintf("%s/repos/%s/%s/actions/workflows/%s/dispatches", githubBaseURL, owner, name, pollWorkflowFile)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(payload))
	if err != nil {
		return 0, fmt.Errorf("creating dispatch request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", githubUserAgent())

	resp, err := mintHTTP(req)
	if err != nil {
		return 0, fmt.Errorf("dispatching %s: %w", pollWorkflowFile, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, nil
}

// getDefaultBranch calls GET /repos/{owner}/{name} and returns the
// repository's current default branch and GitHub's status. The branch is
// empty unless the status is 200.
func getDefaultBranch(ctx context.Context, githubBaseURL, token, owner, name string) (string, int, error) {
	reqURL := fmt.Sprintf("%s/repos/%s/%s", githubBaseURL, owner, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", 0, fmt.Errorf("creating repository request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", githubUserAgent())

	resp, err := mintHTTP(req)
	if err != nil {
		return "", 0, fmt.Errorf("reading repository: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return "", resp.StatusCode, nil
	}
	var repo struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&repo); err != nil {
		return "", 0, fmt.Errorf("decoding repository: %w", err)
	}
	return repo.DefaultBranch, http.StatusOK, nil
}

// webhookLimiter bounds the GitHub calls the webhook route makes per
// repository. It is best-effort and in-process: its state lives in one
// mint instance (one Cloud Run instance, one Worker isolate, one
// standalone process), is not shared across instances or isolates, and is
// lost on restart. A nil *webhookLimiter limits nothing.
type webhookLimiter struct {
	mu         sync.Mutex
	now        func() time.Time
	maxEntries int
	entries    map[string]webhookLimitEntry
}

// webhookLimitEntry is what the limiter remembers for one repository:
// reason is the answer for deliveries until expires.
type webhookLimitEntry struct {
	reason  string
	expires time.Time
}

func newWebhookLimiter() *webhookLimiter {
	return &webhookLimiter{
		now:        time.Now,
		maxEntries: webhookLimiterMaxEntries,
		entries:    make(map[string]webhookLimitEntry),
	}
}

// reserve reports whether a delivery for repo may call GitHub. When it may
// not, it returns the reason to answer with: debounced after a recent or
// in-flight dispatch, or a remembered app_not_installed or
// workflow_not_dispatchable. When it may, it marks repo as debounced, so
// concurrent deliveries for it do not call GitHub too; settle records the
// result.
func (l *webhookLimiter) reserve(repo string) (string, bool) {
	if l == nil {
		return "", true
	}
	key := strings.ToLower(repo)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if e, ok := l.entries[key]; ok && now.Before(e.expires) {
		return e.reason, false
	}
	l.putLocked(key, webhookLimitEntry{reason: webhookOutcomeDebounced, expires: now.Add(webhookDebounce)}, now)
	return "", true
}

// settle records the outcome of the dispatch reserve allowed for repo. A
// dispatch debounces repo for webhookDebounce, app_not_installed and
// workflow_not_dispatchable are remembered for webhookNegativeTTL, and any
// failure clears the reservation so a redelivery calls GitHub again.
func (l *webhookLimiter) settle(repo, outcome string) {
	if l == nil {
		return
	}
	key := strings.ToLower(repo)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	switch outcome {
	case webhookOutcomeDispatched:
		l.putLocked(key, webhookLimitEntry{reason: webhookOutcomeDebounced, expires: now.Add(webhookDebounce)}, now)
	case webhookOutcomeNotInstalled, webhookOutcomeNotDispatchable:
		l.putLocked(key, webhookLimitEntry{reason: outcome, expires: now.Add(webhookNegativeTTL)}, now)
	default:
		delete(l.entries, key)
	}
}

// putLocked stores e for key. When the map is full it first drops expired
// entries and then, if still full, the entry that expires soonest.
func (l *webhookLimiter) putLocked(key string, e webhookLimitEntry, now time.Time) {
	if _, ok := l.entries[key]; !ok && len(l.entries) >= l.maxEntries {
		for k, v := range l.entries {
			if !now.Before(v.expires) {
				delete(l.entries, k)
			}
		}
		if len(l.entries) >= l.maxEntries {
			oldest, first := "", true
			for k, v := range l.entries {
				if first || v.expires.Before(l.entries[oldest].expires) {
					oldest, first = k, false
				}
			}
			delete(l.entries, oldest)
		}
	}
	l.entries[key] = e
}

// writeWebhookDrop answers a signed delivery the route does not act on.
func writeWebhookDrop(w http.ResponseWriter, reason string) {
	writeWebhookResponse(w, http.StatusAccepted, map[string]any{"dispatched": false, "reason": reason})
}

func writeWebhookResponse(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}
