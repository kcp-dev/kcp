#!/usr/bin/env bash

# Copyright 2025 The kcp Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

#
# End-to-end demo: validate objects across an APIExport/APIBinding using either a
# cross-workspace admission webhook (default) or a ValidatingAdmissionPolicy.
#
# Prereqs:
#   - a running kcp reachable via $KUBECONFIG (defaults to ./admin.kubeconfig in the repo root)
#   - the `kubectl ws` plugin on PATH (bin/kubectl-ws or `make install`)
#   - for webhook mode: kcp must be able to reach this host (this script uses
#     host.docker.internal, which works for the Tilt/kind setup on Docker Desktop)
#
# Usage:
#   ./run.sh            # webhook mode (external server)
#   ./run.sh policy     # ValidatingAdmissionPolicy mode (no server, no certs)
#   ./run.sh clean      # delete the provider/consumer workspaces created by the demo
set -euo pipefail

MODE="${1:-webhook}"
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$DIR/../../.." && pwd)"
export KUBECONFIG="${KUBECONFIG:-$REPO_ROOT/admin.kubeconfig}"

# Resolve the ws plugin (prefer the repo's bin).
WS="kubectl-ws"
[ -x "$REPO_ROOT/bin/kubectl-ws" ] && WS="$REPO_ROOT/bin/kubectl-ws"

WEBHOOK_PORT=9443
WEBHOOK_HOST=host.docker.internal
CERTDIR="$DIR/.certs"
SERVER_PID=""

log()  { printf '\n\033[1;34m== %s\033[0m\n' "$*"; }
ok()   { printf '\033[1;32m✓ %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31m✗ %s\033[0m\n' "$*"; }

cleanup() {
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
}
trap cleanup EXIT

if [ "$MODE" = "clean" ]; then
  log "Deleting demo workspaces"
  "$WS" :root >/dev/null
  kubectl delete workspace provider consumer --ignore-not-found
  ok "cleaned up"
  exit 0
fi

#############################################
# PROVIDER workspace: schema + export
#############################################
log "Creating PROVIDER workspace root:provider"
"$WS" :root >/dev/null
"$WS" create provider --ignore-existing --enter >/dev/null
kubectl apply -f "$DIR/apiresourceschema.yaml"
kubectl apply -f "$DIR/apiexport.yaml"
ok "APIResourceSchema + APIExport created in root:provider"

if [ "$MODE" = "webhook" ]; then
  #############################################
  # PROVIDER: serving cert + webhook server
  #############################################
  log "Generating self-signed serving cert (SAN=$WEBHOOK_HOST)"
  mkdir -p "$CERTDIR"
  openssl req -x509 -newkey rsa:2048 -nodes \
    -keyout "$CERTDIR/tls.key" -out "$CERTDIR/tls.crt" -days 365 \
    -subj "/CN=$WEBHOOK_HOST" \
    -addext "subjectAltName=DNS:$WEBHOOK_HOST,DNS:localhost,IP:127.0.0.1" >/dev/null 2>&1
  ok "cert written to $CERTDIR"

  log "Starting webhook server on :$WEBHOOK_PORT"
  ( cd "$DIR/webhook" && exec go run . \
      --tls-cert "$CERTDIR/tls.crt" --tls-key "$CERTDIR/tls.key" \
      --addr ":$WEBHOOK_PORT" ) &
  SERVER_PID=$!

  # Wait for the server to come up.
  for _ in $(seq 1 30); do
    if curl -sk "https://127.0.0.1:$WEBHOOK_PORT/healthz" >/dev/null 2>&1; then break; fi
    sleep 1
  done
  curl -sk "https://127.0.0.1:$WEBHOOK_PORT/healthz" >/dev/null && ok "webhook server is up"

  log "Registering ValidatingWebhookConfiguration in root:provider"
  CA_BUNDLE="$(base64 < "$CERTDIR/tls.crt" | tr -d '\n')"
  WEBHOOK_URL="https://$WEBHOOK_HOST:$WEBHOOK_PORT/validate"
  sed -e "s|CA_BUNDLE|$CA_BUNDLE|" -e "s|WEBHOOK_URL|$WEBHOOK_URL|" \
    "$DIR/validatingwebhookconfiguration.yaml" | kubectl apply -f -
  ok "webhook registered"
else
  #############################################
  # PROVIDER: ValidatingAdmissionPolicy (no server)
  #############################################
  log "Applying ValidatingAdmissionPolicy in root:provider"
  kubectl apply -f "$DIR/validatingadmissionpolicy.yaml"
  ok "policy applied"
fi

#############################################
# CONSUMER workspace: bind + test
#############################################
log "Creating CONSUMER workspace root:consumer and binding the export"
"$WS" :root >/dev/null
"$WS" create consumer --ignore-existing --enter >/dev/null
kubectl apply -f "$DIR/apibinding.yaml"

log "Waiting for the cowboys API to be served in root:consumer"
for _ in $(seq 1 60); do
  if kubectl get cowboys -A >/dev/null 2>&1; then break; fi
  sleep 1
done
kubectl get cowboys -A >/dev/null 2>&1 && ok "cowboys API is served"

log "TEST 1: create a GOOD cowboy (expect ADMITTED)"
if kubectl apply -f "$DIR/cowboy-good.yaml"; then
  ok "good cowboy admitted"
else
  fail "good cowboy was unexpectedly rejected"; exit 1
fi

log "TEST 2: create a BAD cowboy (expect REJECTED)"
if kubectl apply -f "$DIR/cowboy-bad.yaml" 2>/tmp/cowboy-bad.err; then
  fail "bad cowboy was admitted -- validation did NOT work"; exit 1
else
  ok "bad cowboy rejected as expected:"
  sed 's/^/    /' /tmp/cowboy-bad.err
fi

log "DONE"
echo "Provider webhook/policy validated objects created in the consumer workspace."
echo "Inspect with:   KUBECONFIG=$KUBECONFIG $WS :root:consumer && kubectl get cowboys"
echo "Tear down with: $DIR/run.sh clean"
