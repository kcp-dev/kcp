# Load Test Setup

This folder contains the deployment of all components for the kcp loadtest into your setup. Please
refer to the architecture diagram from the load-testing top-level Readme or have a look at the `manifests/kind` example.
to see a list of all required pools. Each pool should be equipped with 3 nodes each.

With a `KUBECONFIG` pointed to your cluster, you can directly run the setup script, it will prompt,
you for any required variables:

```bash
./setup
```
