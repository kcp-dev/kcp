#!/bin/bash

CURRENT_DIR="$(pwd)"
DEMO_ROOT="$(dirname "${BASH_SOURCE}")"
KCP_ROOT="$(cd ${DEMO_ROOT}/../.. && pwd)"
KCP_DATA_ROOT=${KCP_DATA_ROOT:-$KCP_ROOT}


KUBECONFIG=${KCP_DATA_ROOT}/.kcp/data/admin.kubeconfig

source ${DEMO_ROOT}/.startUtils
setupTraps $0 "rm -Rf ${CURRENT_DIR}/.kcp.running"

KCP_FLAGS=""

if [ ! -z "${KCP_LISTEN_ADDR}" ]; then
    KCP_FLAGS="--listen=${KCP_LISTEN_ADDR} "
fi

echo "Starting KCP server ..."
(cd ${KCP_DATA_ROOT} && exec ${KCP_ROOT}/bin/kcp start ${KCP_FLAGS}) &> kcp.log &
KCP_PID=$!
echo "KCP server started: $KCP_PID" 

echo "Waiting for KCP server to be up and running..." 
wait_command "grep 'Serving securely' ${CURRENT_DIR}/kcp.log"

echo "Applying CRDs..."
kubectl apply -f config/

echo ""
echo "Starting Cluster Controller..."
${KCP_ROOT}/bin/cluster-controller -push_mode=true -pull_mode=false -kubeconfig=${KUBECONFIG} "$@" &> cluster-controller.log &
CC_PID=$!
echo "Cluster Controller started: $CC_PID" 

echo ""
echo "Use ctrl-C to stop all components"
echo ""

tail -f cluster-controller.log &

wait
