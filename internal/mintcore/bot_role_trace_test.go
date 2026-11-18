package mintcore

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// traceBotRole sends one bot-role request through a traced handler and
// returns the exported spans and the raw exported payloads.
func traceBotRole(t *testing.T, env *testOIDCEnv, body string) (*testCollector, []otlpSpan) {
	t.Helper()
	collector := newTestCollector(t, http.StatusOK, 0)
	env.handler.SetTracer(mustNewTracer(t, collector.server.URL, 0))
	token := env.signToken(t, nil)
	postBotRole(t, env, token, body)
	return collector, collector.spans()
}

func mustSpan(t *testing.T, spans []otlpSpan, name string) *otlpSpan {
	t.Helper()
	s := spanByName(spans, name)
	if s == nil {
		t.Fatalf("span %q not exported; got %d spans", name, len(spans))
	}
	return s
}

func wantAttr(t *testing.T, s *otlpSpan, key, want string) {
	t.Helper()
	if got, ok := attr(s, key); !ok || got != want {
		t.Errorf("span %q attr %s = %q (present=%v), want %q", s.Name, key, got, ok, want)
	}
}

func wantNoFailureStage(t *testing.T, s *otlpSpan) {
	t.Helper()
	if got, ok := attr(s, "fullsend.mint.failure_stage"); ok {
		t.Errorf("span %q carries failure stage %q, want none", s.Name, got)
	}
}

func eventAttr(e otlpEvent, key string) string {
	v, _ := attr(&otlpSpan{Attributes: e.Attributes}, key)
	return v
}

// assertNoIdentifiers fails when the exported payloads carry anything the
// traces must exclude: the actor, App slug, repository, owner or a token.
func assertNoIdentifiers(t *testing.T, c *testCollector, token string) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, raw := range c.raw {
		for _, banned := range []string{"fullsend-review", "dependabot", "test-org", "test-repo", "Bearer", token} {
			if banned != "" && strings.Contains(raw, banned) {
				t.Errorf("exported trace contains %q: %s", banned, raw)
			}
		}
	}
}

func TestBotRoleTrace_RouteIsKnownAndStagesAreSpans(t *testing.T) {
	env := newBotRoleEnv(t, botRoleGitHub{
		apps:     map[string]botRoleFakeResponse{"fullsend-review": {http.StatusOK, `{"id":300}`}},
		installs: map[string]int{"test-org/test-repo": http.StatusOK},
	}, "review", "coder")
	c, spans := traceBotRole(t, env, botRoleBody)

	root := mustSpan(t, spans, "POST /v1/bot-role")
	wantAttr(t, root, "http.response.status_code", "200")
	if spanByName(spans, "POST other") != nil {
		t.Errorf("bot-role request recorded under the catch-all route")
	}
	wantNoFailureStage(t, mustSpan(t, spans, "mint.bot_role_config"))
	creds := mustSpan(t, spans, "mint.bot_role_credentials")
	wantAttr(t, creds, "fullsend.bot_role.result", "ok")
	wantAttr(t, mustSpan(t, spans, "mint.bot_role_apps_token"), "fullsend.bot_role.result", "ok")
	apps := mustSpan(t, spans, "mint.github_apps_lookup")
	wantAttr(t, apps, "fullsend.bot_role.apps_auth", "installation_token")
	wantAttr(t, apps, "fullsend.bot_role.attempts", "1")
	wantAttr(t, apps, "fullsend.bot_role.result", "ok")
	inst := mustSpan(t, spans, "mint.installation_lookup")
	wantAttr(t, inst, "fullsend.bot_role.retry_decision", retrySucceeded)
	assertNoIdentifiers(t, c, "")
}

