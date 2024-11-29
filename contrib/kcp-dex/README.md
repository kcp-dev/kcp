# KCP Dex

How to run local kcp with dex.

## Step by step guide

### Dex

Run dex outside of kcp
We use dex to manage OIDC, following the steps below you can run a local OIDC issuer using dex:
* First, clone the dex repo: `git clone https://github.com/dexidp/dex.git`
* `cd dex` and then build the dex binary `make build`
* The binary will be created in `bin/dex`
* Adjust the config file(`examples/config-dev.yaml`) for dex by specifying the server callback method:
* Generate certificates for dex:
```bash
GOBIN=$(pwd)/bin go install github.com/mjudeikis/genkey
./bin/genkey -ca root
./bin/genkey -keyFile root.key -certFile root.crt 127.0.0.1
```

* Run dex: `./bin/dex serve contrib/kcp-dex/kcp-config.yaml `


### KCP

Start kcp with oidc enabled:

```bash
o run ./cmd/kcp start \
--oidc-issuer-url=https://127.0.0.1:5556/dex \
--oidc-client-id=kcp-dev \
--oidc-groups-claim=groups \
--oidc-ca-file=127.0.0.1.pem
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
--insecure-skip-tls-verify

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
  --exec-arg=--insecure-skip-tls-verify

# set current context to use oidc
kubectl config set-context --current --user=oidc
