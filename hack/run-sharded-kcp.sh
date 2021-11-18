#!/bin/bash

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