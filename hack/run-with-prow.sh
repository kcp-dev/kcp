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
set +o errexit
tar cjf artifacts.tar.bz2 --remove-files *
TAR_EXIT_CODE=${?}
set -o errexit
if [[ "${TAR_EXIT_CODE}" -ne 0 ]]; then
	# Processes started by the test run (e.g. kcp servers) can still be
	# shutting down and writing to their log/audit files when we get here,
	# which makes tar report a non-fatal "file changed as we read it"
	# warning (or a failed rmdir of the directory it lives in) as a fatal
	# error. Don't let that mask the actual test result.
	echo "warning: compressing artifacts exited with code ${TAR_EXIT_CODE}, some files may still be present uncompressed in ${ARTIFACTS}"
fi
echo 'Done compressing files.'

exit "${EXIT_CODE}"
