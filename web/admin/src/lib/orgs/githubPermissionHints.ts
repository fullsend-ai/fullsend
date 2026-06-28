import { RequestError } from "@octokit/request-error";

/**
 * Browser calls to `api.github.com` only see response headers listed in GitHub’s
 * `Access-Control-Expose-Headers` (CORS). That list includes rate-limit headers and
 * `X-OAuth-Scopes` / `X-Accepted-OAuth-Scopes`, but **not** `X-Accepted-GitHub-Permissions`.
 * So user-to-server GitHub App tokens never surface fine-grained App permission hints to
 * this SPA via headers — only the JSON body and the exposed OAuth scope headers are reliable.
 *
 * @see https://docs.github.com/en/rest/using-the-rest-api/using-cors-and-jsonp-to-make-cross-origin-requests
 */

/** Lowercase header names as returned by Octokit. */
function headerGet(headers: Record<string, string> | undefined, name: string): string | undefined {
  if (!headers) return undefined;
  const direct = headers[name];
  if (direct !== undefined) return direct;
  const lower = name.toLowerCase();
  for (const [k, v] of Object.entries(headers)) {
    if (k.toLowerCase() === lower) return v;
  }
  return undefined;
}

function githubApiMessage(data: unknown): string | undefined {
  if (!data || typeof data !== "object") return undefined;
  const m = (data as { message?: unknown }).message;
  return typeof m === "string" && m.trim() ? m.trim() : undefined;
}

/** GitHub JSON `message` on 403s that are access / scope / org-policy — not REST rate limits. */
function github403BodyLooksLikePermissionDenied(apiMsg: string | undefined): boolean {
  if (!apiMsg?.trim()) return false;
  const m = apiMsg.toLowerCase();
  return (
    m.includes("resource not accessible") ||
    m.includes("although you appear to have the correct authorization credentials") ||
    m.includes("oauth app access restrictions") ||
    m.includes("organization has enabled or enforced") ||
    m.includes("saml") ||
    (m.includes("sso") && m.includes("token"))
  );
}

function github403BodyLooksLikeRateLimit(apiMsg: string | undefined): boolean {
  if (!apiMsg?.trim()) return false;
  const m = apiMsg.toLowerCase();
  return (
    m.includes("api rate limit") ||
    m.includes("secondary rate limit") ||
    m.includes("abuse detection mechanism") ||
    m.includes("abuse detection") ||
    m.includes("too many requests")
  );
}

function rateLimitRemainingIsZero(headers: Record<string, string> | undefined): boolean {
  const raw = headerGet(headers, "x-ratelimit-remaining");
  if (raw === undefined) return false;
  return String(raw).trim() === "0";
}

/**
 * True when this 403 is almost certainly GitHub REST primary/secondary rate limiting
 * or abuse throttling — not missing OAuth scopes / org access.
 *
 * Uses the JSON `message` field only (not {@link RequestError#message}), because Octokit
 * appends `documentation_url` to `error.message`, which can mention “rate limits” for
 * unrelated errors and mislead callers.
 */
export function isLikelyGitHubRateLimit403(error: RequestError): boolean {
  if (error.status !== 403) return false;
  const headers = error.response?.headers as Record<string, string> | undefined;
  const apiMsg = githubApiMessage(error.response?.data);
  if (github403BodyLooksLikePermissionDenied(apiMsg)) {
    return false;
  }
  if (github403BodyLooksLikeRateLimit(apiMsg)) {
    return true;
  }
  if (rateLimitRemainingIsZero(headers)) {
    return true;
  }
  return false;
}

/**
 * User-facing line when {@link isLikelyGitHubRateLimit403} is true — uses
 * `X-RateLimit-Reset` / `X-RateLimit-Limit` / `X-RateLimit-Resource` when GitHub sends them.
 */
export function userGitHubRestRateLimitShortMessage(error: RequestError): string {
  const headers = error.response?.headers as Record<string, string> | undefined;
  const resetRaw = headerGet(headers, "x-ratelimit-reset");
  const limitRaw = headerGet(headers, "x-ratelimit-limit");
  const resourceRaw = headerGet(headers, "x-ratelimit-resource");
  const resetSec = resetRaw ? Number.parseInt(String(resetRaw).trim(), 10) : NaN;

  const limitPart =
    limitRaw?.trim() && limitRaw.trim() !== "0"
      ? ` This sign-in is allowed ${limitRaw.trim()} REST requests per hour on GitHub’s “${resourceRaw?.trim() || "core"}” budget.`
      : "";

  if (Number.isFinite(resetSec) && resetSec > 1_000_000_000) {
    const whenUtc = new Date(resetSec * 1000).toUTCString();
    return `GitHub’s hourly REST API quota for this account is exhausted.${limitPart} It resets at ${whenUtc}. Use Retry or Refresh after that.`;
  }

  return `GitHub’s hourly REST API quota for this account is exhausted.${limitPart} Wait up to an hour, then use Retry or Refresh.`;
}

/** Classic OAuth / PAT: scopes GitHub says would have worked (readable in browser per GitHub CORS). */
export function humanLineFromAcceptedOAuthScopes(raw: string | undefined): string | undefined {
  if (!raw?.trim()) return undefined;
  return `GitHub reports this API call would be allowed with these OAuth scopes: ${raw.trim()}. Your account or token may need them, or an organisation owner may need to adjust app access.`;
}

export type Forbidden403Hints = {
  /** Lines derived only from browser-visible sources (exposed OAuth scope headers + caller fallbacks). */
  missingPermissionLines: string[];
  githubApiMessage?: string;
};

/**
 * Collects permission hints available to the **browser** Octokit client.
 * Does not read `X-Accepted-GitHub-Permissions` — that header is not exposed to cross-origin JS.
 */
export function forbidden403HintsFromRequestError(error: RequestError): Forbidden403Hints {
  const headers = error.response?.headers as Record<string, string> | undefined;
  const rawOAuth = headerGet(headers, "x-accepted-oauth-scopes");
  const lines: string[] = [];
  const oauthLine = humanLineFromAcceptedOAuthScopes(rawOAuth);
  if (oauthLine) lines.push(oauthLine);
  return {
    missingPermissionLines: lines,
    githubApiMessage: githubApiMessage(error.response?.data),
  };
}
