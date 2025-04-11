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

#-----------------------------------------------------------------------------
# Workaround git issues on OpenShift Prow CI, where the user running in the
# job is not guaranteed to own the repo checkout.
#-----------------------------------------------------------------------------
ifeq ($(CI),true)
   $(shell git config --global --add safe.directory '*')
endif

GO_INSTALL = ./hack/go-install.sh

TOOLS_DIR=hack/tools
ROOT_DIR=$(abspath .)
TOOLS_GOBIN_DIR := $(abspath $(TOOLS_DIR))
GOBIN_DIR=$(abspath ./bin)
PATH := $(GOBIN_DIR):$(TOOLS_GOBIN_DIR):$(PATH)
TMPDIR := $(shell mktemp -d)
KIND_CLUSTER_NAME ?= kind

# Detect the path used for the install target
ifeq (,$(shell go env GOBIN))
INSTALL_GOBIN=$(shell go env GOPATH)/bin
else
INSTALL_GOBIN=$(shell go env GOBIN)
endif

CONTROLLER_GEN_VER := v0.16.5
CONTROLLER_GEN_BIN := controller-gen
CONTROLLER_GEN := $(TOOLS_DIR)/$(CONTROLLER_GEN_BIN)-$(CONTROLLER_GEN_VER)
export CONTROLLER_GEN # so hack scripts can use it

YAML_PATCH_VER ?= v0.0.11
YAML_PATCH_BIN := yaml-patch
YAML_PATCH := $(TOOLS_DIR)/$(YAML_PATCH_BIN)-$(YAML_PATCH_VER)
export YAML_PATCH # so hack scripts can use it

GOLANGCI_LINT_VER := v1.62.2
GOLANGCI_LINT_BIN := golangci-lint
GOLANGCI_LINT := $(TOOLS_GOBIN_DIR)/$(GOLANGCI_LINT_BIN)-$(GOLANGCI_LINT_VER)

HTTEST_VER := v0.3.2
HTTEST_BIN := httest
HTTEST := $(TOOLS_GOBIN_DIR)/$(HTTEST_BIN)-$(HTTEST_VER)

GOTESTSUM_VER := v1.8.1
GOTESTSUM_BIN := gotestsum
GOTESTSUM := $(abspath $(TOOLS_DIR))/$(GOTESTSUM_BIN)-$(GOTESTSUM_VER)

LOGCHECK_VER := v0.8.2
LOGCHECK_BIN := logcheck
LOGCHECK := $(TOOLS_GOBIN_DIR)/$(LOGCHECK_BIN)-$(LOGCHECK_VER)
export LOGCHECK # so hack scripts can use it

CODE_GENERATOR_VER := f78b79a2f905778087cbd266cda10d47997f0bdf
CODE_GENERATOR_BIN := code-generator
CODE_GENERATOR := $(TOOLS_GOBIN_DIR)/$(CODE_GENERATOR_BIN)-$(CODE_GENERATOR_VER)
export CODE_GENERATOR # so hack scripts can use it

KCP_APIGEN_BIN := apigen
KCP_APIGEN_GEN := $(TOOLS_DIR)/$(KCP_APIGEN_BIN)
export KCP_APIGEN_GEN # so hack scripts can use it

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
	-X k8s.io/component-base/version.buildDate=${BUILD_DATE} \
	-extldflags '-static'
all: build
.PHONY: all

ldflags:
	@echo $(LDFLAGS)

.PHONY: require-%
require-%:
	@if ! command -v $* 1> /dev/null 2>&1; then echo "$* not found in ${PATH}"; exit 1; fi

build: WHAT ?= ./cmd/... ./cli/cmd/... ./sdk/cmd/...
build: require-jq require-go require-git verify-go-versions ## Build the project
	set -x; for W in $(WHAT); do \
		pushd . && cd $${W%..}; \
    	GOOS=$(OS) GOARCH=$(ARCH) CGO_ENABLED=0 go build $(BUILDFLAGS) -ldflags="$(LDFLAGS)" -o $(ROOT_DIR)/bin ./...; \
		popd; \
    done
.PHONY: build

.PHONY: build-all
build-all:
	GOOS=$(OS) GOARCH=$(ARCH) $(MAKE) build WHAT='./cmd/...'

