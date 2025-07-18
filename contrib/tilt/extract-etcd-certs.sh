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


# Add alpha-etcd and beta-etcd in /etc/hosts, then:
#
# etcdctl \
#   --endpoints alpha-etcd:30100 \
#   --cacert ./contrib/tilt/etcd-ca.crt \
#   --cert ./contrib/tilt/etcd-alpha-client.crt \
#   --key ./contrib/tilt/etcd-alpha-client.key \
#   endpoint health

cd "$(dirname "$0")"

kubectl --context kind-kcp -n kcp-certs get secret certs-etcd-peer-ca -o jsonpath='{.data.ca\.crt}' \
    | base64 -d > etcd-ca.crt

for name in alpha beta; do
    kubectl --context kind-kcp -n kcp-certs get secret "$name-etcd-client-cert" -o jsonpath='{.data.tls\.crt}' \
        | base64 -d > "etcd-$name-client.crt"
    kubectl --context kind-kcp -n kcp-certs get secret "$name-etcd-client-cert" -o jsonpath='{.data.tls\.key}' \
        | base64 -d > "etcd-$name-client.key"
done
