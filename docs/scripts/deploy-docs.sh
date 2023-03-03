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

set -o errexit
set -o nounset
set -o pipefail
set -o xtrace

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$REPO_ROOT/docs"

if [[ "${GITHUB_EVENT_NAME:-}" == "pull_request" ]]; then
  # For PRs, we don't want to use GITHUB_REF_NAME, which will be something like merge/1234; instead, we want to use
  # the branch the PR is targeting, such as main or release-0.11
  VERSION=$GITHUB_BASE_REF
else
  if [[ -n "${GITHUB_REF_NAME:-}" ]]; then
    VERSION="${VERSION:-$GITHUB_REF_NAME}"
  else
    VERSION=${VERSION:-$(git rev-parse --abbrev-ref HEAD)}
  fi

  if echo "$VERSION" | grep '^release-[0-9]'; then
    VERSION=v$(echo "$VERSION" | cut -d - -f 2)
  elif echo "$VERSION" | grep '^v[0-9]\+\.[0-9]\+'; then
    VERSION=$(echo "$VERSION" | grep -o '^v[0-9]\+\.[0-9]\+')
  fi
fi

MIKE_OPTIONS=()

if [[ -n "${REMOTE:-}" ]]; then
  MIKE_OPTIONS+=(--remote "$REMOTE")
fi

if [[ -n "${BRANCH:-}" ]]; then
  MIKE_OPTIONS+=(--branch "$BRANCH")
fi

if [[ -n "${CI:-}" ]]; then
  if [[ "${GITHUB_EVENT_NAME:-}" == "push" ]]; then
    # Only push to gh-pages if we're in GitHub Actions (CI is set) and we have a non-PR event.
    MIKE_OPTIONS+=(--push)
  fi

  # Always set git user info in CI because even if we're not pushing, we need it
  git config user.name kcp-docs-bot
  git config user.email no-reply@kcp.io
fi

mike deploy "${MIKE_OPTIONS[@]}" "$VERSION"
