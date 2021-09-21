#!/bin/bash

trap cleanup 1 2 3 6

cleanup() {
  echo "Killing KCP and the KCP-OCM controllers"
  kill $KCP_PID $CC_PID
}

KUTTL_DIR="$( cd `dirname "${BASH_SOURCE[0]}"` && pwd )"
ROOT_DIR="$( cd ${KUTTL_DIR}/../.. && pwd)"

#clear out test kcp data
rm -rf ${KUTTL_DIR}/.kcp

echo "Starting KCP..."
#NOTE: `kcp start --install_cluster_controller` expect cluster controller to be run from the root project directory
(cd ${KUTTL_DIR} && exec ${ROOT_DIR}/bin/kcp start) &> ${KUTTL_DIR}/kcp.log & #TODO add --resources_to_sync
KCP_PID=$!
ps aux | grep $KCP_PID
echo "KCP server started: ${KCP_PID}"

export KUBECONFIG=${KUTTL_DIR}/.kcp/data/admin.kubeconfig

echo -ne "Waiting for KCP server to be up and running"
i=0
while [ $i -lt 10 ]; do
  echo -ne "."
  kubectl get namespace &> /dev/null
  if [ "$?" = "0" ]; then
    echo ""
    break
  fi
  i=$((i+1))
  sleep 1
done

if [ $i -eq 10 ]; then
    echo ""
    echo "KCP not running check the ${KUTTL_DIR}/kcp.log"
    exit 1
fi

echo "Starting Cluster Controller..."
${ROOT_DIR}/bin/cluster-controller -push_mode=true -pull_mode=false -kubeconfig=${KUBECONFIG} &> ${KUTTL_DIR}/cluster-controller.log &
CC_PID=$!
echo "Cluster Controller started: ${CC_PID}" 

#hackaround to pass KUTTL initial check
kubectl create namespace default
kubectl create sa default

#start test
(cd ${KUTTL_DIR} && kubectl kuttl test)

cleanup



