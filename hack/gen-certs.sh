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

SOURCE_PATH=$(readlink -f "${BASH_SOURCE[0]}")
SOURCE_DIR=$(dirname $SOURCE_PATH)
CERT_DIR=$(readlink -f "${SOURCE_DIR}/../certs")

mkdir -p $CERT_DIR
rm -f $CERT_DIR/*.pem

# Generate a private key and self-signed cert
make_ca () {
    local name=$1
    local org=${2:-KCP}
    openssl req -x509 -nodes -newkey rsa:2048 -days 365 -keyout "$CERT_DIR/${name}-key.pem" -out "$CERT_DIR/${name}-cert.pem" -subj "/CN=${name}/O=${org}"
}

# Generate keys and certs that are signed by a CA
make_signed_cert () {
    local name=$1
    local ca=$2
    local org=${3:-KCP}
    local type=${4:-server} # alternative is client

    # Generate the private key and csr
    openssl req -nodes -newkey rsa:2048 -keyout "$CERT_DIR/${name}-key.pem" -out "$CERT_DIR/${name}-req.pem" -subj "/CN=${name}/O=${org}"

    # Sign the request
    openssl x509 -req -in "$CERT_DIR/${name}-req.pem" -days 90 -CA "$CERT_DIR/${ca}-cert.pem" -CAkey "$CERT_DIR/${ca}-key.pem" -CAcreateserial -out "$CERT_DIR/${name}-cert.pem" -extfile "$SOURCE_DIR/${type}-ext.cnf"
}

make_ca "kcp-ca"
make_ca "kcp-proxy-ca"
make_ca "kcp-client-ca"

# kcp server identity
make_signed_cert "kcp-server" "kcp-ca"

# kcp virtual workspace server identity
make_signed_cert "kcp-vw-server" "kcp-ca"

# kcp proxy identity for its clients
make_signed_cert "proxy-server" "kcp-proxy-ca"

# kcp proxy's identity to kcp server and virtual workspace server
make_signed_cert "proxy-client" "kcp-ca" "KCP" "client"

# An admin identity to connect to KCP or the virtual workspace server through the proxy
make_signed_cert "admin" "kcp-client-ca" "my-admin" "client"

# Delete the requests
rm -f $CERT_DIR/*-req.pem
