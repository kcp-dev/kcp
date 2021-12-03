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
TEMP_DIR="$(mktemp -d)"
CLUSTERS_DIR="${DEMO_ROOT}/clusters"
export KCP_DATA_ROOT=${KCP_DATA_ROOT:-$KCP_ROOT}

source "${DEMO_ROOT}"/.startUtils
setupTraps $0

if ! command -v envoy &> /dev/null
then
    echo "The envoy binary could not be found, this demo requires it."
    echo "To install envoy refer to: https://www.envoyproxy.io/docs/envoy/latest/start/install"
    exit
fi

export KCP_LISTEN_ADDR="127.0.0.1:6443"


"${DEMO_ROOT}"/startKcpAndClusterController.sh --auto-publish-apis=true --resources-to-sync "ingresses.networking.k8s.io,deployments.apps,services" &
KCP_PID=$!

wait_command "grep 'Serving securely' ${CURRENT_DIR}/kcp.log"

kubectl config use-context admin &>/dev/null

echo ""
echo "Building KCP-Ingress controller"

git clone --depth=1 https://github.com/jmprusi/kcp-ingress "${TEMP_DIR}"
pushd "${TEMP_DIR}" && go build -o ${KCP_ROOT}/bin/kcp-ingress cmd/ingress-controller/main.go &> ${DEMO_ROOT}/ingress-test/kcp-ingress_build.log
popd

echo "" 
echo "Running the kcp-ingress controller"

${KCP_ROOT}/bin/kcp-ingress -kubeconfig="${KUBECONFIG}" -envoyxds -envoy-listener-port=8181 &>kcp-ingress.log &
KCP_INGRESS_PID=$!
echo "KCP Ingress started: $KCP_INGRESS_PID"

echo ""
echo "Starting Envoy"
envoy -c "${TEMP_DIR}"/utils/envoy/bootstrap.yaml &>envoy.log &
ENVOY_PID=$!
echo "Envoy started: $ENVOY_PID"

echo ""
echo "Use ctrl-C to stop all components"
echo ""

wait
