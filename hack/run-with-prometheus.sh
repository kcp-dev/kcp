#!/usr/bin/env bash

# Copyright 2023 The kcp Authors.
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

# Usage: ./hack/run-with-prometheus.sh <command> [args...]
#
# Starts a local Prometheus (http://localhost:9090) using .prometheus-config.yaml,
# runs the given command, then tears Prometheus down and exits with the command's
# exit code. Without arguments the script runs nothing, exits 0, and immediately
# kills Prometheus — you must pass a command.
#
# Examples:
#   # Single-node kcp (production binary). The dev watcher below auto-populates
#   # .prometheus-config.yaml with a scrape config pointing at localhost:6443.
#   ./hack/run-with-prometheus.sh go run ./cmd/kcp start
#   ./hack/run-with-prometheus.sh ./bin/kcp start
#
#   # Sharded setup via the dev test-server — the server itself registers scrape
#   # configs (one job per shard plus kcp-front-proxy) through the Go testing
#   # framework, so nothing extra is needed here. Build once with `make build-e2e`.
#   ./hack/run-with-prometheus.sh ./bin/sharded-test-server --work-dir-path=.
#   ./hack/run-with-prometheus.sh ./bin/sharded-test-server --work-dir-path=. --number-of-shards=3
#
# When a local cmd/kcp dev server is detected (.kcp/apiserver.crt and
# .kcp/admin.kubeconfig appear in the repo root), the script auto-writes a scrape
# config for it and reloads Prometheus. sharded-test-server writes .kcp/serving-ca.crt
# (no apiserver.crt) so the watcher stays silent and lets the server's own
# ScrapeMetrics calls populate .prometheus-config.yaml. E2e runs use tempdirs and
# follow the same pattern — watcher idle, Go framework owns the config.
#
# If ARTIFACT_DIR is set, the tsdb is archived to
# ${ARTIFACT_DIR}/metrics/prometheus.tar after shutdown.

set -o nounset
set -o pipefail
set -o errexit

cd "$(dirname $0)/.."

PROMETHEUS="$(UGET_PRINT_PATH=absolute make --no-print-directory prometheus | tail -n 1)"

if [ ! -x "${PROMETHEUS}" ]; then
  echo "prometheus binary not found at ${PROMETHEUS}" >&2
  exit 1
fi

touch .prometheus-config.yaml
"$PROMETHEUS" \
  --storage.tsdb.path=".prometheus_data" \
  --config.file=".prometheus-config.yaml" \
  --web.enable-lifecycle \
  --log.level="warn" &
PROM_PID=$!
export PROMETHEUS_URL="http://localhost:9090"
echo 'Waiting for Prometheus to be ready...'
while ! curl -s http://localhost:9090/-/ready; do
  sleep 1
done
echo 'Prometheus is ready!'

# Dev-only: when running a local `kcp start` (not in Prow CI), watch for kcp's
# cert+kubeconfig to appear and auto-populate a scrape config. In Prow, e2e tests
# populate .prometheus-config.yaml via the Go testing framework
# (staging/.../testing/server/metrics.go) — this watcher would race and clobber
# those per-server entries, so it stays off. Gating on PROW_JOB_ID rather than
# ARTIFACT_DIR because users set ARTIFACT_DIR locally to collect tsdb archives.
WATCHER_PID=""
if [ -z "${PROW_JOB_ID:-}" ]; then
  (
    for _ in $(seq 1 120); do
      if [ -f .kcp/apiserver.crt ] && [ -f .kcp/admin.kubeconfig ]; then
        # Use the shard-admin token (system:master) — kcp-admin is workspace-scoped
        # and gets 401 on the raw /metrics endpoint.
        TOKEN=$(awk '/name: shard-admin/{found=1} found && /token:/{print $2; exit}' .kcp/admin.kubeconfig)
        if [ -n "${TOKEN}" ]; then
          cat > .prometheus-config.yaml <<EOF
scrape_configs:
  - job_name: kcp
    scrape_interval: 5s
    scheme: https
    bearer_token: ${TOKEN}
    tls_config:
      ca_file: $(pwd)/.kcp/apiserver.crt
    static_configs:
      - targets: ["localhost:6443"]
EOF
          curl -sX POST http://localhost:9090/-/reload >/dev/null
          echo 'Wrote kcp scrape config and reloaded Prometheus.'
          exit 0
        fi
      fi
      sleep 1
    done
  ) &
  WATCHER_PID=$!
fi

set -o xtrace
set +o errexit
"${@}"
EXIT_CODE=${?}
set +o xtrace
set -o errexit
echo "Command terminated with ${EXIT_CODE}"

[ -n "${WATCHER_PID}" ] && kill -TERM ${WATCHER_PID} 2>/dev/null || true
kill -TERM ${PROM_PID}

if [ -n "${ARTIFACT_DIR:=}" ]; then
  echo 'Waiting for Prometheus to shut down...'
  while [ -f .prometheus_data/lock ]; do sleep 1; done
  echo 'Prometheus shut down successfully!'
  mkdir -p ${ARTIFACT_DIR}/metrics
  tar cvzf ${ARTIFACT_DIR}/metrics/prometheus.tar -C .prometheus_data .
fi

exit "${EXIT_CODE}"
