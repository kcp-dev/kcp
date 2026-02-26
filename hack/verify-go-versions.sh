#!/usr/bin/env bash

# Copyright 2022 The kcp Authors.
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

gomod_version() {
    awk '/^go / { print $2 }' "$1" | sed 's/.0$//'
}

minimum_go_version="$(gomod_version go.mod)"
build_go_version="$(awk '/go-build-version/ { print $3 }' go.mod)"
errors=0

echo "Verifying minimum Go version: $minimum_go_version"

for gomod in $(git ls-files '**/go.mod'); do
    if [[ "$(gomod_version $gomod)" != "$minimum_go_version" ]]; then
        echo "  Wrong go version in $gomod, expected $minimum_go_version"
        errors=$((errors + 1))
    fi
done

if ! grep "golang.org/doc/install" $(git ls-files 'docs/**/*.md') | grep -q "$minimum_go_version"; then
    echo "  Wrong go version in docs/content/contributing/getting-started.md; expected $minimum_go_version"
fi

echo "Verifying build Go version: $build_go_version"

for dockerfile in $(git ls-files Dockerfile '**/Dockerfile'); do
    for version in $(sed -nE -e '/^FROM .*golang:/ s#^.*:([1-9.-rc]+).*$#\1#p' "$dockerfile"); do
        if [[ "$version" != "$build_go_version" ]]; then
            echo "  Wrong go version in $dockerfile, expected $build_go_version, found $version"
            errors=$((errors + 1))
        fi
    done
done

for workflow in $(git ls-files '.github/workflows/*.yaml' '.github/workflows/*.yml'); do
    if grep -q 'go-version-file:' "$workflow"; then
        echo "  Workflow $workflow uses go-version-file, should use go-version instead"
        errors=$((errors + 1))
    fi

    for version in $(sed -nE -e '/go-version:/ s#^.*v([1-9.-rc]+)$#\1#p' "$workflow"); do
        if [[ "$version" != "$build_go_version" ]]; then
            echo "  Wrong go version in $workflow, expected v${build_go_version}, found v${version}"
            errors=$((errors + 1))
        fi
    done
done

for prow_image_version in $(sed -nE -e '/kcp-dev\/infra/ s#^.*:([1-9.-rc]+)-[1-9]+.*$#\1#p' .prow.yaml); do
    if [[ "$prow_image_version" != "$build_go_version" ]]; then
        echo "  Wrong go version in .prow.yaml, expected ${build_go_version}, found ${prow_image_version}"
        errors=$((errors + 1))
    fi
done

if [[ "$CI" == true ]] && ! go version | grep -q "go${build_go_version}"; then
    echo "  Wrong go version detected, expected ${build_go_version}"
    errors=$((errors + 1))
fi

exit $errors