func TestBotRoleTrace_RetriedAppsLookupRecordsEveryAttempt(t *testing.T) {
	env := newBotRoleEnv(t, botRoleGitHub{}, "review")
	newSequencedGitHub(t, env, []seqResponse{seqBadGateway, seqBadGateway, seqAppOK}, []seqResponse{seqInstallOK})
	c, spans := traceBotRole(t, env, botRoleBody)

	apps := mustSpan(t, spans, "mint.github_apps_lookup")
	wantAttr(t, apps, "fullsend.bot_role.attempts", "3")
	wantAttr(t, apps, "fullsend.bot_role.result", "ok")
	if len(apps.Events) != 3 {
		t.Fatalf("attempt events = %d, want 3", len(apps.Events))
	}
	for i, wantWait := range []time.Duration{botRoleBackoffBase, 2 * botRoleBackoffBase} {
		e := apps.Events[i]
		if got := eventAttr(e, "fullsend.bot_role.upstream_class"); got != "5xx" {
			t.Errorf("attempt %d upstream_class = %q", i+1, got)
		}
		if got := eventAttr(e, "fullsend.bot_role.upstream_status"); got != "502" {
			t.Errorf("attempt %d upstream_status = %q", i+1, got)
		}
		if got := eventAttr(e, "fullsend.bot_role.retry_decision"); got != retryRetrying {
			t.Errorf("attempt %d decision = %q", i+1, got)
		}
		if got := eventAttr(e, "fullsend.bot_role.wait_ms"); got != strconv.FormatInt(wantWait.Milliseconds(), 10) {
			t.Errorf("attempt %d wait_ms = %q, want %v", i+1, got, wantWait)
		}
	}
	if got := eventAttr(apps.Events[2], "fullsend.bot_role.retry_decision"); got != retrySucceeded {
		t.Errorf("last attempt decision = %q", got)
	}
	assertNoIdentifiers(t, c, "")
}

func TestBotRoleTrace_ExhaustedAppsLookupAttributesTheFailure(t *testing.T) {
	env := newBotRoleEnv(t, botRoleGitHub{}, "review")
	newSequencedGitHub(t, env, []seqResponse{seqBadGateway}, []seqResponse{seqInstallOK})
	c, spans := traceBotRole(t, env, botRoleBody)

	root := mustSpan(t, spans, "POST /v1/bot-role")
	wantAttr(t, root, "http.response.status_code", "502")
	wantAttr(t, root, "fullsend.mint.failure_stage", stageAppsLookup)
	apps := mustSpan(t, spans, "mint.github_apps_lookup")
	wantAttr(t, apps, "fullsend.bot_role.attempts", strconv.Itoa(botRoleMaxAttempts))
	wantAttr(t, apps, "fullsend.bot_role.upstream_class", "5xx")
	wantAttr(t, apps, "fullsend.bot_role.upstream_status", "502")
	wantAttr(t, apps, "fullsend.bot_role.rate_limit", "none")
	wantAttr(t, apps, "fullsend.bot_role.retry_decision", retryAttemptsExhausted)
	wantAttr(t, apps, "fullsend.bot_role.result", "error")
	if apps.Status.Code != spanStatusError || apps.Status.Message != "GitHub request failed: status 502" {
		t.Errorf("status = %+v, want the sanitized upstream status", apps.Status)
	}
	assertNoIdentifiers(t, c, "")
}

func TestBotRoleTrace_RateLimitBeyondBoundRecordsSkipReason(t *testing.T) {
	env := newBotRoleEnv(t, botRoleGitHub{}, "review")
	newSequencedGitHub(t, env, []seqResponse{
		{status: http.StatusTooManyRequests, header: map[string]string{"Retry-After": "60"}},
	}, []seqResponse{seqInstallOK})
	_, spans := traceBotRole(t, env, botRoleBody)

	apps := mustSpan(t, spans, "mint.github_apps_lookup")
	wantAttr(t, apps, "fullsend.bot_role.attempts", "1")
	wantAttr(t, apps, "fullsend.bot_role.upstream_status", "429")
	wantAttr(t, apps, "fullsend.bot_role.rate_limit", "secondary")
	wantAttr(t, apps, "fullsend.bot_role.retry_decision", retryWaitExceedsBound)
	wantAttr(t, apps, "fullsend.bot_role.wait_ms", "60000")
	wantAttr(t, apps, "fullsend.bot_role.wait_bound_ms", strconv.FormatInt(botRoleMaxWait.Milliseconds(), 10))
	if got := eventAttr(apps.Events[0], "fullsend.bot_role.retry_after_ms"); got != "60000" {
		t.Errorf("retry_after_ms = %q", got)
	}
}

