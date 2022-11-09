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

# Detect the path used for the install target
ifeq (,$(shell go env GOBIN))
INSTALL_GOBIN=$(shell go env GOPATH)/bin
else
INSTALL_GOBIN=$(shell go env GOBIN)
endif

CONTROLLER_GEN_VER := v0.10.0
CONTROLLER_GEN_BIN := controller-gen
CONTROLLER_GEN := $(TOOLS_DIR)/$(CONTROLLER_GEN_BIN)-$(CONTROLLER_GEN_VER)
export CONTROLLER_GEN # so hack scripts can use it

YAML_PATCH_VER ?= v0.0.11
YAML_PATCH_BIN := yaml-patch
YAML_PATCH := $(TOOLS_DIR)/$(YAML_PATCH_BIN)-$(YAML_PATCH_VER)
export YAML_PATCH # so hack scripts can use it

OPENSHIFT_GOIMPORTS_VER := c72f1dc2e3aacfa00aece3391d938c9bc734e791
OPENSHIFT_GOIMPORTS_BIN := openshift-goimports
OPENSHIFT_GOIMPORTS := $(TOOLS_DIR)/$(OPENSHIFT_GOIMPORTS_BIN)-$(OPENSHIFT_GOIMPORTS_VER)
export OPENSHIFT_GOIMPORTS # so hack scripts can use it

GOLANGCI_LINT_VER := v1.49.0
GOLANGCI_LINT_BIN := golangci-lint
GOLANGCI_LINT := $(TOOLS_GOBIN_DIR)/$(GOLANGCI_LINT_BIN)-$(GOLANGCI_LINT_VER)

STATICCHECK_VER := 2022.1
STATICCHECK_BIN := staticcheck
STATICCHECK := $(TOOLS_GOBIN_DIR)/$(STATICCHECK_BIN)-$(STATICCHECK_VER)

GOTESTSUM_VER := v1.8.1
GOTESTSUM_BIN := gotestsum
GOTESTSUM := $(abspath $(TOOLS_DIR))/$(GOTESTSUM_BIN)-$(GOTESTSUM_VER)

LOGCHECK_VER := v0.2.0
LOGCHECK_BIN := logcheck
LOGCHECK := $(TOOLS_GOBIN_DIR)/$(LOGCHECK_BIN)-$(LOGCHECK_VER)
export LOGCHECK # so hack scripts can use it

CODE_GENERATOR_VER := 2dc1248118a7f2337c6374ff5778c0880e1a4226
CODE_GENERATOR_BIN := code-generator
CODE_GENERATOR := $(TOOLS_GOBIN_DIR)/$(CODE_GENERATOR_BIN)-$(CODE_GENERATOR_VER)
export CODE_GENERATOR # so hack scripts can use it

ARCH := $(shell go env GOARCH)
OS := $(shell go env GOOS)

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

ldflags:
	@echo $(LDFLAGS)

.PHONY: require-%
require-%:
	@if ! command -v $* 1> /dev/null 2>&1; then echo "$* not found in \$$PATH"; exit 1; fi

build: WHAT ?= ./cmd/... ./tmc/cmd/...
build: require-jq require-go require-git verify-go-versions ## Build the project
	GOOS=$(OS) GOARCH=$(ARCH) go build $(BUILDFLAGS) -ldflags="$(LDFLAGS)" -o bin $(WHAT)
.PHONY: build

.PHONY: build-all
build-all:
	GOOS=$(OS) GOARCH=$(ARCH) $(MAKE) build WHAT='./cmd/...  ./tmc/cmd/...'

.PHONY: build-kind-images
build-kind-images-ko: require-ko
	$(eval SYNCER_IMAGE=$(shell KO_DOCKER_REPO=kind.local ko build --platform=linux/$(ARCH) ./cmd/syncer))
	$(eval TEST_IMAGE=$(shell KO_DOCKER_REPO=kind.local ko build --platform=linux/$(ARCH) ./test/e2e/fixtures/kcp-test-image))
build-kind-images: build-kind-images-ko
	test -n "$(SYNCER_IMAGE)" || (echo Failed to create syncer image; exit 1)
	test -n "$(TEST_IMAGE)" || (echo Failed to create test image; exit 1)

install: WHAT ?= ./cmd/...
install:
	GOOS=$(OS) GOARCH=$(ARCH) go install -ldflags="$(LDFLAGS)" $(WHAT)
	ln -sf $(INSTALL_GOBIN)/kubectl-workspace $(INSTALL_GOBIN)/kubectl-ws
	ln -sf $(INSTALL_GOBIN)/kubectl-workspace $(INSTALL_GOBIN)/kubectl-workspaces
