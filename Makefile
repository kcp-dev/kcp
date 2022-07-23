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
SHELL := /usr/bin/env bash -e

GO_INSTALL = ./hack/go-install.sh

TOOLS_DIR=hack/tools
TOOLS_GOBIN_DIR := $(abspath $(TOOLS_DIR))
GOBIN_DIR=$(abspath ./bin )
PATH := $(GOBIN_DIR):$(TOOLS_GOBIN_DIR):$(PATH)
TMPDIR := $(shell mktemp -d)

CONTROLLER_GEN_VER := v0.7.0
CONTROLLER_GEN_BIN := controller-gen
CONTROLLER_GEN := $(TOOLS_DIR)/$(CONTROLLER_GEN_BIN)-$(CONTROLLER_GEN_VER)
export CONTROLLER_GEN # so hack scripts can use it

YAML_PATCH_VER ?= v0.0.11
YAML_PATCH_BIN := yaml-patch
YAML_PATCH := $(TOOLS_DIR)/$(YAML_PATCH_BIN)-$(YAML_PATCH_VER)
export YAML_PATCH # so hack scripts can use it

OPENSHIFT_GOIMPORTS_VER := e3ded13788f820b9e4c79b44f7f8c63861100ac6
OPENSHIFT_GOIMPORTS_BIN := openshift-goimports
OPENSHIFT_GOIMPORTS := $(TOOLS_DIR)/$(OPENSHIFT_GOIMPORTS_BIN)-$(OPENSHIFT_GOIMPORTS_VER)
export OPENSHIFT_GOIMPORTS # so hack scripts can use it

GOLANGCI_LINT_VER := v1.44.2
GOLANGCI_LINT_BIN := golangci-lint
GOLANGCI_LINT := $(TOOLS_GOBIN_DIR)/$(GOLANGCI_LINT_BIN)-$(GOLANGCI_LINT_VER)

GOTESTSUM_VER := v1.8.1
GOTESTSUM_BIN := gotestsum
GOTESTSUM := $(abspath $(TOOLS_DIR))/$(GOTESTSUM_BIN)-$(GOTESTSUM_VER)

ARCH := $(subst 64,,$(shell uname -p | sed s/x86_/amd/))64

KUBE_MAJOR_VERSION := $(shell go mod edit -json | jq '.Require[] | select(.Path == "k8s.io/kubernetes") | .Version' --raw-output | sed 's/v\([0-9]*\).*/\1/')
KUBE_MINOR_VERSION := $(shell go mod edit -json | jq '.Require[] | select(.Path == "k8s.io/kubernetes") | .Version' --raw-output | sed "s/v[0-9]*\.\([0-9]*\).*/\1/")
GIT_COMMIT := $(shell git rev-parse --short HEAD || echo 'local')
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

.PHONY: require-%
require-%:
	@if ! command -v $* 1> /dev/null 2>&1; then echo "$* not found in \$$PATH"; exit 1; fi

pre-build-checks:
ifeq ($(and $(KUBE_MAJOR_VERSION),$(KUBE_MINOR_VERSION)),)
	$(info Kubernetes version not set. Ensure jq is installed.)
	exit 1
endif
.PHONY: pre-build-checks

build: WHAT ?= ./cmd/...
build: pre-build-checks ## Build the project
	go build $(BUILDFLAGS) -ldflags="$(LDFLAGS)" -o bin $(WHAT)
.PHONY: build

.PHONY: build-all
build-all:
	@$(MAKE) build WHAT=./cmd/...

.PHONY: build-kind-images
build-kind-images-ko: require-ko
	$(eval SYNCER_IMAGE=$(shell KO_DOCKER_REPO=kind.local ko build --platform=linux/$(ARCH) ./cmd/syncer))
	$(eval TEST_IMAGE=$(shell KO_DOCKER_REPO=kind.local ko build --platform=linux/$(ARCH) ./test/e2e/fixtures/kcp-test-image))
build-kind-images: build-kind-images-ko
	test -n "$(SYNCER_IMAGE)" || (echo Failed to create syncer image; exit 1)
	test -n "$(TEST_IMAGE)" || (echo Failed to create test image; exit 1)

install: WHAT ?= ./cmd/...
install:
	go install -ldflags="$(LDFLAGS)" $(WHAT)
.PHONY: install

$(GOLANGCI_LINT):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) github.com/golangci/golangci-lint/cmd/golangci-lint $(GOLANGCI_LINT_BIN) $(GOLANGCI_LINT_VER)

lint: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) run --timeout=10m ./...
.PHONY: lint

vendor: ## Vendor the dependencies
	go mod tidy
	go mod vendor
.PHONY: vendor

tools: $(GOLANGCI_LINT) $(CONTROLLER_GEN) $(YAML_PATCH) $(GOTESTSUM) $(OPENSHIFT_GOIMPORTS)
.PHONY: tools

$(CONTROLLER_GEN):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) sigs.k8s.io/controller-tools/cmd/controller-gen $(CONTROLLER_GEN_BIN) $(CONTROLLER_GEN_VER)

$(YAML_PATCH):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) github.com/pivotal-cf/yaml-patch/cmd/yaml-patch $(YAML_PATCH_BIN) $(YAML_PATCH_VER)

$(GOTESTSUM):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) gotest.tools/gotestsum $(GOTESTSUM_BIN) $(GOTESTSUM_VER)

codegen: $(CONTROLLER_GEN) $(YAML_PATCH) ## Run the codegenerators
	go mod download
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

