package mintcore

import (
	"errors"
	"strconv"
	"time"
)

// Sanitized tracing of the bot-role lookup stages (configuration check,
// App credentials, GitHub /apps lookup, installation lookup). Spans carry
// fixed-vocabulary values only: stage names, upstream status and class,
// rate-limit class, attempt counts and the retry decision. They never carry
// credentials, raw upstream responses, error text built from them, or the
// actor login, App slug or repository, which the call descriptions in the
// returned errors include.

// Retry decisions recorded as fullsend.bot_role.retry_decision.
const (
	retrySucceeded           = "succeeded"
	retryNotRetryable        = "not_retryable"
	retryContextDone         = "context_done"
	retryAttemptsExhausted   = "attempts_exhausted"
	retryWaitExceedsBound    = "wait_exceeds_bound"
	retryWaitExceedsDeadline = "wait_exceeds_deadline"
	retryRetrying            = "retrying"
	retrySleepInterrupted    = "sleep_interrupted"
)

// sanitizedErr is a span error whose text is fixed and safe to export.
type sanitizedErr string

func (e sanitizedErr) Error() string            { return string(e) }
func (e sanitizedErr) telemetryMessage() string { return string(e) }

// malformedAppError is a GET /apps/{slug} answer that could not be used.
type malformedAppError struct{ msg string }

func (e *malformedAppError) Error() string { return e.msg }

// credentialError is a failure to build the App JWT of a role. Its text is
// the plain error text; step is the failure stage and text the span-safe
// description.
type credentialError struct {
	step string
	text string
	err  error
}

func (e *credentialError) Error() string { return e.err.Error() }
func (e *credentialError) Unwrap() error { return e.err }

// lookupTrace accumulates the attempts of one retried GitHub lookup.
type lookupTrace struct {
	span      *span
	attempts  int
	decision  string
	class     string
	status    int
	rateLimit string
	waitMS    int64
	hasWait   bool
}

// upstreamClass classifies the outcome of one lookup attempt: its class
// ("ok", "not_found", "transport", "4xx", "5xx", "malformed_response" or
// "local") and the HTTP status when GitHub answered with one.
func upstreamClass(err error) (string, int) {
	var statusErr *githubStatusError
	var malformed *malformedAppError
	switch {
	case err == nil:
		return "ok", 0
	case errors.Is(err, errBotRoleNoMatch), errors.Is(err, ErrInstallationNotFound):
		return "not_found", 404
	case errors.As(err, &statusErr):
		if statusErr.Status == 0 {
			return "transport", 0
		}
		return strconv.Itoa(statusErr.Status/100) + "xx", statusErr.Status
	case errors.As(err, &malformed):
		return "malformed_response", 0
	default:
		return "local", 0
	}
}

// record notes one finished attempt and the decision taken after it.
func (lt *lookupTrace) record(attempt int, err error, decision string, wait time.Duration, hasWait bool) {
	class, status := upstreamClass(err)
	rateLimit := "none"
	var retryAfter time.Duration
	var statusErr *githubStatusError
	if errors.As(err, &statusErr) {
		rateLimit = statusErr.rateLimitClass()
		retryAfter = statusErr.RetryAfter
	}
	lt.attempts, lt.decision, lt.class, lt.status, lt.rateLimit = attempt, decision, class, status, rateLimit
	lt.waitMS, lt.hasWait = wait.Milliseconds(), hasWait

	var attrs []otlpKeyValue
	attrs = appendIntAttr(attrs, "fullsend.bot_role.attempt", int64(attempt))
	attrs = appendStrAttr(attrs, "fullsend.bot_role.upstream_class", class)
	if status > 0 {
		attrs = appendIntAttr(attrs, "fullsend.bot_role.upstream_status", int64(status))
	}
	attrs = appendStrAttr(attrs, "fullsend.bot_role.rate_limit", rateLimit)
	if retryAfter > 0 {
		attrs = appendIntAttr(attrs, "fullsend.bot_role.retry_after_ms", retryAfter.Milliseconds())
	}
	attrs = appendStrAttr(attrs, "fullsend.bot_role.retry_decision", decision)
	if hasWait {
		attrs = appendIntAttr(attrs, "fullsend.bot_role.wait_ms", wait.Milliseconds())
		attrs = appendIntAttr(attrs, "fullsend.bot_role.wait_bound_ms", botRoleMaxWait.Milliseconds())
	}
	lt.span.addEvent("attempt", attrs)
}

// finish records the summary of the lookup on its span and ends it. A
// failure is attributed to the span's stage, except a rejected App
// credential, which the caller recovers from by retrying anonymously.
func (lt *lookupTrace) finish(err error) {
	sp := lt.span
	sp.setInt("fullsend.bot_role.attempts", int64(lt.attempts))
	if lt.attempts > 0 {
		sp.setStr("fullsend.bot_role.upstream_class", lt.class)
		if lt.status > 0 {
			sp.setInt("fullsend.bot_role.upstream_status", int64(lt.status))
		}
		sp.setStr("fullsend.bot_role.rate_limit", lt.rateLimit)
		sp.setStr("fullsend.bot_role.retry_decision", lt.decision)
		if lt.hasWait {
			sp.setInt("fullsend.bot_role.wait_ms", lt.waitMS)
			sp.setInt("fullsend.bot_role.wait_bound_ms", botRoleMaxWait.Milliseconds())
		}
	}
	switch {
	case err == nil:
		sp.setStr("fullsend.bot_role.result", "ok")
	case errors.Is(err, errBotRoleNoMatch), errors.Is(err, ErrInstallationNotFound):
		sp.setStr("fullsend.bot_role.result", "no_match")
	case errors.Is(err, errAppsAuthRejected):
		sp.setStr("fullsend.bot_role.result", "credential_rejected")
		sp.setStr("fullsend.bot_role.skip_reason", "credential_rejected_retrying_anonymously")
	default:
		sp.setStr("fullsend.bot_role.result", "error")
		sp.recordError(sanitizedErr(botRoleFailureText(err)))
	}
	sp.end()
}

// botRoleFailureText describes a failed lookup without any text taken from
// the error itself.
func botRoleFailureText(err error) string {
	var statusErr *githubStatusError
	var malformed *malformedAppError
	switch {
	case errors.As(err, &statusErr):
		return statusErr.telemetryMessage()
	case errors.As(err, &malformed):
		return "GitHub response unusable"
	default:
		return "lookup failed"
	}
}

// credentialFailure returns the failure stage and span-safe text of a
// failed App JWT build.
func credentialFailure(err error) (string, string) {
	var ce *credentialError
	if errors.As(err, &ce) {
		return ce.step, ce.text
	}
	return stageGitHubAppAuth, "App credential unavailable"
}