install: WHAT ?= ./cmd/... ./cli/cmd/... ./sdk/cmd/...
install: require-jq require-go require-git verify-go-versions ## Install the project
	set -x; for W in $(WHAT); do \
		pushd . && cd $${W%..}; \
  		GOOS=$(OS) GOARCH=$(ARCH) CGO_ENABLED=0 go install -ldflags="$(LDFLAGS)" ./...; \
		popd; \
  	done
.PHONY: install

$(GOLANGCI_LINT):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) github.com/golangci/golangci-lint/cmd/golangci-lint $(GOLANGCI_LINT_BIN) $(GOLANGCI_LINT_VER)

$(HTTEST):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) go.xrstf.de/httest $(HTTEST_BIN) $(HTTEST_VER)

$(LOGCHECK):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) sigs.k8s.io/logtools/logcheck $(LOGCHECK_BIN) $(LOGCHECK_VER)

$(CODE_GENERATOR):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) github.com/kcp-dev/code-generator/v2 $(CODE_GENERATOR_BIN) $(CODE_GENERATOR_VER)

$(KCP_APIGEN_GEN):
	pushd . && cd sdk && GOBIN=$(TOOLS_GOBIN_DIR) go install ./cmd/apigen && popd

lint: $(GOLANGCI_LINT) $(LOGCHECK) ## Verify lint
	$(GOLANGCI_LINT) run --timeout 20m ./...
	./hack/verify-contextual-logging.sh
.PHONY: lint

update-contextual-logging: $(LOGCHECK) ## Update contextual logging
	UPDATE=true ./hack/verify-contextual-logging.sh
.PHONY: update-contextual-logging

.PHONY: generate-cli-docs
generate-cli-docs: ## Generate cli docs
	git clean -fdX docs/content/reference/cli
	pushd . && cd docs/generators/cli-doc && go build . && popd
	./docs/generators/cli-doc/cli-doc -output docs/content/reference/cli

.PHONY: generate-api-docs
generate-api-docs: ## Generate api docs
	git clean -fdX docs/content/reference/api
	docs/generators/crd-ref/run-crd-ref-gen.sh

VENVDIR=$(abspath docs/venv)
REQUIREMENTS_TXT=docs/requirements.txt

.PHONY: local-docs
local-docs: venv ## Run mkdocs serve
	. $(VENV)/activate; \
	VENV=$(VENV) cd docs && mkdocs serve

.PHONY: serve-docs
serve-docs: venv ## Serve docs
	. $(VENV)/activate; \
	VENV=$(VENV) REMOTE=$(REMOTE) BRANCH=$(BRANCH) docs/scripts/serve-docs.sh

.PHONY: deploy-docs
deploy-docs: venv ## Deploy docs
	. $(VENV)/activate; \
	REMOTE=$(REMOTE) BRANCH=$(BRANCH) docs/scripts/deploy-docs.sh

vendor: ## Vendor the dependencies
	go mod tidy
	go mod vendor
.PHONY: vendor

tools: $(GOLANGCI_LINT) $(HTTEST) $(CONTROLLER_GEN) $(KCP_APIGEN_GEN) $(YAML_PATCH) $(GOTESTSUM) $(CODE_GENERATOR) ## Install tools
.PHONY: tools

$(CONTROLLER_GEN):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) sigs.k8s.io/controller-tools/cmd/controller-gen $(CONTROLLER_GEN_BIN) $(CONTROLLER_GEN_VER)

$(YAML_PATCH):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) github.com/pivotal-cf/yaml-patch/cmd/yaml-patch $(YAML_PATCH_BIN) $(YAML_PATCH_VER)

$(GOTESTSUM):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) gotest.tools/gotestsum $(GOTESTSUM_BIN) $(GOTESTSUM_VER)

crds: $(CONTROLLER_GEN) $(YAML_PATCH) ## Generate crds
	./hack/update-codegen-crds.sh
.PHONY: crds

codegen:  $(KCP_APIGEN_GEN) $(CODE_GENERATOR) crds ## Generate all
	go mod download
	./hack/update-codegen-clients.sh
	$(MAKE) imports
.PHONY: codegen

# Note, running this locally if you have any modified files, even those that are not generated,
# will result in an error. This target is mostly for CI jobs.
.PHONY: verify-codegen
verify-codegen: ## Verify codegen
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

	./hack/verify-apiresourceschemas.sh

