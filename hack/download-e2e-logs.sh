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

if ! command -v gsutil &> /dev/null; then
  echo "gsutil missing. Install the gcloud SDK first"
  exit 1
fi

EXAMPLE_URL=https://prow.ci.openshift.org/view/gs/origin-ci-test/pr-logs/pull/kcp-dev_kcp/2707/pull-ci-kcp-dev-kcp-main-e2e-sharded/1620846315476881408

if [[ -z "${URL:-}" ]]; then
  echo -e "URL is required\n\nExample URL: $EXAMPLE_URL\n"
  exit 1
fi

# Strip off the prefix
PR_ISH="${URL#https://prow.ci.openshift.org/view/gs/origin-ci-test/pr-logs/pull/kcp-dev_kcp/}"

# Split into fields
PR="$(echo $PR_ISH | cut -d / -f 1)"
JOB="$(echo $PR_ISH | cut -d / -f 2)"
E2E="$(echo $JOB | grep -o 'e2e.*')"
RUN="$(echo $PR_ISH | cut -d / -f 3)"

# Default to placing logs in prow/$PR/$JOB/$RUN/
OUT=${OUT:-prow/$PR/$JOB/$RUN/}
mkdir -p $OUT

gsutil -m cp -r gs://origin-ci-test/pr-logs/pull/kcp-dev_kcp/$PR/$JOB/$RUN/artifacts/$E2E/$E2E/artifacts/kcp/ $OUT
