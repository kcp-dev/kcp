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

# Originally copied from
# https://github.com/kubernetes-sigs/controller-runtime/blob/479b723944e34ae42c9911fe01228ff34eb5ca81/hack/go-install.sh#L4

set -o errexit
set -o nounset
set -o pipefail

if [ -z "${1}" ]; then
  echo "must provide module as first parameter"
  exit 1
fi

if [ -z "${2}" ]; then
  echo "must provide binary name as second parameter"
  exit 1
fi

if [ -z "${3}" ]; then
  echo "must provide version as third parameter"
  exit 1
fi

if [ -z "${GOBIN}" ]; then
  echo "GOBIN is not set. Must set GOBIN to install the bin in a specified directory."
  exit 1
fi

rm -f "${GOBIN}/${2}"* || true

# install the golang module specified as the first argument
go install "${1}@${3}"
mv "${GOBIN}/${2}" "${GOBIN}/${2}-${3}"
ln -sf "${GOBIN}/${2}-${3}" "${GOBIN}/${2}"