.PHONY: imports
imports: WHAT ?=
imports: $(GOLANGCI_LINT) verify-go-versions
	@if [ -n "$(WHAT)" ]; then \
	  $(GOLANGCI_LINT) run --enable-only=gci --fix --fast $(WHAT); \
	else \
	  for MOD in . $$(git ls-files '**/go.mod' | sed 's,/go.mod,,'); do \
		(cd $$MOD; $(GOLANGCI_LINT) run --enable-only=gci --fix --fast); \
	  done; \
	fi

$(TOOLS_DIR)/verify_boilerplate.py:
	mkdir -p $(TOOLS_DIR)
	curl --fail --retry 3 -L -o $(TOOLS_DIR)/verify_boilerplate.py https://raw.githubusercontent.com/kubernetes/repo-infra/master/hack/verify_boilerplate.py
	chmod +x $(TOOLS_DIR)/verify_boilerplate.py

.PHONY: verify-boilerplate
verify-boilerplate: $(TOOLS_DIR)/verify_boilerplate.py ## Verify boilerplate
	$(TOOLS_DIR)/verify_boilerplate.py --boilerplate-dir=hack/boilerplate --skip docs/venv --skip pkg/network/dialer

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
TEST_FEATURE_GATES ?= WorkspaceMounts=true

.PHONY: test-e2e
ifdef USE_GOTESTSUM
test-e2e: $(GOTESTSUM)
endif
test-e2e: $(HTTEST)
test-e2e: TEST_ARGS ?=
test-e2e: WHAT ?= ./test/e2e...
test-e2e: build-all ## Run e2e tests
	UNSAFE_E2E_HACK_DISABLE_ETCD_FSYNC=true NO_GORUN=1 GOOS=$(OS) GOARCH=$(ARCH) \
		$(GO_TEST) -race $(COUNT_ARG) $(PARALLELISM_ARG) $(WHAT) $(TEST_ARGS) $(COMPLETE_SUITES_ARG)


.PHONY: test-e2e-shared-minimal
ifdef USE_GOTESTSUM
test-e2e-shared-minimal: $(GOTESTSUM)
endif
test-e2e-shared-minimal: $(HTTEST)
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
	rm -f "$(WORK_DIR)/.kcp/ready-to-test"
	UNSAFE_E2E_HACK_DISABLE_ETCD_FSYNC=true NO_GORUN=1 \
		./bin/test-server --quiet --log-dir-path="$(LOG_DIR)" $(TEST_SERVER_ARGS) -- --feature-gates=$(TEST_FEATURE_GATES) 2>&1 & PID=$$! && echo "PID $$PID" && \
	trap 'kill -TERM $$PID' TERM INT EXIT && \
	while [ ! -f "$(WORK_DIR)/.kcp/ready-to-test" ]; do sleep 1; done && \
	echo 'Starting test(s)' && \
	NO_GORUN=1 GOOS=$(OS) GOARCH=$(ARCH) \
		$(GO_TEST) -race $(COUNT_ARG) $(PARALLELISM_ARG) $(WHAT) $(TEST_ARGS) \
		-args --use-default-kcp-server $(SUITES_ARG) \
	$(if $(value WAIT),|| { echo "Terminated with $$?"; wait "$$PID"; },)

.PHONY: test-e2e-sharded-minimal
ifdef USE_GOTESTSUM
test-e2e-sharded-minimal: $(GOTESTSUM)
endif
test-e2e-sharded-minimal: $(HTTEST)
test-e2e-sharded-minimal: TEST_ARGS ?=
test-e2e-sharded-minimal: WHAT ?= ./test/e2e...
test-e2e-sharded-minimal: WORK_DIR ?= .
test-e2e-sharded-minimal: SHARDS ?= 2
ifdef ARTIFACT_DIR
test-e2e-sharded-minimal: LOG_DIR ?= $(ARTIFACT_DIR)/kcp
else
test-e2e-sharded-minimal: LOG_DIR ?= $(WORK_DIR)/.kcp
endif
test-e2e-sharded-minimal: build-all
	mkdir -p "$(LOG_DIR)" "$(WORK_DIR)/.kcp"
	rm -f "$(WORK_DIR)/.kcp/ready-to-test"
	UNSAFE_E2E_HACK_DISABLE_ETCD_FSYNC=true NO_GORUN=1 ./bin/sharded-test-server --quiet --v=2 --log-dir-path="$(LOG_DIR)" --work-dir-path="$(WORK_DIR)" --shard-run-virtual-workspaces=false --shard-feature-gates=$(TEST_FEATURE_GATES) $(TEST_SERVER_ARGS) --number-of-shards=$(SHARDS) 2>&1 & PID=$$!; echo "PID $$PID" && \
	trap 'kill -TERM $$PID && wait $$PID' TERM INT EXIT && \
	while [ ! -f "$(WORK_DIR)/.kcp/ready-to-test" ]; do sleep 1; done && \
	echo 'Starting test(s)' && \
	NO_GORUN=1 GOOS=$(OS) GOARCH=$(ARCH) $(GO_TEST) -race $(COUNT_ARG) $(PARALLELISM_ARG) $(WHAT) $(TEST_ARGS) \
		-args --use-default-kcp-server --shard-kubeconfigs=root=$(PWD)/.kcp-0/admin.kubeconfig$(shell if [ $(SHARDS) -gt 1 ]; then seq 1 $$[$(SHARDS) - 1]; fi | while read n; do echo -n ",shard-$$n=$(PWD)/.kcp-$$n/admin.kubeconfig"; done) \
		$(SUITES_ARGS)

