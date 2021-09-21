all: build
.PHONY: all

build:
	go build -o bin ./cmd/...
.PHONY: build

kuttl: #build
	@ ./tests/kuttl/run_kuttl.sh
.PHONY: e2e

vendor:
	go mod tidy
	go mod vendor
.PHONY: vendor

codegen:
	./hack/update-codegen.sh
.PHONY: codegen
