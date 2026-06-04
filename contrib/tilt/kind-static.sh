#!/usr/bin/env bash

# Copyright 2021 The kcp Authors.
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

# Static variant of kind.sh: brings up a kind cluster and runs Tilt against
# Tiltfile.static, which installs the upstream kcp-operator and deploys the
# upstream kcp images instead of building them from local sources.

set -e

if ! command -v kind &> /dev/null; then
  echo "kind not found, exiting"
  exit 1
fi

if ! command -v helm &> /dev/null; then
  echo "helm not found, exiting"
  exit 1
fi

if ! command -v tilt &> /dev/null; then
  echo "tilt not found, exiting"
  echo "please follow the instructions at https://github.com/tilt-dev/tilt#install-tilt"
  exit 1
fi

CLUSTER_NAME=kcp-tilt

if ! kind get clusters | grep -w -q "$CLUSTER_NAME"; then
    kind create cluster --name "$CLUSTER_NAME"
fi

kind export kubeconfig --name "$CLUSTER_NAME"

echo "Once the setup is done and kcp is ready, the files 'tilt-frontproxy.kubeconfig',"
echo "'tilt-root.kubeconfig' and 'tilt-theseus.kubeconfig' will be created in the"
echo "repository root to access the kcp instance as an admin."
echo

# The kcp hostnames must resolve to 127.0.0.1 so the Tilt port-forward (:8443)
# is reachable. The '.localhost' TLD is not auto-resolved on macOS, so check
# that the entries exist in /etc/hosts and tell the user how to add them.
KCP_HOSTS="kcp.localhost root.kcp.localhost theseus.kcp.localhost"
missing_hosts=""
for h in $KCP_HOSTS; do
    if ! grep -Eq "(^|[[:space:]])${h//./\\.}([[:space:]]|\$)" /etc/hosts 2>/dev/null; then
        missing_hosts="$missing_hosts $h"
    fi
done

if [ -n "$missing_hosts" ]; then
    echo "WARNING: the following kcp hostnames do not resolve via /etc/hosts:"
    echo "  $missing_hosts"
    echo "Add the following line to /etc/hosts (e.g. 'sudo vi /etc/hosts'), otherwise"
    echo "kubectl will fail with 'no such host':"
    echo
    echo "  127.0.0.1 $KCP_HOSTS"
    echo
else
    echo "/etc/hosts entries for kcp hostnames: OK"
    echo
fi

echo "URLs:"
echo "  front-proxy:    https://kcp.localhost:8443"
echo "  root shard:     https://root.kcp.localhost:8443"
echo "  theseus shard:  https://theseus.kcp.localhost:8443"
echo "  grafana:        http://localhost:3333/"
echo "  prometheus:     http://localhost:9091"

echo "Starting tilt (static, upstream images)..."
tilt up -f ./contrib/tilt/Tiltfile.static