.PHONY: install

$(GOLANGCI_LINT):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) github.com/golangci/golangci-lint/cmd/golangci-lint $(GOLANGCI_LINT_BIN) $(GOLANGCI_LINT_VER)

$(STATICCHECK):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) honnef.co/go/tools/cmd/staticcheck $(STATICCHECK_BIN) $(STATICCHECK_VER)

$(LOGCHECK):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) sigs.k8s.io/logtools/logcheck $(LOGCHECK_BIN) $(LOGCHECK_VER)

$(CODE_GENERATOR):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) github.com/kcp-dev/code-generator $(CODE_GENERATOR_BIN) $(CODE_GENERATOR_VER)

lint: $(GOLANGCI_LINT) $(STATICCHECK) $(LOGCHECK)
	$(GOLANGCI_LINT) run --timeout=10m ./...
	$(STATICCHECK) -checks ST1019,ST1005 ./...
	./hack/verify-contextual-logging.sh
.PHONY: lint

update-contextual-logging: $(LOGCHECK)
	UPDATE=true ./hack/verify-contextual-logging.sh
.PHONY: update-contextual-logging

generate-docs:
	go run hack/generate/cli-doc/gen-cli-doc.go
	./hack/generate/crd-ref/run-crd-ref-gen.sh

vendor: ## Vendor the dependencies
	go mod tidy
	go mod vendor
.PHONY: vendor

tools: $(GOLANGCI_LINT) $(CONTROLLER_GEN) $(YAML_PATCH) $(GOTESTSUM) $(OPENSHIFT_GOIMPORTS) $(CODE_GENERATOR)
.PHONY: tools

$(CONTROLLER_GEN):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) sigs.k8s.io/controller-tools/cmd/controller-gen $(CONTROLLER_GEN_BIN) $(CONTROLLER_GEN_VER)

$(YAML_PATCH):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) github.com/pivotal-cf/yaml-patch/cmd/yaml-patch $(YAML_PATCH_BIN) $(YAML_PATCH_VER)

$(GOTESTSUM):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) gotest.tools/gotestsum $(GOTESTSUM_BIN) $(GOTESTSUM_VER)

crds: $(CONTROLLER_GEN) $(YAML_PATCH)
	./hack/update-codegen-crds.sh
.PHONY: crds

codegen: crds $(CODE_GENERATOR)
	go mod download
	./hack/update-codegen-clients.sh
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
imports: $(OPENSHIFT_GOIMPORTS) verify-go-versions
	$(OPENSHIFT_GOIMPORTS) -m github.com/kcp-dev/kcp

$(TOOLS_DIR)/verify_boilerplate.py:
	mkdir -p $(TOOLS_DIR)
	curl --fail --retry 3 -L -o $(TOOLS_DIR)/verify_boilerplate.py https://raw.githubusercontent.com/kubernetes/repo-infra/master/hack/verify_boilerplate.py
	chmod +x $(TOOLS_DIR)/verify_boilerplate.py

.PHONY: verify-boilerplate
verify-boilerplate: $(TOOLS_DIR)/verify_boilerplate.py
	$(TOOLS_DIR)/verify_boilerplate.py --boilerplate-dir=hack/boilerplate --skip docs

ifdef ARTIFACT_DIR
GOTESTSUM_ARGS += --junitfile=$(ARTIFACT_DIR)/junit.xml
endif

GO_TEST = go test
ifdef USE_GOTESTSUM
GO_TEST = $(GOTESTSUM) $(GOTESTSUM_ARGS) --
endif

COUNT ?=
COUNT_ARG =
ifdef COUNT
COUNT_ARG = -count $(COUNT)
endif
E2E_PARALLELISM ?=
ifdef E2E_PARALLELISM
PARALLELISM_ARG = -p $(E2E_PARALLELISM) -parallel $(E2E_PARALLELISM)
endif
SUITES ?=
SUITES_ARG =
COMPLETE_SUITES_ARG =
ifdef SUITES
SUITES_ARG = --suites $(SUITES)
COMPLETE_SUITES_ARG = -args $(SUITES_ARG)
endif


.PHONY: test-e2e
ifdef USE_GOTESTSUM
test-e2e: $(GOTESTSUM)
endif
test-e2e: TEST_ARGS ?=
test-e2e: WHAT ?= ./test/e2e...
test-e2e: build-all
	UNSAFE_E2E_HACK_DISABLE_ETCD_FSYNC=true NO_GORUN=1 GOOS=$(OS) GOARCH=$(ARCH) $(GO_TEST) -race $(COUNT_ARG) $(PARALLELISM_ARG) $(WHAT) $(TEST_ARGS) $(COMPLETE_SUITES_ARG)

