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

CRD_DOCS_GENERATOR_VERSION=0.10.0
# set destination to crd-reference directory in the docs
DESTINATION="docs/content/en/main/crd-reference"

# Clear output folder
find "${PWD}/${DESTINATION}" -type f -not -name "_index.md" | xargs -I '{}' rm '{}'

BIND_MOUNT_OPTS=":z"
if [[ $(uname -s) == "Darwin" ]]; then
  BIND_MOUNT_OPTS=""
fi

# Generate new content
podman run --rm \
    -v "${PWD}"/${DESTINATION}:/opt/crd-docs-generator/output"${BIND_MOUNT_OPTS}" \
    -v "${PWD}"/hack/generate/crd-ref:/opt/crd-docs-generator/config"${BIND_MOUNT_OPTS}" \
    quay.io/giantswarm/crd-docs-generator:${CRD_DOCS_GENERATOR_VERSION} \
    --config /opt/crd-docs-generator/config/config.yaml
