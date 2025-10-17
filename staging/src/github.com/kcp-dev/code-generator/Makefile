# Copyright 2025 The KCP Authors.
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

SHELL := /usr/bin/env bash

KCP_ROOT_DIR ?= $(abspath ../../../../..)

GO_INSTALL = $(KCP_ROOT_DIR)/hack/go-install.sh

BUILD_DEST ?= _build
BUILDFLAGS ?=
CMD ?= $(notdir $(wildcard ./cmd/*))

TOOLS_DIR=hack/tools
GOBIN_DIR := $(KCP_ROOT_DIR)/$(TOOLS_DIR)
TMPDIR := $(shell mktemp -d)

GOLANGCI_LINT_VER := v2.1.6
GOLANGCI_LINT_BIN := golangci-lint
GOLANGCI_LINT := $(GOBIN_DIR)/$(GOLANGCI_LINT_BIN)-$(GOLANGCI_LINT_VER)

$(GOLANGCI_LINT):
	GOBIN=$(GOBIN_DIR) $(GO_INSTALL) github.com/golangci/golangci-lint/cmd/golangci-lint $(GOLANGCI_LINT_BIN) $(GOLANGCI_LINT_VER)

.PHONY: imports
imports: WHAT ?=
imports: $(GOLANGCI_LINT)
	if [ -n "$(WHAT)" ]; then \
	  $(GOLANGCI_LINT) fmt --enable gci -c $(KCP_ROOT_DIR)/.golangci.yaml $(WHAT); \
	else \
	  $(GOLANGCI_LINT) fmt --enable gci -c $(KCP_ROOT_DIR)/.golangci.yaml ; \
	fi;

.PHONY: clean
clean:
	rm -rf $(BUILD_DEST)
	@echo "Cleaned $(BUILD_DEST)"

.PHONY: build
build: $(CMD)

.PHONY: $(CMD)
$(CMD): %: $(BUILD_DEST)/%

$(BUILD_DEST)/%: cmd/%
	go build $(BUILDFLAGS) -o $@ ./cmd/$*

.PHONY: install
install:
	go install

.PHONY: codegen
codegen: build
	./hack/update-codegen.sh
	$(MAKE) imports

# Note, running this locally if you have any modified files, even those that are not generated,
# will result in an error. This target is mostly for CI jobs.
.PHONY: verify-codegen
verify-codegen:
	if [[ -n "${GITHUB_WORKSPACE}" ]]; then \
		mkdir -p $$(go env GOPATH)/src/github.com/kcp-dev; \
		ln -s ${GITHUB_WORKSPACE} $$(go env GOPATH)/src/github.com/kcp-dev/code-generator; \
	fi

	$(MAKE) codegen

	if ! git diff --quiet HEAD; then \
		git diff; \
		echo "You need to run 'make codegen' to update generated files and commit them"; \
		exit 1; \
	fi
