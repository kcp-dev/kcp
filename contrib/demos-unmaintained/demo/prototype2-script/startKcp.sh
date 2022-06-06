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

STANDALONE_VIRTUAL_WORKSPACE=${STANDALONE_VIRTUAL_WORKSPACE:-true}

DEMO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../.setupEnv
source "${DEMO_DIR}"/../.setupEnv
# shellcheck source=../.startUtils
source "${DEMOS_DIR}"/.startUtils
setupTraps "$0"

for sig in INT QUIT HUP TERM; do
  trap "
    cleanup
    trap - $sig EXIT
    kill -s $sig "'"$$"' "$sig"
done
trap "cleanup" EXIT

cleanup() {
  echo "Killing Envoy container"
  "${CONTAINER_ENGINE}" kill "${ENVOY_CID}"
}

if ! command -v envoy &> /dev/null; then
    echo "envoy is required - please install and try again"
    exit 1
fi

detect_container_engine() {
    if ! command -v podman; then
        CONTAINER_ENGINE=docker
        return
    fi
    if [[ "$OSTYPE" == "darwin"* && -z "$(podman ps)" ]]; then
        # Podman machine is not started
        CONTAINER_ENGINE=docker
        return
    fi
    if [[ -z "$(podman system connection ls --format=json)" ]]; then
        CONTAINER_ENGINE=docker
        return
    fi
    CONTAINER_ENGINE=podman
}

detect_container_engine

CURRENT_DIR="$(pwd)"

KUBECONFIG=${KCP_DATA_DIR}/.kcp/admin.kubeconfig

VW_ARGS=""
if [ "${STANDALONE_VIRTUAL_WORKSPACE}" = "true" ]; then
    VW_ARGS="--run-virtual-workspaces=false --virtual-workspace-address=https://127.0.0.1:6444 --bind-address=127.0.0.1 --external-hostname=127.0.0.1"
fi

"${DEMOS_DIR}"/startKcp.sh \
    --token-auth-file "${DEMO_DIR}"/kcp-tokens \
    --push-mode \
    --discovery-poll-interval 3s \
    --profiler-address localhost:6060 \
    --resources-to-sync ingresses.networking.k8s.io,deployments.apps,services \
    ${VW_ARGS} \
    -v 2 &

wait_command "ls ${KUBECONFIG}"
echo "Waiting for KCP to be ready ..."
wait_command "kubectl --kubeconfig=${KUBECONFIG} get --raw /readyz"

echo ""
echo "Starting Ingress Controller"
"${KCP_DIR}"/bin/ingress-controller --kubeconfig="${KUBECONFIG}" --context=system:admin --envoy-listener-port=8181 --envoy-xds-port=18000 &> "${CURRENT_DIR}"/ingress-controller.log &
INGRESS_CONTROLLER_PID=$!
echo "Ingress Controller started: ${INGRESS_CONTROLLER_PID}"

echo ""
echo "Starting envoy"
bootstrapAddress="host.docker.internal"
if [[ "${CONTAINER_ENGINE}" == "podman" ]]; then
  bootstrapAddress="host.containers.internal"
fi
sed "s/BOOTSTRAP_ADDRESS/$bootstrapAddress/" "${KCP_DIR}"/contrib/envoy/bootstrap.template.yaml > "${KCP_DATA_DIR}"/envoy-bootstrap.yaml
"${CONTAINER_ENGINE}" create --rm -t --net=kind -p 8181:8181 envoyproxy/envoy-dev:d803505d919aff1c4207b353c3b430edfa047010
ENVOY_CID=$("${CONTAINER_ENGINE}" ps -q -n1)
"${CONTAINER_ENGINE}" cp "${KCP_DATA_DIR}"/envoy-bootstrap.yaml "${ENVOY_CID}":/etc/envoy/envoy.yaml
"${CONTAINER_ENGINE}" start "${ENVOY_CID}"
"${CONTAINER_ENGINE}" logs -f "${ENVOY_CID}" &> "${CURRENT_DIR}"/envoy.log &
echo "Envoy started in container: ${ENVOY_CID}"

if [ "${STANDALONE_VIRTUAL_WORKSPACE}" = "true" ]; then
  echo ""
  echo "Starting Virtual Workspace"
  "${KCP_DIR}"/bin/virtual-workspaces workspaces \
      --bind-address=127.0.0.1 \
      --kubeconfig "${KUBECONFIG}" \
      --authentication-kubeconfig "${KUBECONFIG}" \
      --tls-cert-file "${KCP_DATA_DIR}"/.kcp/apiserver.crt \
      --tls-private-key-file "${KCP_DATA_DIR}"/.kcp/apiserver.key \
      &> "${CURRENT_DIR}"/virtual-workspace.log &
  SPLIT_PID=$!
  echo "Virtual Workspace started: $SPLIT_PID"
fi

touch "${KCP_DATA_DIR}/servers-ready"

echo ""
echo "Use ctrl-C to stop all components"
echo ""

wait
