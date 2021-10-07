#!/bin/bash

CURRENT_DIR="$(pwd)"
DEMO_ROOT="$(dirname "${BASH_SOURCE}")"
KCP_ROOT="$(cd ${DEMO_ROOT}/../.. && pwd)"
export KCP_DATA_ROOT=${KCP_DATA_ROOT:-$KCP_ROOT}

source ${DEMO_ROOT}/.startUtils
setupTraps $0

KUBECONFIG=${KCP_DATA_ROOT}/.kcp/data/admin.kubeconfig
export KCP_LISTEN_ADDR="127.0.0.1:6443"

${DEMO_ROOT}/startKcpAndClusterController.sh -auto_publish_apis=false deployments.apps &

wait_command "grep 'Serving securely' ${CURRENT_DIR}/kcp.log"

echo ""
echo "Use ctrl-C to stop all components"
echo ""

wait
