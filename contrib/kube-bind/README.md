# Kube-Bind for KCP

This is example backend for KCP that uses [kube-bind](https://github.com/kube-bind/kube-bind) to bind api-exports.

Values here should match the values used to start kcp with so that the oidc tokens are valid.
We use kcp from `contrib/kcp-dex` as an example.


1. Create a kube-bind provider backend.

```bash

```


```bash
make build

bin/backend \
  --oidc-issuer-client-secret=Z2Fyc2lha2FsYmlzdmFuZGVuekWplCg== \
  --oidc-issuer-client-id=kcp-dev \
  --oidc-issuer-url=https://127.0.0.1:5556/dex \
  --oidc-callback-url=https://127.0.0.1:8080/callback \
  --pretty-name="CorpAAA.com" \
  --namespace-prefix="kube-bind-" \
  --cookie-signing-key=bGMHz7SR9XcI9JdDB68VmjQErrjbrAR9JdVqjAOKHzE= \
  --cookie-encryption-key=wadqi4u+w0bqnSrVFtM38Pz2ykYVIeeadhzT34XlC1Y=
```


# Architecture

Challenges:
1. backend needs to be aware of every workspace where APIExports are enabled to be
exported.
   1.1. We can use separate workspace and bind dedicated workspace for kube-bind.
   This means any workspace wanting to use kube-bind backend, would need to bind to this
   workspace apis.
   1.2. We can give global permissions to kube-bind and enable each labeled/annotated export
   to be exported.

Preferred solution: 1.1 as it would enable multipe kube-bind backends to be used simultaneously.

