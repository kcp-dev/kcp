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

CONTROLLER_GEN_VER := v0.7.0
CONTROLLER_GEN_BIN := controller-gen
CONTROLLER_GEN := $(TOOLS_DIR)/$(CONTROLLER_GEN_BIN)-$(CONTROLLER_GEN_VER)
export CONTROLLER_GEN # so hack scripts can use it

OPENSHIFT_GOIMPORTS_VER := b92214262c6ce8598aefdee87aae6b8cf1a9fc86
OPENSHIFT_GOIMPORTS_BIN := openshift-goimports
OPENSHIFT_GOIMPORTS := $(TOOLS_DIR)/$(OPENSHIFT_GOIMPORTS_BIN)-$(OPENSHIFT_GOIMPORTS_VER)
export OPENSHIFT_GOIMPORTS # so hack scripts can use it

all: build
.PHONY: all

build: ## Build the project
	go build -o bin ./cmd/...
.PHONY: build

install: install-ingress-controller
	go install ./cmd/...
.PHONY: install


INGRESS_CONTROLLER_DIR = ./build/kcp-ingress

clone-ingress-controller:
	test ! -d $(INGRESS_CONTROLLER_DIR) \
	&& mkdir -p $(INGRESS_CONTROLLER_DIR) \
	&& git clone --branch update_kcp_envoy https://github.com/jmprusi/kcp-ingress $(INGRESS_CONTROLLER_DIR) || true

install-ingress-controller: clone-ingress-controller
	cd $(INGRESS_CONTROLLER_DIR) \
	&& git pull \
	&& go install ./cmd/...

.PHONY: install-ingress-controller

vendor: ## Vendor the dependencies
	go mod tidy
	go mod vendor
.PHONY: vendor

$(CONTROLLER_GEN):
	GOBIN=$(GOBIN_DIR) $(GO_INSTALL) sigs.k8s.io/controller-tools/cmd/controller-gen $(CONTROLLER_GEN_BIN) $(CONTROLLER_GEN_VER)

codegen: $(CONTROLLER_GEN) ## Run the codegenerator
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

	if ! git diff -I '^Copyright.*' --quiet HEAD; then \
		git diff; \
		echo "You need to run 'make codegen' to update generated files and commit them"; \
		exit 1; \
	fi

$(OPENSHIFT_GOIMPORTS):
	GOBIN=$(GOBIN_DIR) $(GO_INSTALL) github.com/coreydaley/openshift-goimports $(OPENSHIFT_GOIMPORTS_BIN) $(OPENSHIFT_GOIMPORTS_VER)

.PHONY: imports
imports: $(OPENSHIFT_GOIMPORTS)
	$(OPENSHIFT_GOIMPORTS) -m github.com/kcp-dev/kcp

COUNT ?= 5

.PHONY: test-e2e
test-e2e: install
	go test -race -count $(COUNT) ./test/e2e... $(WHAT)

.PHONY: test
test: install
	go test -race -count $(COUNT) -coverprofile=coverage.txt -covermode=atomic ./... $(WHAT)

.PHONY: demos
demos: build ## Runs all the default demos (kubecon and apiNegotiation).
	cd contrib/demo && ./runDemos.sh 

.PHONY: demo-apinegotiation
demo-apinegotiation: build ## Run the API Negotiation demo.
	cd contrib/demo && ./runDemos.sh apiNegotiation

.PHONY: demo-kubecon
demo-kubecon: build ## Run the KubeCon demo.
	cd contrib/demo && ./runDemos.sh kubecon

.PHONY: demo-ingress
demo-ingress: build ## Run the Ingress demo.
	cd contrib/demo && ./runDemos.sh ingress

.PHONY: help
help: ## Show this help.
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'