func TestBotRoleTrace_PrimaryRateLimitClass(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	reset := strconv.FormatInt(now.Add(10*time.Minute).Unix(), 10)
	env := newBotRoleEnv(t, botRoleGitHub{}, "review")
	newSequencedGitHub(t, env, []seqResponse{
		{status: http.StatusForbidden, header: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": reset}, body: `{"message":"API rate limit exceeded"}`},
	}, []seqResponse{seqInstallOK})
	stubBotRoleClock(t, now)
	collector := newTestCollector(t, http.StatusOK, 0)
	env.handler.SetTracer(mustNewTracer(t, collector.server.URL, 0))
	postBotRole(t, env, env.signToken(t, nil), botRoleBody)

	apps := mustSpan(t, collector.spans(), "mint.github_apps_lookup")
	wantAttr(t, apps, "fullsend.bot_role.rate_limit", "primary")
	wantAttr(t, apps, "fullsend.bot_role.retry_decision", retryWaitExceedsBound)
	if apps.Status.Message != "GitHub request failed: status 403 (rate limit: primary)" {
		t.Errorf("status message = %q", apps.Status.Message)
	}
}

func TestBotRoleTrace_TransportFailureIsClassified(t *testing.T) {
	env := newBotRoleEnv(t, botRoleGitHub{}, "review")
	env.handler.githubBaseURL = "http://127.0.0.1:1" // nothing listens
	c, spans := traceBotRole(t, env, botRoleBody)

	apps := mustSpan(t, spans, "mint.github_apps_lookup")
	wantAttr(t, apps, "fullsend.bot_role.upstream_class", "transport")
	if _, ok := attr(apps, "fullsend.bot_role.upstream_status"); ok {
		t.Errorf("transport failure must not carry an upstream status")
	}
	if apps.Status.Message != "GitHub request failed: transport failure" {
		t.Errorf("status message = %q", apps.Status.Message)
	}
	assertNoIdentifiers(t, c, "")
}

func TestBotRoleTrace_InstallationFailureStage(t *testing.T) {
	env := newBotRoleEnv(t, botRoleGitHub{}, "review")
	newSequencedGitHub(t, env, []seqResponse{seqAppOK}, []seqResponse{{status: http.StatusUnprocessableEntity}})
	c, spans := traceBotRole(t, env, botRoleBody)

	root := mustSpan(t, spans, "POST /v1/bot-role")
	wantAttr(t, root, "fullsend.mint.failure_stage", stageInstallationLookup)
	inst := mustSpan(t, spans, "mint.installation_lookup")
	wantAttr(t, inst, "fullsend.bot_role.upstream_status", "422")
	wantAttr(t, inst, "fullsend.bot_role.retry_decision", retryNotRetryable)
	wantAttr(t, inst, "fullsend.bot_role.attempts", "1")
	assertNoIdentifiers(t, c, "")
}

func TestBotRoleTrace_InstallationNotFoundIsNoMatchNotFailure(t *testing.T) {
	env := newBotRoleEnv(t, botRoleGitHub{}, "review")
	newSequencedGitHub(t, env, []seqResponse{seqAppOK}, []seqResponse{{status: http.StatusNotFound}})
	_, spans := traceBotRole(t, env, botRoleBody)

	root := mustSpan(t, spans, "POST /v1/bot-role")
	wantAttr(t, root, "http.response.status_code", "200")
	inst := mustSpan(t, spans, "mint.installation_lookup")
	wantAttr(t, inst, "fullsend.bot_role.result", "no_match")
	wantAttr(t, inst, "fullsend.bot_role.upstream_class", "not_found")
	if inst.Status.Code == spanStatusError {
		t.Errorf("a completed no-match lookup must not be an error span")
	}
}

func TestBotRoleTrace_MalformedAppResponse(t *testing.T) {
	env := newBotRoleEnv(t, botRoleGitHub{}, "review")
	newSequencedGitHub(t, env, []seqResponse{{status: http.StatusOK, body: `{"no":"id"}`}}, []seqResponse{seqInstallOK})
	c, spans := traceBotRole(t, env, botRoleBody)

	apps := mustSpan(t, spans, "mint.github_apps_lookup")
	wantAttr(t, apps, "fullsend.bot_role.upstream_class", "malformed_response")
	wantAttr(t, apps, "fullsend.bot_role.retry_decision", retryNotRetryable)
	if apps.Status.Message != "GitHub response unusable" {
		t.Errorf("status message = %q", apps.Status.Message)
	}
	assertNoIdentifiers(t, c, "")
}

func TestBotRoleTrace_UnavailableCredentialDegradesWithoutFailingRequest(t *testing.T) {
	var auth []string
	env := newBotRoleEnv(t, botRoleGitHub{
		apps:     map[string]botRoleFakeResponse{"dependabot": {http.StatusOK, `{"id":29110}`}},
		appsAuth: &auth,
	}) // no PEMs: no credential to authenticate /apps with
	c, spans := traceBotRole(t, env, `{"actor_login":"dependabot[bot]","repo":"test-org/test-repo"}`)

	creds := mustSpan(t, spans, "mint.bot_role_credentials")
	wantAttr(t, creds, "fullsend.bot_role.result", "unavailable")
	wantAttr(t, creds, "fullsend.bot_role.skip_reason", "credential_unavailable_using_anonymous")
	wantAttr(t, creds, "fullsend.bot_role.failure_step", stagePEMAccess)
	root := mustSpan(t, spans, "POST /v1/bot-role")
	wantAttr(t, root, "http.response.status_code", "200")
	wantNoFailureStage(t, root)
	assertNoIdentifiers(t, c, "")
}

func TestBotRoleTrace_RejectedCredentialRetriesAnonymouslyWithoutFailure(t *testing.T) {
	env := newBotRoleEnv(t, botRoleGitHub{
		apps:           map[string]botRoleFakeResponse{"fullsend-review": {http.StatusOK, `{"id":300}`}},
		installs:       map[string]int{"test-org/test-repo": http.StatusOK},
		rejectAppsAuth: true,
	}, "review", "coder")
	_, spans := traceBotRole(t, env, botRoleBody)

	var results, auths []string
	for _, s := range spans {
		if s.Name != "mint.github_apps_lookup" {
			continue
		}
		r, _ := attr(&s, "fullsend.bot_role.result")
		a, _ := attr(&s, "fullsend.bot_role.apps_auth")
		results, auths = append(results, r), append(auths, a)
		if r == "credential_rejected" {
			wantAttr(t, &s, "fullsend.bot_role.upstream_status", "401")
			wantAttr(t, &s, "fullsend.bot_role.skip_reason", "credential_rejected_retrying_anonymously")
		}
	}
	if len(results) != 2 || results[0] != "credential_rejected" || results[1] != "ok" ||
		auths[0] != "installation_token" || auths[1] != "anonymous" {
		t.Fatalf("apps lookups: results=%v auths=%v", results, auths)
	}
	wantNoFailureStage(t, mustSpan(t, spans, "POST /v1/bot-role"))
}

func TestBotRoleTrace_RequiredCredentialFailureStage(t *testing.T) {
	// coder sorts first and authenticates /apps; the matched role, review,
	// has no PEM, so its own credential is required and missing.
	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatal(err)
	}
	env := newTestOIDCEnv(t, &fakePEMAccessor{pems: map[string][]byte{PemSecretRole("coder"): pemData}})
	newSequencedGitHub(t, env, []seqResponse{seqAppOK}, []seqResponse{seqInstallOK})
	stubBotRoleClock(t, time.Now())
	c, spans := traceBotRole(t, env, botRoleBody)

	root := mustSpan(t, spans, "POST /v1/bot-role")
	wantAttr(t, root, "http.response.status_code", "502")
	wantAttr(t, root, "fullsend.mint.failure_stage", stagePEMAccess)
	creds := spans[0]
	for _, s := range spans {
		if r, _ := attr(&s, "fullsend.bot_role.credential_required"); r == "true" {
			creds = s
		}
	}
	wantAttr(t, &creds, "fullsend.bot_role.result", "error")
	if creds.Status.Message != "PEM access failed" {
		t.Errorf("status message = %q", creds.Status.Message)
	}
	assertNoIdentifiers(t, c, "")
}

