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
FROM golang:1.19 AS builder

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
COPY pkg/apis/go.mod pkg/apis/go.mod
COPY pkg/apis/go.sum pkg/apis/go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
USER 0
RUN go mod download

COPY Makefile Makefile

# Copy the go source
COPY config/ config/
COPY pkg/ pkg/
COPY cmd/ cmd/
COPY tmc/ tmc/
COPY third_party/ third_party/
COPY hack/ hack/
COPY .git/ .git/
# To make sure hack/verify-go-versions.sh succeeds
COPY .ci-operator.yaml .
COPY Dockerfile .
COPY .github/ .github/

RUN apt-get update && apt-get install -y jq && mkdir bin
RUN CGO_ENABLED=0 make

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
# FROM gcr.io/distroless/static:nonroot
FROM alpine:3.15
WORKDIR /
COPY --from=builder workspace/bin/kcp-front-proxy workspace/bin/kcp workspace/bin/virtual-workspaces /
COPY --from=builder workspace/bin/kubectl-* /usr/local/bin/
RUN ln -s /usr/local/bin/kubectl-workspace /usr/local/bin/kubectl-workspaces && ln -s /usr/local/bin/kubectl-workspace /usr/local/bin/kubectl-ws
RUN apk add --update curl && rm -rf /var/cache/apk/*
RUN curl -fL -o /usr/local/bin/kubectl "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/$(uname -m | sed 's/aarch.*/arm64/;s/armv8.*/arm64/;s/x86_64/amd64/')/kubectl" && \
  chmod +x /usr/local/bin/kubectl
ENV KUBECONFIG=/etc/kcp/config/admin.kubeconfig
RUN mkdir -p /data && \
    chown 65532:65532 /data
USER 65532:65532
WORKDIR /data
VOLUME /data
ENTRYPOINT ["/kcp"]
CMD ["start"]
