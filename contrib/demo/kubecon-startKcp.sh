#!/bin/bash

CURRENT_DIR="$(pwd)"
DEMO_ROOT="$(dirname "${BASH_SOURCE}")"
KCP_ROOT="$(cd ${DEMO_ROOT}/../.. && pwd)"
export KCP_DATA_ROOT=${KCP_DATA_ROOT:-$KCP_ROOT}

source ${DEMO_ROOT}/.startUtils
setupTraps $0

KUBECONFIG=${KCP_DATA_ROOT}/.kcp/data/admin.kubeconfig
export KCP_LISTEN_ADDR="127.0.0.1:6443"

${DEMO_ROOT}/startKcpAndClusterController.sh -auto_publish_apis=true deployments.apps &
KCP_PID=$!

wait_command "grep 'Serving securely' ${CURRENT_DIR}/kcp.log"

echo ""
echo "Starting Deployment Splitter"
${KCP_ROOT}/bin/deployment-splitter -kubeconfig=${KUBECONFIG} &> deployment-splitter.log &
SPLIT_PID=$!
echo "Deployment Splitter started: $SPLIT_PID" 

echo ""
echo "Use ctrl-C to stop all components"
echo ""

tail -f deployment-splitter.log  &

wait
