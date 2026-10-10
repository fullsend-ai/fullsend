#!/bin/sh
# gateway-token.sh — codex `model_providers.fullsend-gateway.auth.command`.
#
# Written by CodexRuntime.Bootstrap when an inference.gateway block applies
# (ADR 0137) and SHA-256 checked by Run before every iteration. codex runs
# it with stdin on /dev/null, caches the trimmed stdout as the bearer token
# for `refresh_interval_ms`, and re-runs it on expiry and on the 401 retry
# path. A non-zero exit or empty stdout fails the model call — there is no
# fallback to the environment, which is what makes this fail closed.
#
# What it prints is never a credential: it is the OpenShell gateway
# placeholder for INFERENCE_GATEWAY_API_KEY that the runner seeded into the
# token file, and the runner re-seeds that file after every token refresh
# (the oidc mode) so a running iteration follows the new generation.
#
# The placeholder namespace is assembled from two parts on purpose: OpenShell
# 0.0.110+ resets any model request whose body carries the contiguous prefix,
# so a file that spelled it out could not be read by an agent inside a
# sandbox (fullsend#6716).

set -eu

# Hardcoded rather than templated so the embedded asset's bytes — and
# therefore the SHA-256 the run command pins — are fixed at compile time.
# TestCodexAssetPathsMatchConstants keeps it equal to
# CodexRuntime.GatewayCredentialSeed().File.
TOKEN_FILE="/sandbox/codex-config/gateway-token"
PREFIX="openshell:resolve:env"

if [ ! -r "$TOKEN_FILE" ]; then
	echo "fullsend: codex inference gateway token file $TOKEN_FILE is missing or unreadable; the runner seeds it at iteration start and after every token refresh" >&2
	exit 1
fi

token=$(cat "$TOKEN_FILE")

case "$token" in
"$PREFIX":*INFERENCE_GATEWAY_API_KEY) ;;
*)
	# Deliberately does not echo the value: if a real token reached the
	# sandbox the provider path was bypassed, and printing it would put it
	# in the run log as well as on the wire.
	echo "fullsend: codex inference gateway token file $TOKEN_FILE does not hold a gateway placeholder for INFERENCE_GATEWAY_API_KEY; refusing to hand codex a credential" >&2
	exit 1
	;;
esac

case "$token" in
*[!A-Za-z0-9_:]*)
	echo "fullsend: codex inference gateway token file $TOKEN_FILE has unexpected characters; refusing to hand codex a credential" >&2
	exit 1
	;;
esac

printf '%s' "$token"
