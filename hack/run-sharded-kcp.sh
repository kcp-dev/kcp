#!/usr/bin/env bash

# Copyright 2022 The KCP Authors.
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

set -o nounset
set -o pipefail
set -o errexit

function cleanup() {
  for job in $( jobs -p ); do
    kill "${job}"
  done
  wait
}
trap cleanup EXIT

echo "Starting sharded kcp..."
rm -rf "${WORKDIR}"
mkdir -p "${WORKDIR}"
./contrib/demo/sharding/run.sh &

for i in {1..25} ; do
    if [[ -e "${WORKDIR}/.ready" ]]; then
      break
    fi
    echo -n '.'
    sleep 1
done
echo

echo "Running end-to-end tests..."
kubernetes_path="${KUBERNETES_PATH:-"${GITHUB_WORKSPACE}/kubernetes"}"
kubeconfig="${WORKDIR}/.kcp2/admin.kubeconfig"

## each of the single-cluster contexts passes normal e2e
for context in "admin" "user" "other"; do
  "${kubernetes_path}/_output/local/bin/linux/amd64/e2e.test" --e2e-verify-service-account=false --ginkgo.focus "Watchers" \
    --kubeconfig "${kubeconfig}" --context "${context}" \
    --provider local
done

# the multi-cluster list/watcher works with all the single-cluster contexts
"${kubernetes_path}/_output/local/bin/linux/amd64/e2e.test" --e2e-verify-service-account=false --ginkgo.focus "KCP MultiCluster" \
  --kubeconfig "${kubeconfig}" --context "admin" \
  --provider kcp \
  --kcp-multi-cluster-kubeconfig "${kubeconfig}" --kcp-multi-cluster-context "cross-cluster" \
  --kcp-secondary-kubeconfig "${kubeconfig}" --kcp-secondary-context "user" \
  --kcp-tertiary-kubeconfig "${kubeconfig}" --kcp-tertiary-context "other" \
  --kcp-clusterless-kubeconfig "${kubeconfig}" --kcp-clusterless-context "admin"