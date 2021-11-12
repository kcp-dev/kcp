#!/bin/bash

set -o errexit
set -o nounset
set -o pipefail
#set -o xtrace

DEMO_ROOT="$(dirname "${BASH_SOURCE[0]}")"
KCP_ROOT="$(realpath "${DEMO_ROOT}/../../..")"

workdir="${WORKDIR:-"$( mktemp -d )"}"
echo "Workdir: ${workdir}"
pushd "${workdir}"

function cleanup() {
  for job in $( jobs -p ); do
    kill "${job}"
  done
  wait
}
trap cleanup EXIT

echo "Starting kcp1..."
"${KCP_ROOT}"/bin/kcp start --enable-sharding --root_directory ".kcp1" > ".kcp1.log" 2>&1 &
for i in {1..10}; do
    if grep -q "Serving securely" ".kcp1.log"; then
      break
    fi
    echo -n .
    sleep 1
done
echo

echo "Bootstrapping CRDs on kcp1..."
for z in {1..10} ; do
  if kubectl --kubeconfig ".kcp1/data/admin.kubeconfig" --context cross-cluster get ns -o yaml | grep -q kube-system; then
    kubectl --kubeconfig ".kcp1/data/admin.kubeconfig" apply -f "${KCP_ROOT}/config"
    break
  fi
  echo -n .
  sleep 1
done
echo

echo "Starting Cluster Controller on kcp1..."
"${KCP_ROOT}"/bin/cluster-controller -push_mode=true -pull_mode=false -kubeconfig=".kcp1/data/admin.kubeconfig" -auto_publish_apis=true .configmaps &> .kcp1.cluster-controller.log 2>&1 &

echo "Starting kcp2..."
"${KCP_ROOT}"/bin/kcp start --enable-sharding --shard-kubeconfig-file ".kcp1/data/shard.kubeconfig" --root_directory ".kcp2" --etcd_client_port 2381 --etcd_peer_port 2382 --listen :6444 > ".kcp2.log" 2>&1 &
for i in {1..10} ; do
    if grep -q "Serving securely" ".kcp2.log"; then
      break
    fi
    echo -n .
    sleep 1
done
echo

for z in {1..10} ; do
  if kubectl --kubeconfig ".kcp2/data/admin.kubeconfig" --context cross-cluster get ns -o yaml | grep -q kube-system; then
    kubectl --kubeconfig ".kcp2/data/admin.kubeconfig" apply -f "${KCP_ROOT}/config"
    break
  fi
  echo -n .
  sleep 1
done
echo

echo "Starting Cluster Controller on kcp2..."
"${KCP_ROOT}"/bin/cluster-controller -push_mode=true -pull_mode=false -kubeconfig=".kcp1/data/admin.kubeconfig" -auto_publish_apis=true .configmaps &> .kcp1.cluster-controller.log 2>&1 &

"${KCP_ROOT}"/contrib/demo/sharding/add-cluster.py ".kcp1/data/admin.kubeconfig" ".kcp2/data/admin.kubeconfig"
touch .ready
wait
