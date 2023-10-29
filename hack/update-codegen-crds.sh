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

if [[ -z "${CONTROLLER_GEN:-}" ]]; then
    echo "You must either set CONTROLLER_GEN to the path to controller-gen or invoke via make"
    exit 1
fi

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

# Update generated CRD YAML
(
    cd "${REPO_ROOT}/sdk/apis"
    "../../${CONTROLLER_GEN}" \
        crd \
        rbac:roleName=manager-role \
        webhook \
        paths="./..." \
        output:crd:artifacts:config="${REPO_ROOT}"/config/crds
)

for CRD in "${REPO_ROOT}"/config/crds/*.yaml; do
    if [ -f "${CRD}-patch" ]; then
        echo "Applying ${CRD}"
        ${YAML_PATCH} -o "${CRD}-patch" < "${CRD}" > "${CRD}.patched"
        mv "${CRD}.patched" "${CRD}"
    fi
done

(
  ${KCP_APIGEN_GEN} --input-dir "${REPO_ROOT}"/config/crds --output-dir "${REPO_ROOT}"/config/root-phase0
)


# Tests CRDs

(
    cd "${REPO_ROOT}/test/e2e/fixtures/wildwest/apis"
    "${REPO_ROOT}/${CONTROLLER_GEN}" \
        crd \
        rbac:roleName=manager-role \
        webhook \
        paths="./..." \
        output:crd:artifacts:config="${REPO_ROOT}"/test/e2e/fixtures/wildwest/crds
)
