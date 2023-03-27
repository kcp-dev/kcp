# Document the KCP Workspace objects

Document key KCP objects, with they Yaml, that are created and modified during the key steps of MC Syncer deployment:

1. Creation of a KCP Workspace (`k ws create`)
2. Creation of a KCP Syncer (`k kcp workload sync`)
3. Deployment of the Syncer in the edge pcluster (`k apply -f`)
4. Binding of the Syncer resources (`k kcp bind compute`)

Two explanatory scenarios are considered:

1. [A pcluster with compatible resource schemas, such as the Kind cluster](kind-scenario/README.md) example discussed in the [quickstart](https://docs.kcp.io/kcp/main/)
2. [A pcluster with incompatible resource schemas, as the MicroShift cluster](jn-scenario/README.md) scenario discussed in [issue #2885](https://github.com/kcp-dev/kcp/issues/2885)
