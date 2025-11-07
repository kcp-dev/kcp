---
description: >
  Accessing embedded etcd for local development
---

# Accessing Embedded etcd for Local Development

When developing locally with `kcp start`, you need to pass in client authentication and endpoint to access the embedded etcd. The correspond files can be found in the `.kcp/etcd-server/secrets` folder.

For example, you can pass them directly to the command

```sh
etcdctl --cert ./.kcp/etcd-server/secrets/client/cert.pem --key ./.kcp/etcd-server/secrets/client/key.pem --cacert ./.kcp/etcd-server/secrets/ca/cert.pem --endpoints=localhost:2379 member list
```

or set up the corresponding environment variables

```sh
export ETCDCTL_ENDPOINTS=https://localhost:2379
export ETCDCTL_CACERT=./.kcp/etcd-server/secrets/ca/cert.pem
export ETCDCTL_KEY=./.kcp/etcd-server/secrets/client/key.pem
export ETCDCTL_CERT=./.kcp/etcd-server/secrets/client/cert.pem

etcdctl member list
```

## Known issues

Currently it is not possible to connect using [etcd-workbench](https://github.com/tzfun/etcd-workbench), because the underlying TLS library [cannot handle embedded etcds ECDSA P-521 keys](https://github.com/tzfun/etcd-workbench/issues/108#issuecomment-3023031468)
