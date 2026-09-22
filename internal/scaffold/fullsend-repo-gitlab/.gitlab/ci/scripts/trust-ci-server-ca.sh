#!/usr/bin/env bash
# trust-ci-server-ca.sh — Bootstrap private-CA trust from GitLab Runner.
#
# GitLab Runner sets CI_SERVER_TLS_CA_FILE to a job-local PEM when
# tls-ca-file is configured in config.toml. Source this file before any
# GitLab network operation so the Go client (SSL_CERT_FILE), curl
# (CURL_CA_BUNDLE), git (GIT_SSL_CAINFO), and Python (REQUESTS_CA_BUNDLE)
# all trust that CA without per-tool configuration.
#
# Public CA trust is preserved by concatenating the extra CA onto a
# system bundle. TLS verification is never disabled.
#
# CI_SERVER_TLS_CA_FILE is a job-container path. It is not available on
# a separately provisioned sandbox host — see the operations guide.
#
# Source this file (do not execute it) so the exports persist.

fullsend_trust_ci_server_ca() {
  if [ "${FULLSEND_CI_SERVER_CA_TRUSTED:-}" = "1" ]; then
    return 0
  fi

  ca_file="${CI_SERVER_TLS_CA_FILE:-}"
  if [ -z "${ca_file}" ]; then
    return 0
  fi

  if [ ! -e "${ca_file}" ]; then
    echo "ERROR: CI_SERVER_TLS_CA_FILE is set to ${ca_file} but the file does not exist" >&2
    echo "GitLab Runner writes this path when tls-ca-file is configured in config.toml." >&2
    echo "Installations without a custom CA should leave CI_SERVER_TLS_CA_FILE unset." >&2
    return 1
  fi
  if [ ! -f "${ca_file}" ]; then
    echo "ERROR: CI_SERVER_TLS_CA_FILE (${ca_file}) is not a regular file" >&2
    return 1
  fi
  if [ ! -r "${ca_file}" ]; then
    echo "ERROR: CI_SERVER_TLS_CA_FILE (${ca_file}) is not readable" >&2
    return 1
  fi

  if ! grep -q -- "-----BEGIN CERTIFICATE-----" "${ca_file}"; then
    echo "ERROR: CI_SERVER_TLS_CA_FILE (${ca_file}) is not a PEM certificate bundle" >&2
    echo "Expected at least one BEGIN CERTIFICATE block. TLS verification is not disabled." >&2
    return 1
  fi

  system_ca=""
  for candidate in \
    /etc/pki/tls/certs/ca-bundle.crt \
    /etc/ssl/certs/ca-certificates.crt \
    /etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem \
    /etc/ssl/ca-bundle.pem \
    /etc/ssl/cert.pem
  do
    if [ -r "${candidate}" ] && [ -f "${candidate}" ]; then
      system_ca="${candidate}"
      break
    fi
  done

  combined="${RUNNER_TEMP:-/tmp}/fullsend-ca-bundle.pem"
  if [ -n "${system_ca}" ]; then
    cat "${system_ca}" "${ca_file}" > "${combined}"
  else
    echo "WARNING: no system CA bundle found; public CA trust may be incomplete" >&2
    cat "${ca_file}" > "${combined}"
  fi
  chmod 644 "${combined}"

  export SSL_CERT_FILE="${combined}"
  export GIT_SSL_CAINFO="${combined}"
  export CURL_CA_BUNDLE="${combined}"
  export REQUESTS_CA_BUNDLE="${combined}"
  export NODE_EXTRA_CA_CERTS="${combined}"
  export FULLSEND_CI_SERVER_CA_TRUSTED=1

  echo "Trusted extra CA from CI_SERVER_TLS_CA_FILE (${ca_file}); combined bundle at ${combined}" >&2
}

fullsend_trust_ci_server_ca
