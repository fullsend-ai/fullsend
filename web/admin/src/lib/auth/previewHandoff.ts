/**
 * Validates return_to against an explicit allowlist of preview origins
 * (scheme + host, no path). Caller supplies allowlist from production config.
 */
export function assertAllowedReturnTo(
  returnTo: string,
  allowedOrigins: string[],
): URL {
  let url: URL;
  try {
    url = new URL(returnTo);
  } catch {
    throw new Error("return_to is not a valid URL");
  }
  if (url.protocol !== "https:") {
    throw new Error("return_to must use https");
  }
  const origin = `${url.protocol}//${url.host}`;
  if (!allowedOrigins.includes(origin)) {
    throw new Error("return_to origin is not allowlisted");
  }
  return url;
}