.PHONY: test-e2e-shared
ifdef USE_GOTESTSUM
test-e2e-shared: $(GOTESTSUM)
endif
test-e2e-shared: TEST_ARGS ?=
test-e2e-shared: WHAT ?= ./test/e2e...
test-e2e-shared: WORK_DIR ?= .
ifdef ARTIFACT_DIR
test-e2e-shared: LOG_DIR ?= $(ARTIFACT_DIR)/kcp
else
test-e2e-shared: LOG_DIR ?= $(WORK_DIR)/.kcp
endif
test-e2e-shared: require-kind build-all build-kind-images
	mkdir -p "$(LOG_DIR)" "$(WORK_DIR)/.kcp"
	kind get kubeconfig > "$(WORK_DIR)/.kcp/kind.kubeconfig"
	rm -f "$(WORK_DIR)/.kcp/admin.kubeconfig"
	UNSAFE_E2E_HACK_DISABLE_ETCD_FSYNC=true NO_GORUN=1 ./bin/test-server --log-file-path="$(LOG_DIR)/kcp.log" $(TEST_SERVER_ARGS) 2>&1 & PID=$$! && echo "PID $$PID" && \
	trap 'kill -TERM $$PID' TERM INT EXIT && \
	while [ ! -f "$(WORK_DIR)/.kcp/admin.kubeconfig" ]; do sleep 1; done && \
	NO_GORUN=1 GOOS=$(OS) GOARCH=$(ARCH) $(GO_TEST) -race $(COUNT_ARG) $(PARALLELISM_ARG) $(WHAT) $(TEST_ARGS) \
		-args --use-default-kcp-server --syncer-image="$(SYNCER_IMAGE)" --kcp-test-image="$(TEST_IMAGE)" --pcluster-kubeconfig="$(abspath $(WORK_DIR)/.kcp/kind.kubeconfig)" $(SUITES_ARG) \
	$(if $(value WAIT),|| { echo "Terminated with $$?"; wait "$$PID"; },)

.PHONY: test-e2e-shared-minimal
ifdef USE_GOTESTSUM
test-e2e-shared-minimal: $(GOTESTSUM)
endif
test-e2e-shared-minimal: TEST_ARGS ?=
test-e2e-shared-minimal: WHAT ?= ./test/e2e...
test-e2e-shared-minimal: WORK_DIR ?= .
ifdef ARTIFACT_DIR
test-e2e-shared-minimal: LOG_DIR ?= $(ARTIFACT_DIR)/kcp
else
test-e2e-shared-minimal: LOG_DIR ?= $(WORK_DIR)/.kcp
endif
test-e2e-shared-minimal: build-all
	mkdir -p "$(LOG_DIR)" "$(WORK_DIR)/.kcp"
	rm -f "$(WORK_DIR)/.kcp/admin.kubeconfig"
	UNSAFE_E2E_HACK_DISABLE_ETCD_FSYNC=true NO_GORUN=1 ./bin/test-server --log-file-path="$(LOG_DIR)/kcp.log" $(TEST_SERVER_ARGS) 2>&1 & PID=$$! && echo "PID $$PID" && \
	trap 'kill -TERM $$PID' TERM INT EXIT && \
	while [ ! -f "$(WORK_DIR)/.kcp/admin.kubeconfig" ]; do sleep 1; done && \
	NO_GORUN=1 GOOS=$(OS) GOARCH=$(ARCH) $(GO_TEST) -race $(COUNT_ARG) $(PARALLELISM_ARG) $(WHAT) $(TEST_ARGS) \
		-args --use-default-kcp-server $(SUITES_ARG) \
	$(if $(value WAIT),|| { echo "Terminated with $$?"; wait "$$PID"; },)

