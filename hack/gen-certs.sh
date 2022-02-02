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

# Generate CA private key and self-signed cert
openssl req -x509 -nodes -newkey rsa:4096 -days 365 -keyout $CERT_DIR/ca-key.pem -out $CERT_DIR/ca-cert.pem -subj "/O=KCP CA"

# Generate the server's private key and csr
openssl req -nodes -newkey rsa:4096 -keyout $CERT_DIR/server-key.pem -out $CERT_DIR/server-req.pem -subj "/O=KCP Server"

# Sign the request. Subject Alt Names are in hack/server-ext.cnf
openssl x509 -req -in $CERT_DIR/server-req.pem -days 90 -CA $CERT_DIR/ca-cert.pem -CAkey $CERT_DIR/ca-key.pem -CAcreateserial -out $CERT_DIR/server-cert.pem -extfile $SOURCE_DIR/server-ext.cnf