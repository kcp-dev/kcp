#!/usr/bin/env bash

# Copyright 2022 The KCP Authors.
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

DEMO_DIR="$(dirname "${BASH_SOURCE[0]}")"
# shellcheck source=../.setupEnv
source "${DEMO_DIR}"/../.setupEnv
# shellcheck source=../.startUtils
source "${DEMOS_DIR}"/.startUtils
setupTraps "$0"

if ! command -v envoy 2>/dev/null; then
    echo "envoy is required - please install and try again"
    exit 1
fi

CURRENT_DIR="$(pwd)"

KUBECONFIG=${KCP_DATA_DIR}/.kcp/admin.kubeconfig
"${DEMOS_DIR}"/startKcp.sh \
    --install-namespace-scheduler \
    --install-cluster-controller \
    --token-auth-file "${DEMO_DIR}"/kcp-tokens \
    --auto-publish-apis \
    --push-mode \
    --discovery-poll-interval 3s \
    --resources-to-sync ingresses.networking.k8s.io,deployments.apps,services & # \
    # --listen=127.0.0.1:6443

wait_command "ls ${KUBECONFIG}"

echo ""
echo "Starting Ingress Controller"
ingress-controller --kubeconfig="${KUBECONFIG}" --envoyxds --envoy-listener-port=8181 &> "${CURRENT_DIR}"/ingress-controller.log &
INGRESS_CONTROLLER_PID=$!
echo "Ingress Controller started: ${INGRESS_CONTROLLER_PID}"

echo ""
echo "Starting envoy"
envoy --config-path "${KCP_DIR}"/build/kcp-ingress/utils/envoy/bootstrap.yaml &> "${CURRENT_DIR}"/envoy.log &
ENVOY_PID=$!
echo "Envoy started: ${ENVOY_PID}"

echo ""
echo "Starting Virtual Workspace"
"${KCP_DIR}"/bin/virtual-workspaces workspaces \
    --workspaces:kubeconfig "${KUBECONFIG}" \
    --authentication-kubeconfig "${KUBECONFIG}" \
    --secure-port 6444 \
    --authentication-skip-lookup \
    --cert-dir "${KCP_DATA_DIR}"/.kcp/secrets/ca &> "${CURRENT_DIR}"/virtual-workspace.log &
SPLIT_PID=$!
echo "Virtual Workspace started: $SPLIT_PID"

echo ""
echo "Use ctrl-C to stop all components"
echo ""

wait
