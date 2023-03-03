---
description: >
  How to use the cluster mapper.
---

# Cluster Mapper

## Invariants

1. Every object assigned by a location exists on the target location
2. Every object in the target location has appropriate metadata set indicating source
3. Every object in the target location that has status and the appropriate policy choice set will reflect that status back in the source object
4. No object exists in the target location that is not assigned by a source object(s)

`1..N` mappers per location (sharding by virtual cluster?)
mappers see only the objects assigned to them (special API)

- can load policy info during mapping?
- can load policy info from side channels like regular resources
wants to watch many different resources at once and deal with them as unstructured

assumption: all resources in the location are compatible with the target API (managed by control plane and CRD folding), and if that is broken the mapper is instructed to halt mapping

- how to deal with partial mapping when one object is broken
- how does CRD folding actually work (separate doc)

assumption: higher level control (admission) manages location accessibility

assumption:  kcp has 1k virtual clusters with 50k resources, a given mapper may see 1k to 50k resources

- fully syncing will take `50k / default throttle` (50-100 req/s) ~ 1000s in serial
- order may be important
- there may be 100-1k locations, so we may have up to 1/1000 cardinality (implies indexing)
- mappers that favor summarization objects (deployments) have scale advantages over those that don't (pods)

Assumption: we prefer not to require order to correctly map, but some order is implicit due to resource version ordering

## Basic sync loop

1. retrieve all objects assigned to a particular cluster (metadata.annotations["kcp.io/assigned-locations"] = ["a","b"])
2. transform them into one or more objects for the destination
   - add labels?
   - map namespace from source to target
   - hide certain annotations (assigned-locations?)
   - set and maintain other annotations (like the source namespace / virtual cluster)
   - set a controller ref?
3. perform a merge into the destination object (overwrite of spec)
4. sync some fields (status?) back to source object
5. delete all objects no longer assigned to the remote location
   - read all mappable objects from all mappable resources?
     - use `kcp.io/location=X` label to filter
   - detect when an object policy on mapping is changed?
   - only need the partial object (metadata)
   - can we leverage the garbage collector to delete the object?
