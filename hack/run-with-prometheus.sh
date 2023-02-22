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

set -o nounset
set -o pipefail
set -o errexit

PROMETHEUS_VER=2.42.0
ARCH=$(go env GOARCH)
OS=$(go env GOOS)
if ! command -v hack/tools/prometheus 1> /dev/null 2>&1; then
  echo "Downloading Prometheus v${PROMETHEUS_VER}"
  mkdir -p hack/tools
  curl -L https://github.com/prometheus/prometheus/releases/download/v${PROMETHEUS_VER}/prometheus-${PROMETHEUS_VER}.${OS}-${ARCH}.tar.gz | tar -xz --strip-components 1 -C hack/tools prometheus-${PROMETHEUS_VER}.${OS}-${ARCH}/prometheus
fi

touch .prometheus-config.yaml
./hack/tools/prometheus --storage.tsdb.path=".prometheus_data" --config.file=.prometheus-config.yaml --web.enable-lifecycle &
PROM_PID=$!
export PROMETHEUS_URL="http://localhost:9090"
echo 'Waiting for Prometheus to be ready...'
while ! curl -s http://localhost:9090/-/ready; do
  sleep 1
done
echo 'Prometheus is ready!'

set -o xtrace
set +o errexit
"${@}"
EXIT_CODE=${?}
set +o xtrace
set -o errexit
echo "Command terminated with ${EXIT_CODE}"

kill -TERM ${PROM_PID}

if [ -n "${ARTIFACT_DIR:=}" ]; then
  echo 'Waiting for Prometheus to shut down...'
  while [ -f .prometheus_data/lock ]; do sleep 1; done
  echo 'Prometheus shut down successfully!'
  mkdir -p ${ARTIFACT_DIR}/metrics
  tar cvzf ${ARTIFACT_DIR}/metrics/prometheus.tar -C .prometheus_data .
fi

exit "${EXIT_CODE}"