$(OPENSHIFT_GOIMPORTS):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) github.com/openshift-eng/openshift-goimports $(OPENSHIFT_GOIMPORTS_BIN) $(OPENSHIFT_GOIMPORTS_VER)

.PHONY: imports
imports: $(OPENSHIFT_GOIMPORTS)
	$(OPENSHIFT_GOIMPORTS) -m github.com/kcp-dev/kcp

$(TOOLS_DIR)/verify_boilerplate.py:
	mkdir -p $(TOOLS_DIR)
	curl --fail --retry 3 -L -o $(TOOLS_DIR)/verify_boilerplate.py https://raw.githubusercontent.com/kubernetes/repo-infra/master/hack/verify_boilerplate.py
	chmod +x $(TOOLS_DIR)/verify_boilerplate.py

.PHONY: verify-boilerplate
verify-boilerplate: $(TOOLS_DIR)/verify_boilerplate.py
	$(TOOLS_DIR)/verify_boilerplate.py --boilerplate-dir=hack/boilerplate

ifdef ARTIFACT_DIR
GOTESTSUM_ARGS += --junitfile=$(ARTIFACT_DIR)/junit.xml
endif

GO_TEST = go test
ifdef USE_GOTESTSUM
GO_TEST = $(GOTESTSUM) $(GOTESTSUM_ARGS) --
endif

COUNT ?= 1
E2E_PARALLELISM ?= 1

.PHONY: test-e2e
ifdef USE_GOTESTSUM
test-e2e: $(GOTESTSUM)
endif
test-e2e: TEST_ARGS ?=
test-e2e: WHAT ?= ./test/e2e...
test-e2e: build-all
	NO_GORUN=1 $(GO_TEST) -race -count $(COUNT) -p $(E2E_PARALLELISM) -parallel $(E2E_PARALLELISM) $(WHAT) $(TEST_ARGS)

.PHONY: test-e2e-shared
ifdef USE_GOTESTSUM
test-e2e-shared: $(GOTESTSUM)
endif
test-e2e-shared: TEST_ARGS ?=
test-e2e-shared: WHAT ?= ./test/e2e...
ifdef ARTIFACT_DIR
test-e2e-shared: LOG_DIR ?= $(ARTIFACT_DIR)/kcp
else
test-e2e-shared: LOG_DIR ?= .kcp
endif
test-e2e-shared: build-all
	kind get kubeconfig > "$(PWD)/kind.kubeconfig"
	mkdir -p $(LOG_DIR)
	NO_GORUN=1 ./bin/test-server --log-file-path="$(LOG_DIR)/kcp.log" $(TEST_SERVER_ARGS) 2>&1 & PID=$$! && echo "PID $$PID" && \
	trap 'kill -TERM $$PID' TERM INT EXIT && \
	while [ ! -f .kcp/admin.kubeconfig ]; do sleep 1; done && \
	NO_GORUN=1 $(GO_TEST) -race -count $(COUNT) -p $(E2E_PARALLELISM) -parallel $(E2E_PARALLELISM) $(WHAT) $(TEST_ARGS) \
		-args --use-default-kcp-server --syncer-image="$(SYNCER_IMAGE)" --kcp-test-image="$(TEST_IMAGE)" --pcluster-kubeconfig="$(PWD)/kind.kubeconfig"

.PHONY: test-e2e-sharded
ifdef USE_GOTESTSUM
test-e2e-sharded: $(GOTESTSUM)
endif
test-e2e-sharded: TEST_ARGS ?=
test-e2e-sharded: WHAT ?= ./test/e2e...
ifdef ARTIFACT_DIR
test-e2e-sharded: LOG_DIR ?= $(ARTIFACT_DIR)/kcp
else
test-e2e-sharded: LOG_DIR ?= .kcp
endif
test-e2e-sharded: require-kind require-ko build-all
	kind get kubeconfig > "$(PWD)/kind.kubeconfig"
	mkdir -p $(LOG_DIR)
	NO_GORUN=1 ./bin/sharded-test-server --v=2 --log-dir-path="$(LOG_DIR)" $(TEST_SERVER_ARGS) 2>&1 & PID=$$!; echo "PID $$PID" && \
	trap 'kill -TERM $$PID' TERM INT EXIT && \
	while [ ! -f .kcp/admin.kubeconfig ]; do sleep 1; done && \
	NO_GORUN=1 $(GO_TEST) -race -count $(COUNT) -p $(E2E_PARALLELISM) -parallel $(E2E_PARALLELISM) $(WHAT) $(TEST_ARGS) \
		-args --use-default-kcp-server --root-shard-kubeconfig=$(PWD)/.kcp-0/admin.kubeconfig --syncer-image="$(SYNCER_IMAGE)" --kcp-test-image="$(TEST_IMAGE)" --pcluster-kubeconfig="$(PWD)/kind.kubeconfig"

.PHONY: test
ifdef USE_GOTESTSUM
test: $(GOTESTSUM)
endif
test: WHAT ?= ./...
# We will need to move into the sub package, of pkg/apis to run those tests.
test:
	$(GO_TEST) -race -count $(COUNT) -coverprofile=coverage.txt -covermode=atomic $(TEST_ARGS) $$(go list "$(WHAT)" | grep -v ./test/e2e/)
	cd pkg/apis && $(GO_TEST) -race -count $(COUNT) -coverprofile=coverage.txt -covermode=atomic $(TEST_ARGS) $(WHAT)

.PHONY: verify-k8s-deps
verify-k8s-deps:
	hack/validate-k8s.sh

.PHONY: verify-imports
verify-imports:
	hack/verify-imports.sh

.PHONY: help
help: ## Show this help.
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'
