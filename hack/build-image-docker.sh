#!/usr/bin/env bash

# Copyright 2025 The kcp Authors.
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

# Build container images for kcp using Docker
#
# This script builds container images using Docker (with or without buildx).
#
# Usage examples:
#   # Build locally with default settings (uses current git commit hash)
#   ./hack/build-image-docker.sh
#
#   # Build locally with custom repository name
#   REPOSITORY=my-registry/kcp ./hack/build-image-docker.sh
#
#   # Build locally without pushing (dry run)
#   DRY_RUN=1 ./hack/build-image-docker.sh
#
#   # Build for specific architectures only
#   ARCHITECTURES="amd64" ./hack/build-image-docker.sh
#
# Environment variables:
#   REPOSITORY - Override default repository (default: ghcr.io/kcp-dev/kcp)
#   ARCHITECTURES - Space-separated list of architectures (default: "amd64 arm64")
#   DRY_RUN - Set to any value to build locally without pushing
#   KCP_GHCR_USERNAME/KCP_GHCR_PASSWORD - Registry credentials for pushing
#
# Build tool support:
#   - docker + buildx: Multi-arch support with intelligent platform handling
#   - docker only: Single architecture fallback

set -euo pipefail

# make git available
if ! [ -x "$(command -v git)" ]; then
  echo "Installing git ..."
  yum install -y git
fi

# Check if docker is available
if ! [ -x "$(command -v docker)" ]; then
  echo "Error: Docker is not available"
  exit 1
fi

echo "Using docker for container builds"
# Check if buildx is available for multi-arch builds
if docker buildx version >/dev/null 2>&1; then
  echo "Docker buildx is available for multi-arch builds"
  DOCKER_BUILDX=true
else
  echo "Docker buildx not available, falling back to single-arch builds"
  DOCKER_BUILDX=false
fi


if [ -z "${REPOSITORY:-}" ]; then
  echo "Error: REPOSITORY environment variable is required"
  exit 1
fi
repository=$REPOSITORY
architectures=${ARCHITECTURES:-"amd64 arm64"}

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

# Function to build images with docker buildx
build_with_docker_buildx() {
  echo "Building multi-arch image $image ..."

  # Create platforms string for buildx
  platforms=""
  for arch in $architectures; do
    if [ -n "$platforms" ]; then
      platforms="$platforms,linux/$arch"
    else
      platforms="linux/$arch"
    fi
  done

  # For push builds, use multi-platform; for local builds, build per arch
  if [ -z "${DRY_RUN:-}" ]; then
    # Building for push - use multi-platform with --push
    docker buildx build \
      --file Dockerfile \
      --tag "$image" \
      --platform "$platforms" \
      --build-arg "TARGETOS=linux" \
      --push \
      .
  else
    # For local/dry-run builds, build each architecture separately with --load
    for arch in $architectures; do
      fullTag="$image-$arch"
      echo "Building $fullTag ..."
      docker buildx build \
        --file Dockerfile \
        --tag "$fullTag" \
        --platform "linux/$arch" \
        --build-arg "TARGETOS=linux" \
        --build-arg "TARGETARCH=$arch" \
        --load \
        .
    done
    # Tag the first architecture as the main image for local use
    first_arch=$(echo $architectures | cut -d' ' -f1)
    docker tag "$image-$first_arch" "$image"
  fi
}

# Function to build images with regular docker (single arch only)
build_with_docker() {
  # Use only the first architecture for regular docker
  arch=$(echo $architectures | cut -d' ' -f1)
  fullTag="$image-$arch"

  echo "Building single-arch image $fullTag (docker without buildx) ..."
  docker build \
    --file Dockerfile \
    --tag "$fullTag" \
    --platform "linux/$arch" \
    --build-arg "TARGETOS=linux" \
    --build-arg "TARGETARCH=$arch" \
    .

  # Tag it as the main image too
  docker tag "$fullTag" "$image"
}

# Build images based on available docker features
if [ "$DOCKER_BUILDX" = true ]; then
  build_with_docker_buildx
else
  build_with_docker
fi

# Additionally to an image tagged with the Git tag, we also
# release images tagged with the current branch name, which
# is somewhere between a blanket "latest" tag and a specific
# tag.
if [ -n "$branchName" ] && [ -z "${PULL_NUMBER:-}" ]; then
  branchImage="$repository:$branchName"

  if [ "$DOCKER_BUILDX" = true ]; then
    echo "Tagging multi-arch image as $branchImage ..."
    docker tag "$image" "$branchImage"
  else
    echo "Tagging single-arch image as $branchImage ..."
    docker tag "$image" "$branchImage"
  fi
fi

# push images, except in dry runs
if [ -z "${DRY_RUN:-}" ]; then
  echo "Logging into GHCR ..."

  if [ "$DOCKER_BUILDX" = true ]; then
    # buildx with --push already pushed during build
    echo "Images already pushed during buildx build"
  else
    # Regular docker - need to login and push
    if [ -n "${GHCR_USERNAME:-}" ] && [ -n "${GHCR_PASSWORD:-}" ]; then
      echo "$GHCR_PASSWORD" | docker login ghcr.io -u "$GHCR_USERNAME" --password-stdin
    else
      echo "Skipping login (GHCR_USERNAME/GHCR_PASSWORD not provided)"
    fi

    echo "Pushing images ..."
    docker push "$image"

    if [ -n "${branchImage:-}" ]; then
      docker push "$branchImage"


    fi
  fi
else
  echo "Not pushing images because \$DRY_RUN is set."
fi

echo "Done."
