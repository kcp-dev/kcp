all: build
.PHONY: all

build:
	go build -o bin ./cmd/...
.PHONY: build

e2e: #build
	@ ./tests/utils/run_e2e.sh
.PHONY: e2e

vendor:
	go mod tidy
	go mod vendor
.PHONY: vendor

codegen:
	./hack/update-codegen.sh
.PHONY: codegen
