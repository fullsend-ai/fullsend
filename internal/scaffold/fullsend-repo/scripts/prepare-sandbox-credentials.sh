#!/usr/bin/env bash
set -euo pipefail

# Prepare GCP credentials for sandbox environments.
#
# When using Workload Identity Federation (WIF), the google-github-actions/auth
# action creates an external_account credential config that references GitHub's
# OIDC endpoint via credential_source.url. The sandbox cannot reach that
# endpoint, so this script pre-fetches the OIDC token and rewrites the config
# to use a file-based credential source instead.
#
# Note: the OIDC token expires after ~5 min. The fullsend CLI refreshes it
# automatically using FULLSEND_GCP_OIDC_URL and FULLSEND_GCP_OIDC_AUTH_FILE
# exported below.
#
# In SA-key mode (type != external_account), this script is a no-op.

CRED_CONFIG="$GOOGLE_APPLICATION_CREDENTIALS"
CRED_TYPE=$(jq -r '.type // empty' "$CRED_CONFIG" 2>/dev/null || true)
if [[ "$CRED_TYPE" == "external_account" ]]; then
  OIDC_URL=$(jq -r '.credential_source.url // empty' "$CRED_CONFIG")
  OIDC_AUTH=$(jq -r '.credential_source.headers.Authorization // empty' "$CRED_CONFIG")
  if [[ -z "$OIDC_URL" || -z "$OIDC_AUTH" ]]; then
    echo "::error::WIF credential config missing credential_source.url or auth header"
    exit 1
  fi

  echo "::add-mask::$OIDC_AUTH"
  OIDC_DEST="$RUNNER_TEMP/gcp-oidc-token"
  curl -sSf -H "Authorization: $OIDC_AUTH" "$OIDC_URL" > "$OIDC_DEST"
  chmod 600 "$OIDC_DEST"

  SANDBOX_CREDS="$RUNNER_TEMP/sandbox-gcp-credentials.json"
  jq '{
    type, audience, subject_token_type, token_url,
    credential_source: { file: "/sandbox/workspace/.gcp-oidc-token", format: .credential_source.format }
  } + (if .service_account_impersonation_url then
    {service_account_impersonation_url}
  else {} end)' "$CRED_CONFIG" > "$SANDBOX_CREDS"

  OIDC_AUTH_FILE="$RUNNER_TEMP/gcp-oidc-auth"
  printf '%s' "$OIDC_AUTH" > "$OIDC_AUTH_FILE"
  chmod 600 "$OIDC_AUTH_FILE"

  {
    echo "GOOGLE_APPLICATION_CREDENTIALS=$SANDBOX_CREDS"
    echo "GCP_OIDC_TOKEN_FILE=$OIDC_DEST"
    echo "FULLSEND_GCP_OIDC_URL=$OIDC_URL"
    echo "FULLSEND_GCP_OIDC_AUTH_FILE=$OIDC_AUTH_FILE"
  } >> "$GITHUB_ENV"
fi
