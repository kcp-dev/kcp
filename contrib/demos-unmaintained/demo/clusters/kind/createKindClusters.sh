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
set -o xtrace

if [[ -z "${CLUSTERS_DIR:-}" ]]; then
    echo CLUSTERS_DIR is required
    exit 1
fi

DEMO_ROOT="$(dirname "${BASH_SOURCE[0]}")/../.."

get_cluster_port() {
    local cluster=kind-$1
    local clusterPort
    clusterPort=$(kubectl --kubeconfig "${CLUSTERS_DIR}/$1.kubeconfig" config view -o json | jq -r ".clusters[] | select(.name==\"${cluster}\").cluster.server" | cut -d ':' -f 3)
    echo "${clusterPort}"
}

podman_ssh() {
    local podmanMachine
    podmanMachine="${PODMAN_MACHINE:-podman-machine-default*}"

    local sshURL
    sshURL=$(podman system connection ls --format=json | jq -r ".[]|select(.Name==\"${podmanMachine}\").URI" | grep -E -o 'ssh://[^:]+:\d+')

    # Strip trailing * from machine name, if any, to get key name
    local sshKey
    sshKey="${HOME}/.ssh/${podmanMachine%'*'}"

    # Need to use a subshell in DEMO_ROOT so the control socket arg isn't too long (ssh complains with a long absolute path)
    (
        cd "${DEMO_ROOT}"
        ssh -o 'StrictHostKeyChecking no' -i "${sshKey}" "${sshURL}" -f -N -S "${PODMAN_SSH_CONTROL_SOCKET}" "$@"
    )
}

detect_podman() {
    if ! command -v podman; then
        echo "The podman binary could not be found, falling back to Docker."
        return
    fi

    if [[ "$OSTYPE" == "darwin"* && -z "$(podman ps)" ]]; then
        # Podman machine is not started
        echo "The Podman machine does not seem to be started, falling back to Docker."
        return
    fi

    if [[ -z "$(podman system connection ls --format=json)" ]]; then
        echo "The Podman SSH destinations could not be listed, falling back to Docker."
        return
    fi

    echo "Using Podman as container engine."

    KIND_EXPERIMENTAL_PROVIDER=${KIND_EXPERIMENTAL_PROVIDER:-podman}

    PODMAN_VERSION=$(podman version -f '{{.Server.Version}}')
    PODMAN_MAJOR=$(echo "${PODMAN_VERSION}" | cut -d. -f1)
    if [[ "${PODMAN_MAJOR}" == "3" && "$OSTYPE" == "darwin"* ]]; then
        PODMAN_MAC_SSH_WORKAROUND=1

        # Setup the control socket if it's not already running
        if ! test -S "${DEMO_ROOT}/${PODMAN_SSH_CONTROL_SOCKET}"; then
            podman_ssh -M
        fi
    fi
}

detect_podman

CLUSTERS=(
    us-east1
    us-west1
)

EXISTING_CLUSTERS=$(kind get clusters 2>/dev/null)

IMAGE_FLAG=()
if [[ -n "${KIND_IMAGE:-}" ]]; then
    IMAGE_FLAG=(--image "${KIND_IMAGE}")
fi

for cluster in "${CLUSTERS[@]}"; do
    clusterExists=""
    if echo "${EXISTING_CLUSTERS}" | grep "$cluster"; then
        clusterExists="1"
    fi

    # Only create if we're not reusing existing clusters, or the cluster doesn't exist
    if [[ -z "${REUSE_KIND_CLUSTERS:-}" || -z "${clusterExists}" ]]; then
        KIND_EXPERIMENTAL_PROVIDER=${KIND_EXPERIMENTAL_PROVIDER:-} kind create cluster \
            --config "${CLUSTERS_DIR}/${cluster}.config" \
            --kubeconfig "${CLUSTERS_DIR}/${cluster}.kubeconfig" \
            "${IMAGE_FLAG[@]:[]}"
    fi

    if [[ ! -f "${CLUSTERS_DIR}/${cluster}.yaml" ]]; then
        clusterKubeconfig=$(KIND_EXPERIMENTAL_PROVIDER=${KIND_EXPERIMENTAL_PROVIDER:-} kind get kubeconfig --name "${cluster}")

        echo "${clusterKubeconfig}" | sed -e 's/^/    /' | cat "${DEMO_ROOT}/clusters/${cluster}.yaml" - > "${CLUSTERS_DIR}/${cluster}.yaml"
    fi

    if [[ -n "${PODMAN_MAC_SSH_WORKAROUND:-}" ]]; then
        clusterPort=$(get_cluster_port "${cluster}")
        podman_ssh -L "${clusterPort}:localhost:${clusterPort}"
    fi
done
