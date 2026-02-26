#!/usr/bin/env bash

# Copyright 2023 The kcp Authors.
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

mapfile -t DIRS < <(find "${REPO_ROOT}" -name go.mod -print0 | xargs -0 dirname)

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
    # Extracts v<Maj>.<Min> from a semantic version string.
    def major_minor_semver: capture("v(?<major>\\d+)\\.(?<minor>\\d+)")
      | "v" + .major + "." + .minor;

    map(to_entries)    # Convert both input dicts into two separate arrays.
      | add            # Concatenate those two arrays into a single one.
      | group_by(.key) # Group items by `.key`.
      | map(           # Map each selected item into { "<Dep>": {"has": "<Version0>", "wants": "<Version1>"} }.
          select(
              # If grouping resulted in two items, it means both arrays have this dependency.
              length == 2
              # Compare the v<Maj>.<Min> of both items.
              and (.[0].value | major_minor_semver) != (.[1].value | major_minor_semver)
            )
            | { (.[0].key): { "has": .[0].value, "wants": .[1].value } }
        )
      | add // empty
  ' "${has_deps}" "${wants_deps}"
}

# print_diff_version_deps prints the output of diff_version_deps as a
# human-readable multi-line text.
function print_diff_version_deps {
  jq -r "to_entries | map(\"Warning: version mismatch: has \(.key)@\(.value.has), but \(.value.wants) expected\") | join(\"\\n\")"
}

# Compares versions of dependencies in the supplied go.mod to
# makes sure they are in line with the ones declared in
# k8s.io/kubernetes module and prints the result.
function compare_mod_versions {
  gomod_file="${1}"
  echo "Verifying dependency versions in ${gomod_file} against ${k8s_gomod}"
  deps="$(list_deps ${gomod_file})"

  diff_version_deps <(echo "${deps}") <(echo "${k8s_deps}") \
    | print_diff_version_deps "${gomod_file}"
}

function gomod_filepath_for {
  mod="${1}"
  go list -m -json "${mod}" | jq -r .GoMod
}

k8s_gomod="$(gomod_filepath_for k8s.io/kubernetes)"
k8s_deps="$(list_deps ${k8s_gomod})"

for dir in "${DIRS[@]}"; do
  (
    cd "$dir"
    echo "Verifying ${dir}"

    if ! git diff --quiet HEAD -- go.mod go.sum; then
      git --no-pager diff HEAD -- go.mod go.sum
      echo "Error: go.mod and/or go.sum in ${dir} files have been modified, inspect and commit them before continuing" 1>&2
      exit 1
    fi

    compare_mod_versions "${dir}/go.mod"
  )
done

compare_mod_versions "$(gomod_filepath_for github.com/kcp-dev/client-go)"
compare_mod_versions "$(gomod_filepath_for github.com/kcp-dev/apimachinery/v2)"
