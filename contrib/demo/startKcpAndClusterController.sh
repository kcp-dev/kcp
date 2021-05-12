#!/bin/bash

trap cleanup 1 2 3 6

cleanup() {
  echo "Killing KCP and the controllers"
  kill $KCP_PID $CC_PID $TAIL_PID
}

CURRENT_DIR="$(pwd)"
DEMO_ROOT="$(dirname "${BASH_SOURCE}")"
KCP_ROOT="$(cd ${DEMO_ROOT}/../.. && pwd)"

KUBECONFIG=${KCP_ROOT}/.kcp/data/admin.kubeconfig

echo "Starting KCP"
(cd ${KCP_ROOT} && exec ./bin/kcp start) &> kcp.log &
KCP_PID=$!

sleep 5

echo "Starting Cluster Controller"
${KCP_ROOT}/bin/cluster-controller -pull_model=false -kubeconfig=${KUBECONFIG} deployments &> cluster-controller.log &
CC_PID=$!

echo "Use ctrl-C to stop them"
echo ""

tail -f cluster-controller.log &
TAIL_PID=$!

wait