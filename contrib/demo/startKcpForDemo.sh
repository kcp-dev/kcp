#!/bin/bash

trap cleanup 1 2 3 6

cleanup() {
  echo "Killing KCP and the controllers"
  kill $KCP_PID $CC_PID $SPLIT_PID $TAIL_PID
}

CURRENT_DIR="$(pwd)"
DEMO_ROOT="$(dirname "${BASH_SOURCE}")"
KCP_ROOT="$(cd ${DEMO_ROOT}/../.. && pwd)"

KUBECONFIG=${KCP_ROOT}/.kcp/data/admin.kubeconfig

echo "Starting KCP server ..."
(cd ${KCP_ROOT} && exec ./bin/kcp start) &> kcp.log &
KCP_PID=$!
echo "KCP server started: $KCP_PID" 

echo "Waiting for KCP server to be up and running..." 
sleep 10

echo ""
echo "Starting Cluster Controller..."
${KCP_ROOT}/bin/cluster-controller -push_mode=true -pull_mode=false -auto_publish_apis=true -kubeconfig=${KUBECONFIG} deployments.apps &> cluster-controller.log &
CC_PID=$!
echo "Cluster Controller started: $CC_PID" 

echo ""
echo "Starting Deployment Splitter"
${KCP_ROOT}/bin/deployment-splitter -kubeconfig=${KCP_ROOT}/.kcp/data/admin.kubeconfig &> deployment-splitter.log &
SPLIT_PID=$!
echo "Deployment Splitter started: $SPLIT_PID" 

echo ""
echo "Use ctrl-C to stop all components"
echo ""

tail -f cluster-controller.log deployment-splitter.log  &
TAIL_PID=$!

wait