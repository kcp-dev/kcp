#!/usr/bin/env bash

# Copyright 2021 The KCP Authors.
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

set -o errexit
set -o nounset
set -o pipefail
set -o xtrace

for binary in kind ko; do
  if ! command -v "${binary}" >/dev/null 2>&1; then
    echo "[ERROR] ${binary} not found in \$PATH."
    exit 1
  fi
done

workdir="$(mktemp -d)"

trap cleanup EXIT

function cleanup() {
  for job in $( jobs -p ); do
    kill "${job}"
  done
  rm -rf "${workdir}"
}

echo "[INFO] Retrieving kubeconfig for kind cluster..."
kind get kubeconfig > "${workdir}/kind.kubeconfig"

echo "[INFO] Building images..."
SYNCER_IMAGE="$( KO_DOCKER_REPO=kind.local ko build --platform=linux/${ARCH} ./cmd/syncer 2>/dev/null )"
TEST_IMAGE="$( KO_DOCKER_REPO=kind.local ko build --platform=linux/${ARCH} ./test/e2e/fixtures/kcp-test-image 2>/dev/null )"

echo "[INFO] Starting test server..."
NO_GORUN=1 ./bin/sharded-test-server --v=2 --log-dir-path="${LOG_DIR}" ${TEST_SERVER_ARGS} 2>&1 &

echo "[INFO] Waiting for test server to be ready..."
for (( i = 0; i < 60; i++ )); do
    if [[ -f .kcp/admin.kubeconfig ]]; then
      break
    fi
    sleep 1
done

echo "[INFO] Running end-to-end tests..."
NO_GORUN=1 ${GO_TEST} -race -count ${COUNT} -p ${E2E_PARALLELISM} -parallel ${E2E_PARALLELISM} ${WHAT} ${TEST_ARGS} \
		-args --use-default-kcp-server --syncer-image="${SYNCER_IMAGE}" --kcp-test-image="${TEST_IMAGE}" --pcluster-kubeconfig="${workdir}/kind.kubeconfig"