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

CURRENT_DIR="$(pwd)"
DEMO_ROOT="$(dirname "${BASH_SOURCE}")"
KCP_ROOT="$(cd ${DEMO_ROOT}/../.. && pwd)"
KCP_DATA_ROOT=${KCP_DATA_ROOT:-$KCP_ROOT}


KUBECONFIG=${KCP_DATA_ROOT}/.kcp/admin.kubeconfig

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
kubectl --kubeconfig $KUBECONFIG apply -f config/

echo ""
echo "Starting Cluster Controller..."
${KCP_ROOT}/bin/cluster-controller --push-mode=true --pull-mode=false --kubeconfig=${KUBECONFIG} "$@" &> cluster-controller.log &
CC_PID=$!
echo "Cluster Controller started: $CC_PID" 

echo ""
echo "Use ctrl-C to stop all components"
echo ""

tail -f cluster-controller.log &

wait
