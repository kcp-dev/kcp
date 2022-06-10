# kcp REST access patterns

> Note: This document contains both present state and descriptions of possible future changes.

## Cluster Resources and APIs

### `/clusters/$cluster/apis/$group/$version/$resource`

* CRUD for a specific cluster, specific CRD
* Long-term, the only access pattern for regular users (non API providers)
* Variants:
    * Full
    * Partial metadata

### `/clusters/*/apis/$group/$version/$resource`

* List/watch across all clusters, specific CRD
* At least needed for core APIExport/APIBinding controllers to work
    * May eventually move all other kcp system CRDs to APIExport/APIBinding?
* Long-term:
    * Will never be allowed for any client outside of internal kcp code
    * Variants:
        * Full
        * Partial metadata?

### `/services/apiexport/$cluster/$apiexport/$identity/clusters/$cluster/apis/$group/$version/$resource`

* Access via virtual workspace for an exported CRD
* CRUD for a specific cluster, specific CRD, specific identity
* Variants:
    * Full
    * Partial metadata
* Virtual workspace creates a new request to /clusters/$cluster/apis/$group/$version/$resource:identity
    * CRD lister needs to resolve the identity - exact match

### `/services/apiexport/$cluster/$apiexport/$identity/clusters/*/apis/$group/$version/$resource`

* Access via virtual workspace
* List/watch across all clusters, specific CRD, specific identity
* Variants:
    * Full
    * Partial metadata
* Virtual workspace creates a new request to /clusters/*/apis/$group/$version/$resource:identity
    * CRD lister needs to resolve the identity - exact match

### `/services/syncer/$syncerID/clusters/$cluster/apis/$group/$version/$resource`

* Access via virtual workspace for a resource a syncer should see (transformed to location-specific view the syncer
  should have) for a given logical cluster
* CRUD for a specific cluster, specific CRD

## etcd Storage

### Exported CRD (proposed)

`/registry/$group/$resource/$identity/$cluster/[$namespace]/$name`

### Normal CRD (proposed)

`/registry/$group/$resource/customresources/$cluster/[$namespace]/$name`

### Normal CRD (current)

`/registry/$group/$resource/$cluster/[$namespace]/$name`

## Q & A

**Q:** If we have 2 workspaces A and B, and they each have a normal (non exported) widgets.acme.io CRD

1. Does a controller need to do a wildcard list/watch against all widget instances across workspaces, even though
   they’re from different CRDs?

**A:** A controller can either wildcard list/watch a CRD with identity though the APIExport virtual workspace, or a
single CRD by workspace (non-wildcard). Cross-workspace CRD list/watch is not possible, with the little exception for
meta-data only requests used by some system controllers.

2. Same question as above, but instead of a non exported CRD, what if there are 2 APIExports with unique identities,
   both exporting widgets
   **A:** Two APIExports with different identities are two different resources. Wildcard-list/watch across both only
   works for meta-data requests.

**Q:** What access pattern should the syncer use today?

**A:** Virtual workspaces.

**Q:** What wildcard access patterns do we allow for non exported CRDs, e.g. kcp system CRDs like WorkloadClusters? only
system CRDs, while trying to move some of them over to APIBindings.

**A:** System CRDs will be move to `APIExport` and `APIBinding` patterns.

**Q:** What does RBAC look like for virtual workspaces?

**A:** This will be implemnted by the virtual workspace itself allowing the virtual worksapce to apply custom
authorization.