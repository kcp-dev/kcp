#!/usr/bin/env bash

# Copyright 2024 The kcp Authors.
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

# This script bundles CRDs to be used in relation to goreleaser.
CRDS_DIR=$(dirname "${0}")

CRDS_FILE="${CRDS_DIR}/crds.yaml"
rm -f "${CRDS_FILE}"
for file in "${CRDS_DIR}"/apis.kcp.*.yaml "${CRDS_DIR}"/cache.kcp.*.yaml \
            "${CRDS_DIR}"/core.kcp.*.yaml "${CRDS_DIR}"/tenancy.kcp.*.yaml \
            "${CRDS_DIR}"/topology.kcp.*.yaml; do
    cat "${file}" >> "${CRDS_FILE}"
    echo "--" >> "${CRDS_FILE}"
    echo "${file}" >> /tmp/debug
done
