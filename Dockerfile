# syntax=docker/dockerfile:1.4

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

# Build the binary
FROM --platform=${BUILDPLATFORM} docker.io/golang:1.22.10 AS builder
WORKDIR /workspace

# Install dependencies.
RUN apt-get update && apt-get install -y jq && mkdir bin

# Run this with docker build --build-arg goproxy=$(go env GOPROXY) to override the goproxy
ARG goproxy=https://proxy.golang.org
ENV GOPROXY=$goproxy

# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
COPY sdk/go.mod sdk/go.mod
COPY sdk/go.sum sdk/go.sum
COPY cli/go.mod cli/go.mod
COPY cli/go.sum cli/go.sum
USER 0

# Install kubectl.
RUN wget "https://dl.k8s.io/release/$(go list -m -json k8s.io/kubernetes | jq -r .Version)/bin/linux/$(uname -m | sed 's/aarch.*/arm64/;s/armv8.*/arm64/;s/x86_64/amd64/')/kubectl" -O bin/kubectl && chmod +x bin/kubectl

# Cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy the sources
COPY ./ ./

ARG TARGETOS
ARG TARGETARCH

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    make OS=${TARGETOS} ARCH=${TARGETARCH}

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:debug

# Copy wget so we can do basic healthchecks in the final image.
COPY --from=builder /usr/bin/wget /usr/bin/wget

WORKDIR /
COPY --from=builder /etc/ssl/certs /etc/ssl/certs
COPY --from=builder workspace/bin/kcp-front-proxy workspace/bin/kcp workspace/bin/virtual-workspaces workspace/bin/cache-server /
COPY --from=builder workspace/bin/kubectl-* /usr/local/bin/
COPY --from=builder workspace/bin/kubectl /usr/local/bin/
ENV KUBECONFIG=/etc/kcp/config/admin.kubeconfig
# Use uid of nonroot user (65532) because kubernetes expects numeric user when applying pod security policies
RUN ["/busybox/sh", "-c", "mkdir -p /data && chown 65532:65532 /data"]
USER 65532:65532
WORKDIR /data
VOLUME /data
ENTRYPOINT ["/kcp"]
CMD ["start"]
