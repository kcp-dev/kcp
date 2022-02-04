# Build the manager binary
FROM registry.access.redhat.com/ubi8/go-toolset:1.16.12-4 as builder

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
USER 0
RUN go mod download

COPY Makefile Makefile

# Copy the go source
COPY config/ config/
COPY pkg/ pkg/
COPY cmd/ cmd/
COPY third_party/ third_party/

RUN mkdir bin; CGO_ENABLED=0 go build -o bin ./cmd/...

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
# FROM gcr.io/distroless/static:nonroot
FROM alpine:3.15
WORKDIR /
COPY --from=builder workspace/bin/* /
USER 65532:65532
