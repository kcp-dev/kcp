#!/usr/bin/env bash

# Copyright 2025 The kcp Authors.
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

# This script ensures that the generated client code checked into git is up-to-date
# with the generator. If it is not, re-generate the configuration to update it.

set -o errexit
set -o nounset
set -o pipefail

TARGET_REF=${PULL_BASE_REF:-main}

find config/root-phase0 -name 'apiresourceschema-*.yaml' | while read schema
do
    if ! git diff --exit-code ${TARGET_REF} -- ${schema} 2>&1 >/dev/null; then
        current_name=$(yq '.metadata.name' ${schema})
        previous_name=$(git show ${TARGET_REF}:${schema} | yq '.metadata.name')

        if [ "${current_name}" == "${previous_name}" ]; then
            echo "${schema} has changed in comparison to '${TARGET_REF}', but object name is the same (${current_name} == ${previous_name})."
            echo "This is not valid as APIResourceSchemas are immutable."
            echo "Please run \`make crds\` without modifying APIResourceSchema files manually."
            exit 1
        fi
    fi
done
