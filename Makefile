all: build
.PHONY: all

build:
	go build -ldflags "-X k8s.io/client-go/pkg/version.gitVersion=$$(git describe --abbrev=8 --dirty --always)" -o bin/kcp ./cmd/kcp
	go build -ldflags "-X k8s.io/client-go/pkg/version.gitVersion=$$(git describe --abbrev=8 --dirty --always)" -o bin/syncer ./cmd/syncer
.PHONY: build

vendor:
	go mod tidy
	go mod vendor
.PHONY: vendor
