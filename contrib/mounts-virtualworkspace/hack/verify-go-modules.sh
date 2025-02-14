#!/usr/bin/env bash

# Copyright 2023 The KCP Authors.
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

# This script verifies all go modules in the repository.

set -o errexit
set -o nounset
set -o pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

mapfile -t DIRS < <(find "${REPO_ROOT}" -name go.mod -print0 | xargs -0 dirname)

for dir in "${DIRS[@]}"; do
  (
    cd "$dir"
    echo "Verifying ${dir}"
    if ! git diff --quiet HEAD -- go.mod go.sum; then
      echo "go module files are out of date"
      git diff HEAD -- go.mod go.sum
      exit 1
    fi
  )
done
