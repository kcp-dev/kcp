#!/usr/bin/env bash

# Copyright 2025 The KCP Authors.
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

set -o nounset
set -o pipefail
set -o errexit

apt update
apt install -y bzip2

# propagate prow artifact directory
export ARTIFACT_DIR="$ARTIFACTS"

set -o xtrace
set +o errexit
"${@}"
EXIT_CODE=${?}
set +o xtrace
set -o errexit
echo "Command terminated with ${EXIT_CODE}"

echo 'Compressing build artifacts...'
cd "$ARTIFACTS"
tar cjf artifacts.tar.bz2 --remove-files *
echo 'Done compressing files.'

exit "${EXIT_CODE}"
