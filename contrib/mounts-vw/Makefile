
GO_INSTALL = ./hack/go-install.sh

TOOLS_DIR=hack/tools
TOOLS_GOBIN_DIR := $(abspath $(TOOLS_DIR))
GOBIN_DIR=$(abspath ./bin)
PATH := $(GOBIN_DIR):$(TOOLS_GOBIN_DIR):$(PATH)
TMPDIR := $(shell mktemp -d)
KIND_CLUSTER_NAME ?= kind

CONTROLLER_GEN_VER := v0.16.1
CONTROLLER_GEN_BIN := controller-gen
CONTROLLER_GEN := $(TOOLS_DIR)/$(CONTROLLER_GEN_BIN)-$(CONTROLLER_GEN_VER)
export CONTROLLER_GEN # so hack scripts can use it


CODE_GENERATOR_VER := v2.1.0
CODE_GENERATOR_BIN := code-generator
CODE_GENERATOR := $(TOOLS_GOBIN_DIR)/$(CODE_GENERATOR_BIN)-$(CODE_GENERATOR_VER)
export CODE_GENERATOR # so hack scripts can use it

KCP_APIGEN_VER := v0.26.0
KCP_APIGEN_BIN := apigen
KCP_APIGEN_GEN := $(TOOLS_DIR)/$(KCP_APIGEN_BIN)-$(KCP_APIGEN_VER)
export KCP_APIGEN_GEN # so hack scripts can use it

OPENSHIFT_GOIMPORTS_VER := c72f1dc2e3aacfa00aece3391d938c9bc734e791
OPENSHIFT_GOIMPORTS_BIN := openshift-goimports
OPENSHIFT_GOIMPORTS := $(TOOLS_DIR)/$(OPENSHIFT_GOIMPORTS_BIN)-$(OPENSHIFT_GOIMPORTS_VER)
export OPENSHIFT_GOIMPORTS # so hack scripts can use it

VERSION ?= $(shell git describe --tags --always --dirty)

LDFLAGS= -s -w \
	-extldflags '-static'

all: build
.PHONY: all

ldflags:
	@echo $(LDFLAGS)

tools: $(CONTROLLER_GEN) $(KCP_APIGEN_GEN) $ $(CODE_GENERATOR) $(OPENSHIFT_GOIMPORTS) ## Install tools
.PHONY: tools

$(CONTROLLER_GEN):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) sigs.k8s.io/controller-tools/cmd/controller-gen $(CONTROLLER_GEN_BIN) $(CONTROLLER_GEN_VER)

$(CODE_GENERATOR):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) github.com/kcp-dev/code-generator/v2 $(CODE_GENERATOR_BIN) $(CODE_GENERATOR_VER)

$(KCP_APIGEN_GEN):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) github.com/kcp-dev/kcp/sdk/cmd/apigen $(KCP_APIGEN_BIN) $(KCP_APIGEN_VER)

$(OPENSHIFT_GOIMPORTS):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) github.com/openshift-eng/openshift-goimports $(OPENSHIFT_GOIMPORTS_BIN) $(OPENSHIFT_GOIMPORTS_VER)

images:
	KO_DOCKER_REPO=ghcr.io/faroshq/ ko build  ./cmd/proxy

.PHONY: require-%
require-%:
	@if ! command -v $* 1> /dev/null 2>&1; then echo "$* not found in ${PATH}"; exit 1; fi

.PHONY: verify-go-versions
verify-go-versions: ## Verify go versions
	hack/verify-go-versions.sh

build: WHAT ?= ./cmd/...
build: require-jq require-go require-git verify-go-versions ## Build the project
	GOOS=$(OS) GOARCH=$(ARCH) CGO_ENABLED=0 go build $(BUILDFLAGS) -ldflags="$(LDFLAGS)" -o bin $(WHAT)
.PHONY: build

crds: $(CONTROLLER_GEN) $(YAML_PATCH) ## Generate crds
	./hack/update-codegen-crds.sh
.PHONY: crds

codegen: crds $(CODE_GENERATOR) ## Generate all
	go mod download
	./hack/update-codegen-clients.sh
	$(MAKE) imports

.PHONY: imports
imports: $(OPENSHIFT_GOIMPORTS) verify-go-versions
	$(OPENSHIFT_GOIMPORTS) -m github.com/kcp-dev/kcp/contrib/mounts-vw

test:
	go test ./...

