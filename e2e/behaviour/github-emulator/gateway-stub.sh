#!/usr/bin/env bash
# A test inference gateway inside the emulator VM, for the gateway behaviour
# scenarios: fullsend's own e2e gateway config (hack/setup-e2e-inference-gateway.sh
# --print-config, echo models only, no Vertex), trusting the emulator's OIDC
# issuer, served by the pinned agentgateway image on https://$GW_HOST.
#
#   gateway-stub.sh config DIR   render DIR/config.yaml, DIR/jwks.json,
#                                DIR/tls/ and DIR/key/ (no container, no sudo)
#   gateway-stub.sh start DIR    config, then run the gateway and route
#                                $GW_IP:443 to it (Linux VM, sudo)
#   gateway-stub.sh stop DIR     remove the container and the route
#
# Settings (env):
#   GW_HOST     gateway host name (default gw.ghemu.localhost); resolves to
#               GW_IP through ci-stack.sh's CI_STACK_DNS_EXTRA
#   GW_IP       its address (default 10.254.0.1)
#   STACK_TLS   ci-stack.sh's tls directory (ca.pem, ca-key.pem)   [required]
#   ISSUER      the emulator's OIDC issuer, exactly as in its tokens [required]
#   JWKS_URL    the emulator's JWKS URL                             [required]
#   POOL_ORG_ID halfsend's org id on the emulator                   [required]
#   CONTAINER   container name (default bt-gateway-stub)
#
# start writes DIR/gateway.env with E2E_INFERENCE_GATEWAY_URL and a per-run
# E2E_INFERENCE_GATEWAY_TEST_KEY for the echo model only.
#
# Why the port and file workarounds:
# - fullsend refuses a gateway host with a port, so the gateway must answer
#   on 443, and the emulator already listens on 0.0.0.0:443 (Linux refuses a
#   second, address-specific listener on that port). The gateway listens on
#   8443 and a nat OUTPUT rule sends GW_IP:443 there. The OpenShell sandbox
#   supervisor runs in the host network namespace, so its upstream
#   connections take the rule.
# - The rendered config serves plain HTTP on 8080 (Cloud Run terminates TLS)
#   and routes /e2e-prefix back to 127.0.0.1:8080. It stays as rendered; one
#   more bind adds TLS on 8443 with a certificate from the stack CA and
#   forwards to 8080.
# - The container runs under root's podman: the harness jobs run as the same
#   user as this script, and their setup runs `podman system migrate`, which
#   stops every container in that user's podman store.
# - The gateway reads the emulator's JWKS from a file fetched once, so it
#   needs no trust in the stack CA. The emulator's signing key lives as long
#   as the run.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
CMD="${1:?usage: gateway-stub.sh config|start|stop DIR}"
DIR="${2:?usage: gateway-stub.sh config|start|stop DIR}"
GW_HOST="${GW_HOST:-gw.ghemu.localhost}"
GW_IP="${GW_IP:-10.254.0.1}"
CONTAINER="${CONTAINER:-bt-gateway-stub}"
TLS_PORT=8443
# agentgateway v1.7.0, the digest hack/setup-e2e-inference-gateway.sh pins.
AGW_DIGEST="$(sed -nE 's/^AGW_DIGEST="(sha256:[0-9a-f]{64})"$/\1/p' "$ROOT/hack/setup-e2e-inference-gateway.sh")"
IMAGE="ghcr.io/agentgateway/agentgateway@${AGW_DIGEST:?no AGW_DIGEST in hack/setup-e2e-inference-gateway.sh}"

