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

set -o nounset
set -o pipefail
set -o errexit

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")"/.. && pwd)
LOG_FILE="${REPO_ROOT}/hack/logcheck.out"
work_file="$(mktemp)"
LOGCHECK=${LOGCHECK:-logcheck}
LOGCHECK_ARGS="-check-contextual -check-key=false"

cd "$REPO_ROOT"

set +o errexit
${LOGCHECK} ${LOGCHECK_ARGS} ./... > "${work_file}" 2>&1
set -o errexit

# sdk is a separate module, so check that in addition to our root packages
echo "Checking "${REPO_ROOT}"/sdk"
cd "${REPO_ROOT}"/sdk
set +o errexit
${LOGCHECK} ${LOGCHECK_ARGS} ./... >> "${work_file}" 2>&1
set -o errexit

is_gnu_sed() { sed --version >/dev/null 2>&1; }
if is_gnu_sed; then
  SED="sed -i"
else
  SED="sed -i ''"
fi

# Normalize paths so we don't generate diffs based only on user directory mismatches
${SED} -e "s,${REPO_ROOT},,g" "${work_file}"
LC_COLLATE=C sort "${work_file}" -o "${work_file}"

# Remove errors that are not useful for us
mv "${work_file}" "${work_file}.full"
if ! grep -vE 'Additional arguments to|Key positional arguments|InitFlags' "${work_file}.full" > "${work_file}"; then
  echo "[INFO] Congratulations! No errors found."
fi

# Copy the current set to the known set, but keep temp file in place for diffing
if [[ "${UPDATE:-}" == "true" ]]; then
    cp "${work_file}" "${LOG_FILE}"
fi

# diff in-memory versions that have line/column numbers deleted to provide more stable results
# use plain sed here since we don't actually want to replace the files.
work_cleaned="$( sed -e 's/[0-9]*//g' "${work_file}" )"
log_cleaned="$( sed -e 's/[0-9]*//g' "${LOG_FILE}" )"

if ! changes="$(diff <(echo "${log_cleaned}") <(echo "${work_cleaned}"))"; then
    echo "[ERROR] Current logging errors and saved logging errors do not match."
    echo "${changes}"
    echo
    echo "[INFO] If you need to update the saved list, run \`make update-contextual-logging\` and commit \`hack/logcheck.out\`'"
    exit 1
fi