.PHONY: test-e2e-sharded
ifdef USE_GOTESTSUM
test-e2e-sharded: $(GOTESTSUM)
endif
test-e2e-sharded: TEST_ARGS ?=
test-e2e-sharded: WHAT ?= ./test/e2e...
test-e2e-sharded: WORK_DIR ?= .
ifdef ARTIFACT_DIR
test-e2e-sharded: LOG_DIR ?= $(ARTIFACT_DIR)/kcp
else
test-e2e-sharded: LOG_DIR ?= $(WORK_DIR)/.kcp
endif
test-e2e-sharded: require-kind build-all build-kind-images
	mkdir -p "$(LOG_DIR)" "$(WORK_DIR)/.kcp"
	kind get kubeconfig > "$(WORK_DIR)/.kcp/kind.kubeconfig"
	rm -f "$(WORK_DIR)/.kcp/admin.kubeconfig"
	UNSAFE_E2E_HACK_DISABLE_ETCD_FSYNC=true NO_GORUN=1 ./bin/sharded-test-server --v=2 --log-dir-path="$(LOG_DIR)" --work-dir-path="$(WORK_DIR)" $(TEST_SERVER_ARGS) --number-of-shards=2 2>&1 & PID=$$!; echo "PID $$PID" && \
	trap 'kill -TERM $$PID' TERM INT EXIT && \
	while [ ! -f "$(WORK_DIR)/.kcp/admin.kubeconfig" ]; do sleep 1; done && \
	NO_GORUN=1 GOOS=$(OS) GOARCH=$(ARCH) $(GO_TEST) -race $(COUNT_ARG) $(PARALLELISM_ARG) $(WHAT) $(TEST_ARGS) \
		-args --use-default-kcp-server --root-shard-kubeconfig=$(PWD)/.kcp-0/admin.kubeconfig $(SUITES_ARG) \
		--syncer-image="$(SYNCER_IMAGE)" --kcp-test-image="$(TEST_IMAGE)" --pcluster-kubeconfig="$(abspath $(WORK_DIR)/.kcp/kind.kubeconfig)" \
	$(if $(value WAIT),|| { echo "Terminated with $$?"; wait "$$PID"; },)

.PHONY: test-e2e-sharded-minimal
ifdef USE_GOTESTSUM
test-e2e-sharded-minimal: $(GOTESTSUM)
endif
test-e2e-sharded-minimal: TEST_ARGS ?=
test-e2e-sharded-minimal: WHAT ?= ./test/e2e...
test-e2e-sharded-minimal: WORK_DIR ?= .
ifdef ARTIFACT_DIR
test-e2e-sharded-minimal: LOG_DIR ?= $(ARTIFACT_DIR)/kcp
else
test-e2e-sharded-minimal: LOG_DIR ?= $(WORK_DIR)/.kcp
endif
test-e2e-sharded-minimal: build-all
	mkdir -p "$(LOG_DIR)" "$(WORK_DIR)/.kcp"
	rm -f "$(WORK_DIR)/.kcp/admin.kubeconfig"
	UNSAFE_E2E_HACK_DISABLE_ETCD_FSYNC=true NO_GORUN=1 ./bin/sharded-test-server --v=2 --log-dir-path="$(LOG_DIR)" --work-dir-path="$(WORK_DIR)" $(TEST_SERVER_ARGS) --number-of-shards=2 2>&1 & PID=$$!; echo "PID $$PID" && \
	trap 'kill -TERM $$PID' TERM INT EXIT && \
	while [ ! -f "$(WORK_DIR)/.kcp/admin.kubeconfig" ]; do sleep 1; done && \
	NO_GORUN=1 GOOS=$(OS) GOARCH=$(ARCH) $(GO_TEST) -race $(COUNT_ARG) $(PARALLELISM_ARG) $(WHAT) $(TEST_ARGS) \
		-args --use-default-kcp-server --root-shard-kubeconfig=$(PWD)/.kcp-0/admin.kubeconfig $(SUITES_ARGS) \
	$(if $(value WAIT),|| { echo "Terminated with $$?"; wait "$$PID"; },)

.PHONY: test
ifdef USE_GOTESTSUM
test: $(GOTESTSUM)
endif
test: WHAT ?= ./...
# We will need to move into the sub package, of pkg/apis to run those tests.
test:
	$(GO_TEST) -race $(COUNT_ARG) -coverprofile=coverage.txt -covermode=atomic $(TEST_ARGS) $$(go list "$(WHAT)" | grep -v ./test/e2e/)
	cd pkg/apis && $(GO_TEST) -race $(COUNT_ARG) -coverprofile=coverage.txt -covermode=atomic $(TEST_ARGS) $(WHAT)

.PHONY: verify-k8s-deps
verify-k8s-deps:
	hack/validate-k8s.sh

.PHONY: verify-imports
verify-imports:
	hack/verify-imports.sh

.PHONY: verify-go-versions
verify-go-versions:
	hack/verify-go-versions.sh

.PHONY: modules
modules: ## Run go mod tidy to ensure modules are up to date
	go mod tidy
	cd pkg/apis; go mod tidy

.PHONY: verify-modules
verify-modules: modules  ## Verify go modules are up to date
	@if !(git diff --quiet HEAD -- go.sum go.mod pkg/apis/go.mod pkg/apis/go.sum); then \
		git diff; \
		echo "go module files are out of date"; exit 1; \
	fi

.PHONY: help
help: ## Show this help.
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'
