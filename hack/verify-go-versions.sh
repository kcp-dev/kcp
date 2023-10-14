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

set -e
set -o pipefail

VERSION=$(grep "go 1." go.mod | sed 's/go //')

grep "FROM .* docker.io/golang:" Dockerfile | { ! grep -v "${VERSION}"; } || { echo "Wrong go version in Dockerfile, expected ${VERSION}"; exit 1; }
grep -w "go-version:" .github/workflows/*.yaml | { ! grep -v "go-version: v${VERSION}"; } || { echo "Wrong go version in .github/workflows/*.yaml, expected ${VERSION}"; exit 1; }

shopt -s dotglob
# Note CONTRIBUTING.md isn't copied in the Dockerfile
for f in docs/content/CONTRIBUTING.md; do
  grep "golang.org/doc/install" "$f" | { ! grep -v "${VERSION}"; } || { echo "Wrong go version in $f; expected ${VERSION}"; exit 1; }
done

# Check prow config
grep "ghcr.io/kcp-dev/infra/build" ".prow.yaml" | { ! grep -v "${VERSION}"; } || { echo "Wrong go version in .prow.yaml; expected ${VERSION}"; exit 1; }

if [ -z "${IGNORE_GO_VERSION}" ]; then
  go version | { ! grep -v go${VERSION}; } || { echo "Unexpected go version installed, expected ${VERSION}. Use IGNORE_GO_VERSION=1 to skip this check."; exit 1; }
fi
