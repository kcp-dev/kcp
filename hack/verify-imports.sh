#!/usr/bin/env bash

# Copyright 2022 The KCP Authors.
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

make -C "$( dirname "${BASH_SOURCE[0]}")/../" imports
if ! git diff --quiet --exit-code ; then
	cat << EOF
ERROR: This check enforces that import statements are ordered correctly.
ERROR: The import statements are out of order. Run the following command
ERROR: to regenerate the statements:
ERROR: $ make imports
ERROR: The following differences were found:
EOF
	git diff
	exit 1
fi