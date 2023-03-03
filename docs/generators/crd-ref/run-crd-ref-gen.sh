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

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)

CONTAINER_ENGINE=${CONTAINER_ENGINE:-podman}
CRD_DOCS_GENERATOR_VERSION=0.10.0

#TODO(ncdc): i18n
DESTINATION="${REPO_ROOT}/docs/content/reference/crd"
mkdir -p "${DESTINATION}"

BIND_MOUNT_OPTS=":z"
if [[ $(uname -s) == "Darwin" ]]; then
  BIND_MOUNT_OPTS=""
fi

# Generate new content
$CONTAINER_ENGINE run --rm \
    -v "${DESTINATION}":/opt/crd-docs-generator/output"${BIND_MOUNT_OPTS}" \
    -v "${REPO_ROOT}"/docs/generators/crd-ref:/opt/crd-docs-generator/config"${BIND_MOUNT_OPTS}" \
    quay.io/giantswarm/crd-docs-generator:${CRD_DOCS_GENERATOR_VERSION} \
    --config /opt/crd-docs-generator/config/config.yaml
