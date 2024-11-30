
build: WHAT ?= ./cmd/...
build:
	GOOS=$(OS) GOARCH=$(ARCH) go build $(BUILDFLAGS) -ldflags="$(LDFLAGS)" -o bin/ $(WHAT)
.PHONY: build


run-dev:
	bin/backend \
	--oidc-issuer-client-secret=Z2Fyc2lha2FsYmlzdmFuZGVuekWplCg== \
	--oidc-issuer-client-id=kcp-dev \
	--oidc-issuer-url=https://127.0.0.1:5556/dex \
	--oidc-callback-url=https://127.0.0.1:8080/callback \
	--oidc-ca-file=../../127.0.0.1.pem \
	--pretty-name="CorpAAA.com" \
	--namespace-prefix="kube-bind-" \
	--cookie-signing-key=bGMHz7SR9XcI9JdDB68VmjQErrjbrAR9JdVqjAOKHzE= \
	--cookie-encryption-key=wadqi4u+w0bqnSrVFtM38Pz2ykYVIeeadhzT34XlC1Y=
