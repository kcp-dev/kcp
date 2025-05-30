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

set -euo pipefail

cd $(dirname $0)/..

mkdir -p hack/tools
cd hack/tools

URL="$1"
BINARY="$2"
VERSION="$3"
BINARY_PATTERN="${4:-**/$BINARY}"
GO_MODULE=${GO_MODULE:-false}
UNCOMPRESSED=${UNCOMPRESSED:-false}

# Check if and what version we installed already.
versionFile="$BINARY.version"
existingVersion=""
if [ -f "$versionFile" ]; then
  existingVersion="$(cat "$versionFile")"
fi

# If the binary exists and its version matches, we're good.
if [ -f "$BINARY" ] && [ "$VERSION" == "$existingVersion" ]; then
  exit 0
fi

(
  rm -rf tmp
  mkdir -p tmp
  cd tmp

  echo "Downloading $BINARY version $VERSION …" >&2

  if $GO_MODULE; then
    GOBIN=$(realpath .) go install "$URL@$VERSION"
    mv * "../$BINARY"
  else
    curl --fail --silent -LO "$URL"
    archive="$(ls)"

    if ! $UNCOMPRESSED; then
      case "$archive" in
        *.tar.gz | *.tgz)
          tar xzf "$archive"
          ;;
        *.zip)
          unzip "$archive"
          ;;
        *)
          echo "Unknown file type: $archive" >&2
          exit 1
      esac
    fi

    mv $BINARY_PATTERN ../$BINARY
    chmod +x ../$BINARY
  fi
)

rm -rf tmp
echo "$VERSION" > "$versionFile"

echo "Installed at _tools/$BINARY." >&2