func TestBotRoleTrace_InvalidRegistrationIsConfigStage(t *testing.T) {
	t.Setenv("ROLE_APP_IDS", `{"review":"not-an-id"}`)
	env := newBotRoleEnv(t, botRoleGitHub{}, "review")
	_, spans := traceBotRole(t, env, botRoleBody)

	root := mustSpan(t, spans, "POST /v1/bot-role")
	wantAttr(t, root, "fullsend.mint.failure_stage", stageBotRoleConfig)
	cfg := mustSpan(t, spans, "mint.bot_role_config")
	if cfg.Status.Message != "invalid role registration" {
		t.Errorf("status message = %q", cfg.Status.Message)
	}
	if spanByName(spans, "mint.github_apps_lookup") != nil {
		t.Errorf("GitHub was called despite an invalid registration")
	}
}

func TestBotRoleTrace_RetryDecisions(t *testing.T) {
	// A call that never fails with a status ends at once; a cancelled
	// context stops a transient failure from being retried.
	lt := &lookupTrace{}
	calls := 0
	err := retryGitHubLookup(t.Context(), "x", lt, func() error { calls++; return nil })
	if err != nil || calls != 1 || lt.decision != retrySucceeded || lt.attempts != 1 {
		t.Fatalf("success: err=%v calls=%d trace=%+v", err, calls, lt)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	lt = &lookupTrace{}
	calls = 0
	_ = retryGitHubLookup(ctx, "x", lt, func() error {
		calls++
		return &githubStatusError{Status: http.StatusBadGateway}
	})
	if calls != 1 || lt.decision != retryContextDone {
		t.Fatalf("cancelled: calls=%d decision=%q", calls, lt.decision)
	}
}

func TestGitHubStatusError_TelemetryMessage(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  *githubStatusError
		want string
		rl   string
	}{
		{"transport", &githubStatusError{msg: "getting app \"secret-slug\": boom"}, "GitHub request failed: transport failure", "none"},
		{"status", &githubStatusError{Status: 500, msg: "getting installation for o/r returned status 500"}, "GitHub request failed: status 500", "none"},
		{"primary", &githubStatusError{Status: 403, RateLimited: true, Exhausted: true}, "GitHub request failed: status 403 (rate limit: primary)", "primary"},
		{"secondary", &githubStatusError{Status: 429, RateLimited: true, RetryAfter: time.Second}, "GitHub request failed: status 429 (rate limit: secondary)", "secondary"},
		{"other", &githubStatusError{Status: 429, RateLimited: true}, "GitHub request failed: status 429 (rate limit: other)", "other"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.telemetryMessage(); got != tt.want {
				t.Errorf("telemetryMessage = %q, want %q", got, tt.want)
			}
			if got := tt.err.rateLimitClass(); got != tt.rl {
				t.Errorf("rateLimitClass = %q, want %q", got, tt.rl)
			}
		})
	}
}
