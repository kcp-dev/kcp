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
"${KCP_ROOT}"/bin/kcp start --enable-sharding --root-directory ".kcp1" > ".kcp1.log" 2>&1 &
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
  if kubectl --kubeconfig ".kcp1/admin.kubeconfig" --context cross-cluster get ns -o yaml | grep -q kube-system; then
    kubectl --kubeconfig ".kcp1/admin.kubeconfig" apply -f "${KCP_ROOT}/config"
    break
  fi
  echo -n .
  sleep 1
done
echo

echo "Starting Cluster Controller on kcp1..."
"${KCP_ROOT}"/bin/cluster-controller -push-mode=true -pull-mode=false -kubeconfig=".kcp1/admin.kubeconfig" .configmaps &> .kcp1.cluster-controller.log 2>&1 &

echo "Starting kcp2..."
"${KCP_ROOT}"/bin/kcp start --enable-sharding --shard-kubeconfig-file ".kcp1/data/shard.kubeconfig" --root-directory ".kcp2" --etcd-client-port 2381 --etcd-peer-port 2382 --secure-port :6444 > ".kcp2.log" 2>&1 &
for i in {1..10} ; do
    if grep -q "Serving securely" ".kcp2.log"; then
      break
    fi
    echo -n .
    sleep 1
done
echo

for z in {1..10} ; do
  if kubectl --kubeconfig ".kcp2/admin.kubeconfig" --context cross-cluster get ns -o yaml | grep -q kube-system; then
    kubectl --kubeconfig ".kcp2/admin.kubeconfig" apply -f "${KCP_ROOT}/config"
    break
  fi
  echo -n .
  sleep 1
done
echo

echo "Starting Cluster Controller on kcp2..."
"${KCP_ROOT}"/bin/cluster-controller -push-mode=true -pull-mode=false -kubeconfig=".kcp1/admin.kubeconfig" .configmaps &> .kcp1.cluster-controller.log 2>&1 &

"${KCP_ROOT}"/contrib/demo/sharding/add-cluster.py ".kcp1/admin.kubeconfig" ".kcp2/admin.kubeconfig"
touch .ready
wait