# This is just easy target to run 2 shard test server locally until manually killed.
# You can targer test to it by running:
# go test ./test/e2e/apibinding/... --kcp-kubeconfig=$(pwd)/.kcp/admin.kubeconfig --shard-kubeconfigs=root=$(pwd)/.kcp-0/admin.kubeconfig -run=^TestAPIBindingEndpointSlicesSharded$
test-run-sharded-server: WORK_DIR ?= .
test-run-sharded-server: LOG_DIR ?= $(WORK_DIR)/.kcp
test-run-sharded-server:
	mkdir -p "$(LOG_DIR)" "$(WORK_DIR)/.kcp"
	rm -f "$(WORK_DIR)/.kcp/ready-to-test"
	UNSAFE_E2E_HACK_DISABLE_ETCD_FSYNC=true NO_GORUN=1 ./bin/sharded-test-server --quiet --v=2 --log-dir-path="$(LOG_DIR)" --work-dir-path="$(WORK_DIR)" --shard-run-virtual-workspaces=false --shard-feature-gates=$(TEST_FEATURE_GATES) $(TEST_SERVER_ARGS) --number-of-shards=2 2>&1 & PID=$$!; echo "PID $$PID" && \
	trap 'kill -TERM $$PID' TERM INT EXIT && \
	while [ ! -f "$(WORK_DIR)/.kcp/ready-to-test" ]; do sleep 1; done && \
	echo 'Server started' && \
	wait $$PID

.PHONY: test
ifdef USE_GOTESTSUM
test: $(GOTESTSUM)
endif
test: WHAT ?= ./...
# We will need to move into the sub package, of sdk to run those tests.
test: ## Run tests
	$(GO_TEST) -race $(COUNT_ARG) -coverprofile=coverage.txt -covermode=atomic $(TEST_ARGS) $$(go list "$(WHAT)" | grep -v ./test/e2e/)
	cd sdk && $(GO_TEST) -race $(COUNT_ARG) -coverprofile=coverage.txt -covermode=atomic $(TEST_ARGS) $(WHAT)

.PHONY: verify-k8s-deps
verify-k8s-deps: ## Verify kubernetes deps
	hack/validate-k8s.sh

.PHONY: verify-imports
verify-imports: ## Verify imports
	hack/verify-imports.sh

.PHONY: verify-go-versions
verify-go-versions: ## Verify go versions
	hack/verify-go-versions.sh

.PHONY: modules
modules: ## Run go mod tidy to ensure modules are up to date
	hack/update-go-modules.sh

.PHONY: verify-modules
verify-modules: modules  ## Verify go modules are up to date
	hack/verify-go-modules.sh

.PHONY: clean
clean: clean-workdir ## Clean all
	rm -fr $(TOOLS_DIR)
	rm -f $(GOBIN_DIR)/*

.PHONY: clean-workdir
clean-workdir: WORK_DIR ?= .
clean-workdir: ## Clean workdir
	rm -fr $(WORK_DIR)/.kcp*

.PHONY: download-e2e-logs
download-e2e-logs: ## Download e2e logs from a given URL
	OUT=$(OUT) URL=$(URL) hack/download-e2e-logs.sh

tilt-kind-up:
	./contrib/tilt/kind.sh

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

include Makefile.venv
