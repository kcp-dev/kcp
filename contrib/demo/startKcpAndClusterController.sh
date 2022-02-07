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
DEMOS_DIR="$(dirname "${BASH_SOURCE[0]}")"
KCP_DIR="$(cd "${DEMOS_DIR}"/../.. && pwd)"
KCP_DATA_DIR=${KCP_DATA_DIR:-$KCP_DIR}

KUBECONFIG=${KCP_DATA_DIR}/.kcp/admin.kubeconfig

source "${DEMOS_DIR}"/.startUtils
setupTraps "$0" "rm -Rf ${CURRENT_DIR}/.kcp.running"

KCP_FLAGS=${KCP_FLAGS:-""}

if [ -n "${KCP_LISTEN_ADDR}" ]; then
    KCP_FLAGS="--listen=${KCP_LISTEN_ADDR} ${KCP_FLAGS}"
fi

echo "Starting KCP server ..."
(cd "${KCP_DATA_DIR}" && exec "${KCP_DIR}"/bin/kcp start "${KCP_FLAGS}") &> kcp.log &
KCP_PID=$!
echo "KCP server started: $KCP_PID"

echo "Waiting for KCP server to be up and running..."
wait_command "grep 'Serving securely' ${CURRENT_DIR}/kcp.log"

echo "Applying CRDs..."
kubectl --kubeconfig "$KUBECONFIG" apply -f config/

echo ""
echo "Starting Cluster Controller..."
"${KCP_DIR}"/bin/cluster-controller --push-mode=true --pull-mode=false --kubeconfig="${KUBECONFIG}" "$@" &> cluster-controller.log &
CC_PID=$!
echo "Cluster Controller started: $CC_PID"

echo ""
echo "Use ctrl-C to stop all components"
echo ""

tail -f cluster-controller.log &

wait
