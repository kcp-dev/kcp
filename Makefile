# Copyright 2021 The KCP Authors.
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

# We need bash for some conditional logic below.
SHELL := /usr/bin/env bash

GO_INSTALL = ./hack/go-install.sh

TOOLS_DIR=hack/tools
GOBIN_DIR := $(abspath $(TOOLS_DIR))
TMPDIR := $(shell mktemp -d)

CONTROLLER_GEN := go run sigs.k8s.io/controller-tools/cmd/controller-gen
export CONTROLLER_GEN # so hack scripts can use it

YAML_PATCH := go run github.com/pivotal-cf/yaml-patch/cmd/yaml-patch
export YAML_PATCH # so hack scripts can use it

OPENSHIFT_GOIMPORTS := go run github.com/openshift-eng/openshift-goimports
export OPENSHIFT_GOIMPORTS # so hack scripts can use it

GOLANGCI_LINT_VER := v1.44.2
GOLANGCI_LINT_BIN := golangci-lint
GOLANGCI_LINT := $(GOBIN_DIR)/$(GOLANGCI_LINT_BIN)-$(GOLANGCI_LINT_VER)

KUBE_MAJOR_VERSION := $(shell go mod edit -json | jq '.Require[] | select(.Path == "k8s.io/kubernetes") | .Version' --raw-output | sed 's/v\([0-9]*\).*/\1/')
KUBE_MINOR_VERSION := $(shell go mod edit -json | jq '.Require[] | select(.Path == "k8s.io/kubernetes") | .Version' --raw-output | sed "s/v[0-9]*\.\([0-9]*\).*/\1/")
GIT_COMMIT := $(shell git rev-parse --short HEAD)
GIT_DIRTY := $(shell git diff --quiet && echo 'clean' || echo 'dirty')
GIT_VERSION := $(shell go mod edit -json | jq '.Require[] | select(.Path == "k8s.io/kubernetes") | .Version' --raw-output)+kcp-$(shell git describe --tags --match='v*' --abbrev=14 "$(GIT_COMMIT)^{commit}" 2>/dev/null || echo v0.0.0-$(GIT_COMMIT))
BUILD_DATE := $(shell date -u +'%Y-%m-%dT%H:%M:%SZ')
LDFLAGS := \
	-X k8s.io/client-go/pkg/version.gitCommit=${GIT_COMMIT} \
	-X k8s.io/client-go/pkg/version.gitTreeState=${GIT_DIRTY} \
	-X k8s.io/client-go/pkg/version.gitVersion=${GIT_VERSION} \
	-X k8s.io/client-go/pkg/version.gitMajor=${KUBE_MAJOR_VERSION} \
	-X k8s.io/client-go/pkg/version.gitMinor=${KUBE_MINOR_VERSION} \
	-X k8s.io/client-go/pkg/version.buildDate=${BUILD_DATE} \
	\
	-X k8s.io/component-base/version.gitCommit=${GIT_COMMIT} \
	-X k8s.io/component-base/version.gitTreeState=${GIT_DIRTY} \
	-X k8s.io/component-base/version.gitVersion=${GIT_VERSION} \
	-X k8s.io/component-base/version.gitMajor=${KUBE_MAJOR_VERSION} \
	-X k8s.io/component-base/version.gitMinor=${KUBE_MINOR_VERSION} \
	-X k8s.io/component-base/version.buildDate=${BUILD_DATE}
all: build
.PHONY: all

build: WHAT ?= ./cmd/...
build: ## Build the project
	go build -ldflags="$(LDFLAGS)" -o bin $(WHAT)
.PHONY: build

install: WHAT ?= ./cmd/...
install:
	go install -ldflags="$(LDFLAGS)" $(WHAT)
.PHONY: install

$(GOLANGCI_LINT):
	GOBIN=$(GOBIN_DIR) $(GO_INSTALL) github.com/golangci/golangci-lint/cmd/golangci-lint $(GOLANGCI_LINT_BIN) $(GOLANGCI_LINT_VER)

lint: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) run --timeout=10m ./...
.PHONY: lint

vendor: ## Vendor the dependencies
	go mod tidy
	go mod vendor
.PHONY: vendor

codegen: ## Run the codegenerators
	./hack/update-codegen.sh
	$(MAKE) imports
.PHONY: codegen

# Note, running this locally if you have any modified files, even those that are not generated,
# will result in an error. This target is mostly for CI jobs.
.PHONY: verify-codegen
verify-codegen:
	if [[ -n "${GITHUB_WORKSPACE}" ]]; then \
		mkdir -p $$(go env GOPATH)/src/github.com/kcp-dev; \
		ln -s ${GITHUB_WORKSPACE} $$(go env GOPATH)/src/github.com/kcp-dev/kcp; \
	fi

	$(MAKE) codegen

	if ! git diff --quiet HEAD; then \
		git diff; \
		echo "You need to run 'make codegen' to update generated files and commit them"; \
		exit 1; \
	fi

.PHONY: imports
imports:
	$(OPENSHIFT_GOIMPORTS) -m github.com/kcp-dev/kcp

COUNT ?= 1
E2E_PARALLELISM ?= 1

.PHONY: test-e2e
test-e2e: WHAT ?= ./test/e2e...
test-e2e: build
	NO_GORUN=1 go test -race -count $(COUNT) -p $(E2E_PARALLELISM) -parallel $(E2E_PARALLELISM) $(WHAT) $(TEST_ARGS)

.PHONY: test
test: WHAT ?= ./...
test:
	go test -race -count $(COUNT) -coverprofile=coverage.txt -covermode=atomic $(TEST_ARGS) $$(go list "$(WHAT)" | grep -v ./test/e2e/)

.PHONY: demos
demos: build ## Runs all the default demos (kubecon and apiNegotiation).
	cd contrib/demo && ./runDemoScripts.sh

.PHONY: demo-apinegotiation
demo-apinegotiation: build ## Run the API Negotiation demo.
	cd contrib/demo && ./runDemoScripts.sh apiNegotiation

.PHONY: demo-kubecon
demo-kubecon: build ## Run the KubeCon demo.
	cd contrib/demo && ./runDemoScripts.sh kubecon

.PHONY: demo-ingress
demo-ingress: build ## Run the Ingress demo.
	cd contrib/demo && ./runDemoScripts.sh ingress

.PHONY: demo-prototype2
demo-prototype2: build ## Run the Prototype2 demo.
	cd contrib/demo && ./runDemoScripts.sh prototype2

.PHONY: help
help: ## Show this help.
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'
