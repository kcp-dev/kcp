#!/usr/bin/env bash

# Copyright 2023 The KCP Authors.
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

# This script verifies all go modules in the repository.
# go.mod and go.sum files are checked that both have been
# committed. Versions of dependencies they declare are
# checked to be in line with dependencies in k8s.io/kubernetes,
# and a warning message is printed if they differ in v<Maj>.<Min>.

set -o errexit
set -o nounset
set -o pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

if [[ ! -f "${REPO_ROOT}/go.work" ]]; then
  (
    cd "${REPO_ROOT}"
    go work init
    go work use -r .
  )
fi

# Array of all modules inside the repository, sorted in
# descending topological order (dependant comes before its dependee).
mapfile -t MODULE_DIRS < <(
  for mod in $(tsort <(
    # Lines in format "<Dependant> <Dependee>" are fed into `tsort` to perform the topological sort.
    for dir in $(go list -m -json | jq -r '.Dir'); do
      (
        cd "${dir}"
        # Prints the dependency graph where we extract only the direct
        # github.com/kcp-dev/kcp/* dependencies of the currently examined module,
        # and we discard the dependency version (the line is in format
        # "<This module> <Its dependency>@<Version>", so we split by '@' and get the
        # first part). We skip when no such deps are found.
        go mod graph \
          | grep -E "^$(go mod edit -json | jq -r '.Module.Path') github.com/kcp-dev/kcp/" \
          | cut -d'@' -f1 \
          || true
      )
    done
  ) | tac); do # We need to reverse the lines (with `tac`) to have a descending order.
    # We have sorted module paths. We need to convert them into their respective directory locations.
    go list -m -json "$mod" | jq -r '.Dir'
  done
)

# list_deps lists dependencies of the supplied go.mod file (dependencies
# with version "v0.0.0" are skipped). The output is a json dictionary in the
# format `{"<Dependency>": "<Version>", ...}`.
function list_deps {
  gomod_file="${1}"
  go mod edit -json "${gomod_file}" \
    | jq -r '
        (
          [ .Require[]
            | select(.Version != "v0.0.0")
            | { (.Path): .Version } ]
          | add
        )
      '
}

# diff_version_deps lists common dependencies of the two supplied go.mod files
# whose respective versions differ (up to v<Maj>.<Min>). The output is a json
# dictionary in the format:
# `{"<Dependency>": {
#    "has": "<Dependency version of the first file>",
#    "wants": "<Dependency version of the second file>"
#  }, ...}`
function diff_version_deps {
  has_deps="${1}"
  wants_deps="${2}"
  jq -s '
    map(to_entries)    # Convert both input dicts into two separate arrays.
      | add            # Concatenate those two arrays into a single one.
      | group_by(.key) # Group items by `.key`.
      | map(           # Map each selected item into { "<Dep>": {"has": "<Version0>", "wants": "<Version1>"} }.
          select(
              # If grouping resulted in two items, it means both arrays have this dependency.
              length == 2
              # And the dependency has a version mismatch.
              and (.[0].value  != .[1].value)
            )
            | { (.[0].key): { "has": .[0].value, "wants": .[1].value } }
        )
      | add // empty
  ' "${has_deps}" "${wants_deps}"
}

# print_diff_version_deps prints the output of diff_version_deps (expected in stdin)
# as a human-readable multi-line text.
# Returns 1 if any lines were printed (i.e. errors were found).
function print_diff_version_deps {
  jq -r '
    to_entries
      | map("Version mismatch: \(.key) is \(.value.has), but \(.value.wants) expected")
      | join("\n")
    '
}

# Compares versions of dependencies in the supplied go.mod to
# makes sure they are in line with the ones declared in
# k8s.io/kubernetes module.
function compare_mod_versions_with_k8s_deps {
  gomod_file="${1}"
  deps="$(list_deps ${gomod_file})"
  diff_version_deps <(echo "${deps}") <(echo "${k8s_deps}")
}

function gomod_filepath_for {
  mod="${1}"
  go list -m -json "${mod}" | jq -r .GoMod
}

k8s_gomod="$(gomod_filepath_for k8s.io/kubernetes)"
k8s_deps="$(list_deps ${k8s_gomod})"

for dir in "${MODULE_DIRS[@]}"; do
  (
    cd "$dir"
    echo "Verifying ${dir}"

    if ! go mod tidy -diff ; then
      echo "Error: go.mod and/or go.sum is not clean, run 'go mod tidy' before continuing" 1>&2
      exit 1
    fi

    gomod_file="${dir}/go.mod"
    echo "Verifying dependency versions in ${gomod_file} against ${k8s_gomod}"

    diff="$(compare_mod_versions_with_k8s_deps ${dir}/go.mod)"
    if [[ -n "${diff}" ]]; then
      echo "${diff}" | print_diff_version_deps 1>&2
      echo "${diff}" | grep -q "k8s.io/" && {
        echo "Error: dependencies from '*k8s.io/*' must be in line with ${k8s_gomod}" 1>&2
      } || true
      echo "Continuing because no fatal errors found"
    fi
  )
done
