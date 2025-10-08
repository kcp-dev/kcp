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

set -o errexit
set -o nounset
set -o pipefail
set -o xtrace

# This script populates our third_party directory, along with any other files we're
# wholesale copying from the upstream k8s.io/client-go repository. All files retain
# their original author's copyright.

source_dir="$( go list -m -json k8s.io/client-go | jq --raw-output .Dir )"

update_copy() {
    local upstream_file="$1"
    if [[ ! -f "$upstream_file" ]]; then
        echo "Upstream file $upstream_file does not exist, skipping"
        return
    fi
    local local_file="$2"
    shift 2
    # TODO could look into making this a go command and then use the AST
    # to make intelligent transformations.
    # E.g. the types are somewhat deterministic from `fakeX` to `scopedX`
    # On the other hand that is a lot of work to save a few seconds once
    # every few months.
    sed \
        -e '/Copyright .* The Kubernetes Authors./a \
Modifications Copyright YEAR The KCP Authors.' \
        "$@" \
        "$upstream_file" > "$local_file"
}


update_third_party() {
    for third_party in $(find third_party/k8s.io/client-go -type f); do
        update_copy \
            "$source_dir/${third_party##third_party/k8s.io/client-go/}" \
            "$third_party" \
            -e 's#"k8s.io/client-go/testing"#kcptesting "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing"#' \
            -e 's# testing\.#kcptesting.#' \
            -e 's#*testing\.#*kcptesting.#'

    done
}

update_expansion() {
    local upstream_file="$1"
    shift 1
    if [[ ! -f "$upstream_file" ]]; then
        echo "Upstream file $upstream_file does not exist, skipping"
        return
    fi
    local local_equivalent="${upstream_file##$source_dir/}"
    update_copy "$upstream_file" "$local_equivalent" "$@"
}

update_expansions() {
    for expansion in $(find "${source_dir}/listers" -type f -name '*_expansion.go'); do
        update_expansion "$expansion"
    done
    for expansion in $(find "${source_dir}/kubernetes" -type f -name 'fake_*_expansion.go'); do
        update_expansion "$expansion" \
            -e 's#"k8s.io/client-go/testing"#"github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing"#'
    done
}

update_third_party
update_expansions
