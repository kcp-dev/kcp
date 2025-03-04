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

Dex is an OpenID Connect (OIDC) provider used for authentication in Kubernetes and other platforms. Here is how to configure and run it locally
*Important: We use a fork to allow local group support that Kubernetes relies on: [dexidp/dex#1080](https://github.com/dexidp/dex/issues/1080)*

```bash
git clone https://github.com/mjudeikis/dex.git -b mjudeikis/groups.support
cd dex
make build
```

The compiled Dex binary will be available at `bin/dex`.

#### Generate Certificates for Dex

To continue with this tutorial, we need to generate self-signed certificates for authentication requests.
`genkey` tool can be used to create them for localhost.

```bash
GOBIN=$(pwd)/bin go install github.com/mjudeikis/genkey
./bin/genkey 127.0.0.1
```

#### Configure Dex

Dex requires a configuration file to define authentication parameters. Create a `dex-config.yaml` file with the following content:

```bash
issuer: https://127.0.0.1:5556/dex
web:
  https: 127.0.0.1:5556
  tlsCert: ../127.0.0.1.pem
  tlsKey: ../127.0.0.1.pem
storage:
  type: sqlite3
  config:
    file: examples/dex.db
staticClients:
  - id: kcp-dev
    public: true
    redirectURIs:
    - http://localhost:8000
    name: 'KCP App'
    secret: <generate a secret for the static client here>

# Let dex keep a list of passwords which can be used to login to dex.
enablePasswordDB: true

# A static list of passwords to login the end user. By identifying here, dex
# won't look in its underlying storage for passwords.
#
# If this option isn't chosen users may be added through the gRPC API.
staticPasswords:
- email: "admin"
  hash: <bcrypt hash of the string "password": $(echo password | htpasswd -BinC 10 admin | cut -d: -f2)>
  username: "admin"
  userID: "08a8684b-db88-4b73-90a9-3cd1661f5466"
  groups: ["system:kcp:admin", "system:admin"]
```

#### Run Dex

Start Dex with the required configuration:

```bash
./bin/dex serve ../contrib/kcp-dex/dex-config.yaml
```

### Run kcp with OIDC Enabled

You can configure kcp authentication using OIDC flags or a structured authentication configuration file.

#### Using OIDC Flags

To start kcp with OIDC authentication enabled, run:

```bash
kcp start \
--oidc-issuer-url=https://127.0.0.1:5556/dex \
--oidc-client-id=kcp-dev \
--oidc-groups-claim=groups \
--oidc-ca-file=127.0.0.1.pem
```
- `--oidc-issuer-url` URL of the provider that allows the API server to discover public signing keys.

- `--oidc-client-id` A client id that all tokens must be issued for.

- `--oidc-groups-claim` JWT claim to use as the user's group.

- `--oidc-ca-file` The path to the certificate for the CA that signed your identity provider's web certificate.

#### Using Structured Authentication Configuration

Alternatively, create an authentication configuration file for kcp

```bash
CA_CERT=$(openssl x509 -in 127.0.0.1.pem | sed 's/^/      /')
```

This extracts the CA certificate and formats it properly for AuthenticationConfiguration yaml file.

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

This command starts kcp with the specified authentication configuration.

```bash
kcp start --authentication-config auth-config.yaml
```

### Configure OIDC Login with Kubectl

To allow kubectl to authenticate via OIDC, install the required plugin:

```bash
kubectl krew install oidc-login
```

#### Verify OIDC Login

Verify the login process by running

```bash
kubectl oidc-login get-token \
--oidc-issuer-url=https://127.0.0.1:5556/dex \
--oidc-client-id=kcp-dev \
--oidc-client-secret=<client-secret> \
--insecure-skip-tls-verify \
--oidc-extra-scope=groups,email
```

- `--oidc-issuer-url` specifies Dex server endpoint.

- `--oidc-client-id` is the client ID used during authentication.

- `--oidc-client-secret` authenticates the client, specify the one you have provided in the dex-config file.

- `--oidc-extra-scope` requests additional claims.

You will be redirected to the Dex to provide your static admin user and password that was configured in the dex-config.yaml file.

#### Configure Context for OIDC User

To use the OIDC-authenticated user in kubectl, update the kubeconfig

```bash
export KUBECONFIG=.kcp/admin.kubeconfig

kubectl config set-credentials oidc \
  --exec-api-version=client.authentication.k8s.io/v1beta1 \
  --exec-command=kubectl \
  --exec-arg=oidc-login \
  --exec-arg=get-token \
  --exec-arg=--oidc-issuer-url=https://127.0.0.1:5556/dex  \
  --exec-arg=--oidc-client-id=kcp-dev \
  --exec-arg=--oidc-client-secret=<client-secret> \
  --exec-arg=--oidc-extra-scope=groups \
  --exec-arg=--oidc-extra-scope=email \
  --exec-arg=--insecure-skip-tls-verify

kubectl config set-context --current --user=oidc
```

- This command configures the Kubernetes context to use OIDC authentication.

- The `oidc-login` plugin retrieves authentication tokens dynamically.

### Verify OIDC Authentication in kcp

To confirm the setup, try to first fetch workspaces and then create a new one

```bash
kubectl get ws
kubectl create workspace test-oidc
```
