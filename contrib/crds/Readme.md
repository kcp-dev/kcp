## CRDs for basic legacy schema resources

The folder contains CRDs for useful legacy scheme resources (`Pod`s, `Deployment`s) to be added in the KCP control plane.
This is mainly to be able to start using KCP with meaningful resources, even before having implemented:
- the concept of a physical cluster registered to the KCP
- the import of underlying physical cluster APIResources as CRDs in KCP.

## Generation source

These CRDs have been generated from the related Kube APIs through the `kubebuilder` `controller-gen` tool.
However, before the generation, the types had to be fixed in order to:
- Add required `kubebuilder` annotation such as `groupName`, sub-resource-related annotations, ...
- `listType` and `listMapKeys` annotations wherever `patchStrategy` and `patchergeKey` was used, in order not to loose this precious information, since `patchStrategy` and `patchMergeKey` vendor extensions do not exist in CRD OpenAPI schema.

The following PR against the KCP `kubernetes` repository: https://github.com/kcp-dev/kubernetes/pull/2 contains the changes made to the GO types to enable the generation of valid CRDs that can be successfully used, even with the Strategic Merge Patch support for CRDs, added in commit https://github.com/kcp-dev/kubernetes/commit/33131378ff6e98ef3f5fdcf39fe40b8ed20da47b of the KCP `kubernetes` repository

## How to rebuild the CRDs

You can find the steps used to build those CRDs inside the `generate-crds.sh` script.

You can also simply run this script from this folder:

```
> ./generate-crds .
Checking the presence of 'controller-gen'
Cloning Kubernetes 'crd-compatible-core-and-apps-types' branch into .../go/src/github.com/kcp-dev/kcp/contrib/crds/crd-build
Generating core/v1 CRDs
Removing unnecessary core/v1 resources
Adding the 'core' group as a suffix in the name of core/v1 CRDs 
Generating apps/v1 CRDs
```
