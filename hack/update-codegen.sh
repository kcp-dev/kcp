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

if [[ -z "${MAKELEVEL:-}" ]]; then
    echo 'You must invoke this script via make'
    exit 1
fi

"$(dirname "${BASH_SOURCE[0]}")/update-codegen-clients.sh"

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

for CRD in ./config/crds/*.yaml; do
    if [[ "${CRD}" == *.apis.kcp.dev.yaml ]]; then
        continue
    fi

    NAME=$(grep -E "^  name: " "${CRD}" | sed 's/  name: //')
    SCHEMA="config/root-phase0/apiresourceschema-${NAME}.yaml"

    echo -n "Verifying ${SCHEMA} ... "

    PREFIX=v$(date '+%y%m%d')-$(git rev-parse --short HEAD)
    ./bin/kubectl-kcp crd snapshot -f "${CRD}" --prefix "${PREFIX}" > new.yaml
    if [ -f "${SCHEMA}" ] && diff -q <(grep -E -v "^  name: " "${SCHEMA}") <(grep -E -v "^  name: " new.yaml); then
        echo "OK"
        rm -f new.yaml
    else
        echo "Updating"
        mv new.yaml "${SCHEMA}"

        SCHEMA_NAME=$(grep -E "^  name: " "${SCHEMA}" | sed 's/  name: [^.]*\.//')
        sed -i '' -e "s/^  - [a-z0-9][^.]*\.$(echo "${SCHEMA_NAME}" | sed 's/\./\\./g')/  - ${PREFIX}.${NAME}/" config/root-phase0/apiexport-*.yaml
    fi
done