---
description: >
  How to setup OIDC authentication in kcp.
---

# OIDC Setup

OpenID Connect (OIDC) is a simple identity layer on top of the OAuth 2.0 protocol, which allows clients to verify the identity of users based on the authentication performed by an external authorization server. In this guide, we will set up OIDC authentication in kcp using Dex as the identity provider.
For more details on Kubernetes specific configuration, please refer to this [page](https://kubernetes.io/docs/reference/access-authn-authz/authentication/#openid-connect-tokens).

## Configuring OIDC in kcp with Dex

This guide provides step-by-step instructions on setting up OIDC authentication in a local kcp server using Dex. OIDC allows for secure authentication using external identity providers, enhancing security and ease of access.

## Prerequisites

Before you begin, ensure you have the following installed:
- [Go](https://go.dev/dl/)
- [Kubectl](https://kubernetes.io/docs/tasks/tools/install-kubectl/)
- [Krew](https://krew.sigs.k8s.io/docs/)
- [OpenSSL](https://www.openssl.org/)

#### Set Up Dex

Dex is an OpenID Connect (OIDC) provider used for authentication in Kubernetes and other platforms. Here’s how to configure and run it locally
*Important: We use a fork to allow local group support that Kubernetes relies on: [dexidp/dex#1080](https://github.com/dexidp/dex/issues/1080)*

```bash
git clone https://github.com/mjudeikis/dex.git -b mjudeikis/groups.support
cd dex
make build
```

The binary will be available at `bin/dex`.

#### Generate Certificates for Dex

```bash
GOBIN=$(pwd)/bin go install github.com/mjudeikis/genkey
./bin/genkey 127.0.0.1
```

#### Run Dex

```bash
./bin/dex serve ../contrib/kcp-dex/kcp-config.yaml
```

### Start kcp with OIDC Enabled

You can configure kcp authentication using OIDC flags or a structured authentication configuration file.

#### Using OIDC Flags

```bash
go run ./cmd/kcp start \
--oidc-issuer-url=https://127.0.0.1:5556/dex \
--oidc-client-id=kcp-dev \
--oidc-groups-claim=groups \
--oidc-ca-file=127.0.0.1.pem
```

#### Using Structured Authentication Configuration

```bash
CA_CERT=$(openssl x509 -in 127.0.0.1.pem | sed 's/^/      /')
```

Create `auth-config.yaml`:

```bash
cat << EOF_AuthConfig > auth-config.yaml
apiVersion: apiserver.config.k8s.io/v1beta1
kind: AuthenticationConfiguration
jwt:
- issuer:
    url: https://127.0.0.1:5556/dex
    certificateAuthority: |
$CA_CERT
    audiences:
      - kcp-dev
    audienceMatchPolicy: MatchAny
  claimMappings:
    username:
      claim: "email"
      prefix: ""
    groups:
      claim: "groups"
      prefix: ""
  claimValidationRules: []
  userValidationRules: []
EOF_AuthConfig
```

Start kcp:

```bash
./bin/kcp start --authentication-config auth-config.yaml
```

### Configure OIDC Login with Kubectl

Install OIDC Login plugin for kubectl:

```bash
kubectl krew install oidc-login
```

#### Test OIDC Login

```bash
kubectl oidc-login get-token \
--oidc-issuer-url=https://127.0.0.1:5556/dex \
--oidc-client-id=kcp-dev \
--oidc-client-secret=Z2Fyc2lha2FsYmlzdmFuZGVuekWplCg== \
--insecure-skip-tls-verify \
--oidc-extra-scope=groups,email
```

#### Configure Context for OIDC User

```bash
export KUBECONFIG=.kcp/admin.kubeconfig

kubectl config set-credentials oidc \
  --exec-api-version=client.authentication.k8s.io/v1beta1 \
  --exec-command=kubectl \
  --exec-arg=oidc-login \
  --exec-arg=get-token \
  --exec-arg=--oidc-issuer-url=https://127.0.0.1:5556/dex  \
  --exec-arg=--oidc-client-id=kcp-dev \
  --exec-arg=--oidc-client-secret=Z2Fyc2lha2FsYmlzdmFuZGVuekWplCg== \
  --exec-arg=--oidc-extra-scope=groups \
  --exec-arg=--oidc-extra-scope=email \
  --exec-arg=--insecure-skip-tls-verify

kubectl config set-context --current --user=oidc
```

### Test OIDC Authentication

```bash
kubectl get ws
kubectl create workspace bob
```
