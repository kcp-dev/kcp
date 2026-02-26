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

if uname | grep -q "Darwin"; then
    # On MacOS docker containers are still linux, so the binaries need
    # to be built for linux.
    export GOOS=linux
fi

echo "Once the setup is done and kcp is ready the file 'tilt.kubeconfig' will be created in the repository root to access the kcp instance as an admin."

echo "URLs:"
echo "  front-proxy:    https://kcp.localhost:8443"
echo "  root shard:     https://root.kcp.localhost:8443"
echo "  theseus shard:  https://theseus.kcp.localhost:8443"
echo "  grafana:        http://localhost:3333/"
echo "  prometheus:     http://localhost:9091"

echo "Starting tilt..."
tilt up -f ./contrib/tilt/Tiltfile
