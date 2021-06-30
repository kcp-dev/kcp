all: build
.PHONY: all

build:
	go build -o bin ./cmd/...
.PHONY: build

vendor:
	go mod tidy
	go mod vendor
.PHONY: vendor

codegen:
	./hack/update-codegen.sh
.PHONY: codegen
