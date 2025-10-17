# VirtualResources Example

This example shows usage of VirtualResources, together with CachedResources.
The goal of VirtualResources is to distribute static, read-only resources to multiple clusters
in a scalable way.

## Setup

1. Start kcp with sharded setup:

   ```bash
   make test-run-sharded-server
   ```

2. Create a provider workspace, where we will create the resources to be distributed:

   ```bash
   export KUBECONFIG=.kcp/admin.kubeconfig
   kubectl ws create provider --enter

   kubectl create -f config/examples/virtualresources/crd-instances.yaml
   # this this to work we always require apiresource schema to be present
   kubectl create -f config/examples/virtualresources/apiresourceschema-instances.yaml
   kubectl create -f config/examples/virtualresources/instances.yaml

   # create caching for the resources
   kubectl create -f config/examples/virtualresources/cached-resource-instances.yaml
   ```

3. Create a an APIResourceSchema for actual virtual machines to be distributed,
   which will be using instance types. 

   ```bash
    kubectl create -f config/examples/virtualresources/apiresourceschema-virtualmachine.yaml
   ```

4. Create an APIExport for the virtual machines:

    ```bash
    kubectl create -f config/examples/virtualresources/apiexport.yaml
   ```

5. Create a consumer workspace, where we will consume the virtual machines:

    ```bash
    kubectl ws use :root
    kubectl ws create consumer --enter
    kubectl kcp bind apiexport root:provider:virtualmachines virtualmachines
   ```

6. Now check if we can see instances in the consumer workspace:

   ```bash
   kubectl get instances.machines.svm.io
   ```