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

set -euo pipefail

# make git available
if ! [ -x "$(command -v git)" ]; then
  echo "Installing git ..."
  yum install -y git
fi

# in CI, make use of the registry mirror to avoid getting rate limited
if [ -n "${DOCKER_REGISTRY_MIRROR_ADDR:-}" ]; then
  # remove "http://" or "https://" prefix
  mirror="$(echo "$DOCKER_REGISTRY_MIRROR_ADDR" | awk -F// '{print $NF}')"

  echo "Configuring registry mirror for docker.io ..."

  cat <<EOF > /etc/containers/registries.conf.d/mirror.conf
[[registry]]
prefix = "docker.io"
insecure = true
location = "$mirror"
EOF
fi

repository=ghcr.io/kcp-dev/kcp
architectures="amd64 arm64"

# when building locally, just tag with the current HEAD hash.
version="$(git rev-parse --short HEAD)"
branchName=""

# deduce the tag from the Prow job metadata
if [ -n "${PULL_BASE_REF:-}" ]; then
  version="$(git tag --list "$PULL_BASE_REF")"

  if [ -z "$version" ]; then
    # if the base ref did not point to a tag, it's a branch name
    version="$(git rev-parse --short "$PULL_BASE_REF")"
    branchName="$PULL_BASE_REF"
  else
    # If PULL_BASE_REF is a tag, there is no branch available locally, plus
    # there is no guarantee that vX.Y.Z is tagged _only_ in the release-X.Y
    # branch; because of this we have to deduce the branch name from the tag
    branchName="$(echo "$version" | sed -E 's/^v([0-9]+)\.([0-9]+)\..*/release-\1.\2/')"
  fi
fi

# Prefix with "pr-" if not on a tag or branch
if [ -n "${PULL_NUMBER:-}" ]; then
  version="pr-$PULL_NUMBER-$version"
  repository="$repository-prs"
fi

image="$repository:$version"
echo "Building container image $image ..."

# build image for all architectures
for arch in $architectures; do
  fullTag="$image-$arch"

  echo "Building $fullTag ..."
  buildah build-using-dockerfile \
    --file Dockerfile \
    --tag "$fullTag" \
    --arch "$arch" \
    --override-arch "$arch" \
    --build-arg "TARGETOS=linux" \
    --build-arg "TARGETARCH=$arch" \
    --format=docker \
    .
done

echo "Creating manifest $image ..."
buildah manifest create "$image"
for arch in $architectures; do
  buildah manifest add "$image" "$image-$arch"
done

# Additionally to an image tagged with the Git tag, we also
# release images tagged with the current branch name, which
# is somewhere between a blanket "latest" tag and a specific
# tag.
if [ -n "$branchName" ] && [ -z "${PULL_NUMBER:-}" ]; then
  branchImage="$repository:$branchName"

  echo "Creating manifest $branchImage ..."
  buildah manifest create "$branchImage"
  for arch in $architectures; do
    buildah manifest add "$branchImage" "$image-$arch"
  done
fi

# push manifest, except in presubmits
if [ -z "${DRY_RUN:-}" ]; then
  echo "Logging into GHCR ..."
  buildah login --username "$KCP_GHCR_USERNAME" --password "$KCP_GHCR_PASSWORD" ghcr.io

  echo "Pushing manifest and images ..."
  buildah manifest push --all "$image" "docker://$image"

  if [ -n "${branchImage:-}" ]; then
    buildah manifest push --all "$branchImage" "docker://$branchImage"
  fi
else
  echo "Not pushing images because \$DRY_RUN is set."
fi

echo "Done."
