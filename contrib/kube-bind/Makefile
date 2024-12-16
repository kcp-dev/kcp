TOOLS_DIR=hack/tools
TOOLS_BIN_DIR := $(abspath $(TOOLS_DIR))/bin
export TOOLS_BIN_DIR # so hack scripts can use it

GO_INSTALL = ./hack/go-install.sh

KCP_VERSION ?= 0.23.0
CONTROLLER_GEN := $(TOOLS_BIN_DIR)/controller-gen
export CONTROLLER_GEN # so hack scripts can use it

KUBE_BIND_VERSION ?= v0.4.1
KUBE_BIND_DIR := kube-bind
KUBE_BIND_REPO := https://github.com/kube-bind/kube-bind
export KUBE_BIND_DIR # so hack scripts can use it
export KUBE_BIND_REPO # so hack scripts can use it
export KUBE_BIND_VERSION # so hack scripts can use it

CODE_GENERATOR_VER := v2.1.0
CODE_GENERATOR_BIN := code-generator
CODE_GENERATOR := $(TOOLS_BIN_DIR)/$(CODE_GENERATOR_BIN)-$(CODE_GENERATOR_VER)
export CODE_GENERATOR # so hack scripts can use it

KCP_APIGEN_VER := v0.26.0
KCP_APIGEN_BIN := apigen
KCP_APIGEN_GEN := $(TOOLS_BIN_DIR)/$(KCP_APIGEN_BIN)-$(KCP_APIGEN_VER)
export KCP_APIGEN_GEN # so hack scripts can use it

OPENSHIFT_GOIMPORTS_VER := c72f1dc2e3aacfa00aece3391d938c9bc734e791
OPENSHIFT_GOIMPORTS_BIN := openshift-goimports
OPENSHIFT_GOIMPORTS := $(TOOLS_BIN_DIR)/$(OPENSHIFT_GOIMPORTS_BIN)-$(OPENSHIFT_GOIMPORTS_VER)
export OPENSHIFT_GOIMPORTS # so hack scripts can use it

$(KCP_APIGEN_GEN):
	GOBIN=$(TOOLS_BIN_DIR) $(GO_INSTALL) github.com/kcp-dev/kcp/sdk/cmd/apigen $(KCP_APIGEN_BIN) $(KCP_APIGEN_VER)

$(CONTROLLER_GEN):  # Build controller-gen from tools folder.
	cd $(TOOLS_BIN_DIR) && go build -tags=tools -o bin/controller-gen sigs.k8s.io/controller-tools/cmd/controller-gen

$(CODE_GENERATOR):
	GOBIN=$(TOOLS_BIN_DIR) $(GO_INSTALL) github.com/kcp-dev/code-generator/v2 $(CODE_GENERATOR_BIN) $(CODE_GENERATOR_VER)

$(OPENSHIFT_GOIMPORTS):
	GOBIN=$(TOOLS_BIN_DIR) $(GO_INSTALL) github.com/openshift-eng/openshift-goimports $(OPENSHIFT_GOIMPORTS_BIN) $(OPENSHIFT_GOIMPORTS_VER)


tools: $(CONTROLLER_GEN) $(KCP_APIGEN_GEN) $ $(CODE_GENERATOR $(OPENSHIFT_GOIMPORTS)) ## Install tools
.PHONY: tools

build: WHAT ?= ./cmd/...
build:
	GOOS=$(OS) GOARCH=$(ARCH) go build $(BUILDFLAGS) -ldflags="$(LDFLAGS)" -o bin/ $(WHAT)
.PHONY: build


run-dev-init: build
	bin/bootstrap init --kcp-kubeconfig=../../.kcp/admin.kubeconfig

run-dev: build
	bin/backend start \
	--oidc-issuer-client-secret=Z2Fyc2lha2FsYmlzdmFuZGVuekWplCg== \
	--oidc-issuer-client-id=kcp-dev \
	--oidc-issuer-url=https://127.0.0.1:5556/dex \
	--oidc-callback-url=https://127.0.0.1:8080/callback \
	--oidc-ca-file=../../127.0.0.1.pem \
	--pretty-name="CorpAAA.com" \
	--namespace-prefix="kube-bind-" \
	--cookie-signing-key=bGMHz7SR9XcI9JdDB68VmjQErrjbrAR9JdVqjAOKHzE= \
	--cookie-encryption-key=wadqi4u+w0bqnSrVFtM38Pz2ykYVIeeadhzT34XlC1Y= \
	--workspace-path="root:kube-bind:core" \
	--apiexport-name="core.kube-bind.io" \
	--kubeconfig=../../.kcp/admin.kubeconfig \

crds: $(CONTROLLER_GEN) ## Generate crds
	./hack/update-codegen-crds.sh
.PHONY: crds

codegen: crds
	$(MAKE) imports
.PHONY: codegen

.PHONY: imports
imports: $(OPENSHIFT_GOIMPORTS)
	$(OPENSHIFT_GOIMPORTS) -m github.com/kcp-dev/kcp/contrib/kube-bind