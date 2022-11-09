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
# set -o xtrace

if [[ -z "${MAKELEVEL:-}" ]]; then
    echo 'You must invoke this script via make'
    exit 1
fi

# Update generated CRD YAML
cd pkg/apis
../../${CONTROLLER_GEN} \
    crd \
    rbac:roleName=manager-role \
    webhook \
    paths="./..." \
    output:crd:artifacts:config=../../config/crds
cd -

for CRD in ./config/crds/*.yaml; do
    if [ -f "${CRD}-patch" ]; then
        echo "Applying ${CRD}"
        ${YAML_PATCH} -o "${CRD}-patch" < "${CRD}" > "${CRD}.patched"
        mv "${CRD}.patched" "${CRD}"
    fi
done

${CONTROLLER_GEN} \
    crd \
    rbac:roleName=manager-role \
    webhook \
    paths="./test/e2e/reconciler/cluster/..." \
    output:crd:artifacts:config=test/e2e/reconciler/cluster/

go run ./cmd/apigen --input-dir ./config/crds --output-dir  ./config/root-phase0