render() {
  : "${STACK_TLS:?}" "${ISSUER:?}" "${JWKS_URL:?}" "${POOL_ORG_ID:?}"
  mkdir -p "$DIR/tls" "$DIR/key"
  local echo_key hash
  echo_key="e2e-echo-$(openssl rand -hex 16)"
  hash="sha256:$(printf '%s' "$echo_key" | openssl dgst -sha256 -r | cut -d' ' -f1)"
  printf '%s' "$echo_key" >"$DIR/echo.key"
  chmod 600 "$DIR/echo.key"

  ECHO_KEY_HASH="$hash" bash "$ROOT/hack/setup-e2e-inference-gateway.sh" --print-config --without-vertex \
    --issuer "$ISSUER" --jwks-url "$JWKS_URL" --pool-org-id "halfsend=$POOL_ORG_ID" \
    --upstream-key-dir /etc/agw-upstream >"$DIR/rendered.yaml"

  # The stub upstream's non-secret key, as the rendered echo listener expects it.
  sed -nE 's/.*request\.headers\["authorization"\] == "Bearer ([^"]+)".*/\1/p' "$DIR/rendered.yaml" | head -1 | tr -d '\n' >"$DIR/key/key"
  [ -s "$DIR/key/key" ] || { echo "gateway-stub: no stub upstream key in the rendered config" >&2; return 1; }

  curl -fsS "$JWKS_URL" -o "$DIR/jwks.json"

  openssl req -newkey rsa:2048 -nodes -subj "/CN=$GW_HOST" \
    -keyout "$DIR/tls/key.pem" -out "$DIR/tls/server.csr" 2>/dev/null
  printf 'subjectAltName=DNS:%s\nbasicConstraints=CA:FALSE\nextendedKeyUsage=serverAuth\n' "$GW_HOST" >"$DIR/tls/ext.cnf"
  openssl x509 -req -in "$DIR/tls/server.csr" -CA "$STACK_TLS/ca.pem" -CAkey "$STACK_TLS/ca-key.pem" \
    -CAcreateserial -CAserial "$DIR/tls/ca.srl" -days 2 -extfile "$DIR/tls/ext.cnf" -out "$DIR/tls/cert.pem" 2>/dev/null
  chmod 644 "$DIR/tls/key.pem" # read by the container user; the VM is discarded

  # JWKS from the file; then one more bind, TLS on TLS_PORT, forwarding to
  # the rendered plain listener.
  local jwks_line="jwks: { url: $JWKS_URL }"
  grep -qF "$jwks_line" "$DIR/rendered.yaml" || { echo "gateway-stub: no '$jwks_line' in the rendered config" >&2; return 1; }
  awk -v from="$jwks_line" -v to="jwks: { file: /etc/agw-config/jwks.json }" -v port="$TLS_PORT" '
    index($0, from) { sub(/jwks: \{ url: [^}]*\}/, to) }
    { print }
    $0 == "binds:" {
      print "- port: " port
      print "  listeners:"
      print "  - name: tls"
      print "    protocol: HTTPS"
      print "    tls: { cert: /etc/agw-tls/cert.pem, key: /etc/agw-tls/key.pem }"
      print "    routes:"
      print "    - name: to-gateway"
      print "      backends:"
      print "      - host: 127.0.0.1:8080"
    }' "$DIR/rendered.yaml" >"$DIR/config.yaml"
  grep -q 'jwks: { file: /etc/agw-config/jwks.json }' "$DIR/config.yaml"
  grep -q "^- port: $TLS_PORT\$" "$DIR/config.yaml"
}

nat_rule() { # nat_rule -A|-C|-D
  sudo iptables -t nat "$1" OUTPUT -d "$GW_IP/32" -p tcp --dport 443 -j DNAT --to-destination "$GW_IP:$TLS_PORT"
}

case "$CMD" in
  config) render ;;
  start)
    render
    sudo podman rm -f "$CONTAINER" >/dev/null 2>&1 || true
    sudo podman run -d --name "$CONTAINER" --network host --log-driver k8s-file \
      -v "$DIR/config.yaml:/etc/agw-config/config.yaml:ro" -v "$DIR/jwks.json:/etc/agw-config/jwks.json:ro" \
      -v "$DIR/key:/etc/agw-upstream:ro" -v "$DIR/tls:/etc/agw-tls:ro" \
      "$IMAGE" -f /etc/agw-config/config.yaml >/dev/null
    nat_rule -C 2>/dev/null || nat_rule -A
    # Ready when the TLS bind answers through the route with an HTTP status
    # (an unauthenticated call gets 401 or 403, never 000). curl resolves
    # *.localhost to loopback itself, without DNS, so pin GW_IP: the address
    # DNS gives the sandbox supervisor, and the one the nat rule matches.
    code=000
    for _ in $(seq 1 30); do
      code=$(curl -sS -o /dev/null -w '%{http_code}' --cacert "$STACK_TLS/ca.pem" -m 3 \
        --resolve "$GW_HOST:443:$GW_IP" "https://$GW_HOST/v1/chat/completions" \
        -H 'Content-Type: application/json' -d '{"model":"echo"}' 2>"$DIR/ready.err") || true
      [ "$code" != 000 ] && break
      sleep 1
    done
    if [ "$code" = 000 ]; then
      echo "::error::gateway stub not reachable on https://$GW_HOST ($GW_IP): $(tail -1 "$DIR/ready.err")"
      echo "DNS: $(getent hosts "$GW_HOST" || echo none)"
      sudo iptables -t nat -S OUTPUT
      sudo podman logs "$CONTAINER" 2>&1 | tail -20
      exit 1
    fi
    echo "gateway stub on https://$GW_HOST ($GW_IP:443 -> :$TLS_PORT), unauthenticated call: HTTP $code"
    {
      echo "E2E_INFERENCE_GATEWAY_URL=https://$GW_HOST"
      echo "E2E_INFERENCE_GATEWAY_TEST_KEY=$(cat "$DIR/echo.key")"
    } >"$DIR/gateway.env"
    ;;
  stop)
    # shellcheck disable=SC2024 # the log belongs to this user, in DIR
    sudo podman logs "$CONTAINER" >"$DIR/gateway.log" 2>&1 || true
    sudo podman rm -f "$CONTAINER" >/dev/null 2>&1 || true
    if nat_rule -C 2>/dev/null; then nat_rule -D; fi
    ;;
  *) echo "usage: gateway-stub.sh config|start|stop DIR" >&2; exit 2 ;;
esac
