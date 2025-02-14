# KCP Dex

How to run local kcp with dex.

## Step by step guide

### Dex

Run dex outside of kcp
We use dex to manage OIDC, following the steps below you can run a local OIDC issuer using dex:

* First, clone the dex repo: `git clone https://github.com/mjudeikis/dex.git -b mjudeikis/groups.support`
  * Important: We use fork to allow local group support k8s relies on: https://github.com/dexidp/dex/issues/1080
* `cd dex` and then build the dex binary `make build`
* The binary will be created in `bin/dex`
* Adjust the config file(`examples/config-dev.yaml`) for dex by specifying the server callback method:
* Generate certificates for dex:
```bash
GOBIN=$(pwd)/bin go install github.com/mjudeikis/genkey
./bin/genkey 127.0.0.1
```

* Run dex: `./bin/dex serve ../contrib/kcp-dex/kcp-config.yaml `


### KCP

Start kcp with oidc enabled, you can either use the OIDC flags or structured authentication configuration from a file. Example configuration is shown in `auth-config.yaml`.

## OIDC Flags

```bash
go run ./cmd/kcp start \
--oidc-issuer-url=https://127.0.0.1:5556/dex \
--oidc-client-id=kcp-dev \
--oidc-groups-claim=groups \
--oidc-ca-file=127.0.0.1.pem
```

## Structured Authentication Config

```bash
CA_CERT=$(openssl x509 -in 127.0.0.1.pem | sed 's/^/      /')
```
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

Start a kcp server:

```bash
./bin/kcp start --authentication-config auth-config.yaml
```

### Login

Use oidc plugin:

```bash
kubectl krew install oidc-login

# to test
kubectl oidc-login get-token \
--oidc-issuer-url=https://127.0.0.1:5556/dex \
--oidc-client-id=kcp-dev \
--oidc-client-secret=Z2Fyc2lha2FsYmlzdmFuZGVuekWplCg== \
--insecure-skip-tls-verify \
--oidc-extra-scope=groups,email

# to configure kubectl to use this plugin
export KUBECONFIG=.kcp/admin.kubeconfig

# create a new user with oidc
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

# set current context to use oidc
kubectl config set-context --current --user=oidc

# test
# password is admin:password
kubectl get ws
kubectl create workspace bob
