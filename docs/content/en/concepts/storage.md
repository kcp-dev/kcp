# Storage and stateful applications

## Overview

KCP provides a control plane that implements the concept of Transparent Multi Cluster (TMC) for compute, network, and storage. In order to give the illusion of transparent storage in KCP, it exposes the same Kubernetes APIs for storage (PVC/PV), so users and workloads do not need to be aware of the coordinations taken by the control plane behind the scenes.

Placement for storage in KCP uses the same [concepts used for compute](locations-and-scheduling.md#main-concepts): "`SyncTargets` in a `Location` are transparent to the user, and workloads should be able to seamlessly move from one `SyncTarget` to another within a `Location`, based on operational concerns of the compute service provider, like decommissioning a cluster, rebalancing capacity, or due to an outage of a cluster. It is the compute service's responsibility to ensure that for workloads in a location, to the user it looks like ONE cluster."

KCP will provide the basic controllers and coordination logic for moving volumes, as efficiently as possible, using the underlying storage topology and capabilities. It will use the `SyncTargets` storage APIs to manage volumes, and not require direct access from the control plane to the storage itself. For more advanced or custom solutions, KCP will allow external coordinators to take over.

## Main concepts

- [Transparent multi-cluster](../investigations/transparent-multi-cluster.md) - describes the TMC concepts.

- [Placement, Locations and Scheduling](locations-and-scheduling.md) - describes the KCP APIs and mechanisms used to control compute placement, which will be used for storage as well. Refer to the concepts of `SyncTarget`, `Location`, and `Placement`.

- [Kubernetes storage concepts](https://kubernetes.io/docs/concepts/storage/) - documentation of storage APIs in Kubernetes.

- [Persistent Volumes](https://kubernetes.io/docs/concepts/storage/persistent-volumes/) - PVCs are the main storage APIs used to request storage resources for applications. PVs are invisible to users, and used by administrators or privileged controllers to provision storage to user claims, and will be coordinated by KCP to support transparent multi-cluster storage.

- [Kubernetes CSI](https://kubernetes-csi.github.io/docs/) - The Container Storage Interface (CSI) is a standard for exposing arbitrary block and file storage systems to containerized workloads. The list of [drivers](https://kubernetes-csi.github.io/docs/drivers.html) provides a "menu" of storage systems integrated with kubernetes and their properties.

- [StatefulSets volumeClaimTemplates](https://kubernetes.io/docs/concepts/workloads/controllers/statefulset/#volume-claim-templates) - workload definition used to manage “sharded” stateful applications. Specifying `volumeClaimTemplates` in the statefulset spec will provide stable storage by creating a PVC per instance.

## Volume types

Each physical-cluster (aka "pcluster") brings its own storage to multi-cluster environments, and in order to make efficient coordination decisions, KCP will identify the following types:

#### Shared network-volumes
These volumes are provisioned from an external storage system that is available to all/some of the pclusters over an infrastructure network. These volumes are typically provided by a shared-filesystem (aka [NAS](https://en.wikipedia.org/wiki/Network-attached_storage)), with access-mode of ReadWriteMany (RWX) or ReadOnlyMany (ROX). A shared volume can be used by any pod from any pcluster (that can reach it) at the same time. The application is responsible for the consistency of its data (for example with eventual consistency semantics, or stronger synchronization services like zookeeper). Examples of such storage are generic-NFS/SMB, AWS-EFS, Azure-File, GCP-Filestore, CephFS, GlusterFS, NetApp, GPFS, etc.

#### Owned network-volumes
These volumes are provisioned from an external storage system that is available to all/some of the pclusters over an infrastructure network. However unlike shared volumes, owned volumes require that only a single node/pod will mount the volume at a time. These volumes are typically provided by a block-level storage system, with access-mode of ReadWriteOnce (RWO) or ReadWriteOncePod (RWOP). It is possible to *move* the ownership between pclusters (that have access to that storage), by detaching from the current owner, and then attaching to the new owner. But it would have to guarantee a single owner to prevent data inconsistencies or corruptions, and even work if the owner pcluster is offline (see forcing detach with “fencing” below). Examples of such storage are AWS-EBS, Azure-Disk, Ceph-RBD, etc.

#### Internal volumes
These volumes are provisioned inside the pcluster itself, and rely on its internal resources (aka hyper-converged or software-defined storage). This means that the availability of the pcluster also determines the availability of the volume. In some systems these volumes are bound to a single node in the pcluster, because the storage is physically attached to a host. However, advanced clustered/distributed systems make efforts to overcome temporary and permanent node failures by adding data redundancy over multiple nodes. These volumes can have any type of access-mode (RWO/RWOP/RWX/ROX), but their strong dependency on the pcluster itself is the key difference from network volumes. Examples of such storage are host-path/local-drives, TopoLVM, Ceph-rook, Portworx, OpenEBS, etc.

## Topology and locations

#### Regular topology
A regular storage topology is one where every `Location` is defined so that all of its `SyncTargets` are connected to the same storage system. This makes it trivial to move network volumes transparently between `SyncTargets` inside the same location.

#### Multi-zone cluster
A more complex topology is where pclusters contain nodes from several availability-zones, for the sake of being resilient to a zone failure. Since volumes are bound to a single zone (where they were provisioned), then a volume will not be able to move between `SyncTargets` without nodes on that zone. This is ok if all the `SyncTargets` of the `Location` span over the same set of zones, but if the zones are different, or the capacity per zone is too limited, copying to another zone might be necessary.

#### Internal volumes
Internal volumes are always confined to one pcluster, which means it has to be copied outside of the pcluster continuously to keep the application available even in the case where the pcluster fails entirely (network split, region issue, etc). This is similar to how DR solutions work between locations.

#### Disaster recover between locations
A regular Disaster Recovery (DR) topology will create pairs of `Locations` so that one is “primary” and the other is “secondary” (sometimes this relation is mutual). For volumes to be able to move between these locations, their storage systems would need to be configured to mirror/replicate/backup/snapshot (whichever approach is more appropriate depends on the case) every volume to its secondary. With such a setup, KCP would need to be able to map between the volumes on the primary and the secondary, so that it could failover and move workloads to the secondary and reconnect to the last copied volume state. See more on the DR section below.

## Provisioning volumes

Volume provisioning in Kubernetes involves the CSI controllers and sidecar, as well as a custom storage driver. It reconciles PVCs by dynamically creating a PV for a PVC, and binding them together. This process depends on the CSI driver to be running on the `SyncTarget` compute resources, and would not be able to run on KCP workspaces. Instead, KCP will pick a designated `SyncTarget` for the workload placement, which will include the storage claims (PVCs), and the CSI driver on the `SyncTarget` will perform the storage provisioning.

In order to support changing workload placement overtime, even if the provisioning `SyncTarget` is offline, KCP will have to retrieve the volume information from that `SyncTarget`, and keep it in the KCP workspace for future coordination. The volume information inside the PV is expected to be transferable between `SyncTargets` that connect to the same storage system and drivers, although some transformations would be required.

To retrieve the volume information and maintain it in KCP, a special sync state is required that will sync **UP** the PV from a `SyncTarget` to KCP. This state is referred to as `Upsync` - see [Resource Upsyncing](locations-and-scheduling.md#resource-upsyncing).

The provisioning flow includes: (A) PVC synced to `SyncTarget`, (B) CSI provisioning on the pcluster, (C) Syncer detects PVC binding and initiates PV `Upsync`. Transformations would be applied in KCP virtual workspace to make sure that the PVC and PV would appear bound in KCP, similar to how it is in a single cluster. Once provisioning itself is complete, coordination logic will switch to a normal `Sync` state, to allow multiple `SyncTargets` to share the same volume, and for owned volumes to move ownership to another `SyncTarget`.

## Moving shared volumes

Shared volume can easily move to any `SyncTarget` in the same `Location` by syncing the PVC and PV together, so they bind only to each other on the pcluster. Syncing will transform their mutual references so that the `PVC.volumeName = PV.name` and `PV.claimRef = { PVC.name, PVC.namespace }` are set appropriately for the `SyncTarget`, since the downstream `PVC.namespace` and `PV.name` will not be the same as upstream.

Moving volumes will set the volume's `reclaimPolicy` to always `Retain`, to avoid unintended deletion by any one of the `SyncTargets` while others use it. Once deletion of the upstream PVC is initiated, the coordination controller will transform the `reclaimPolicy` to `Delete` for one of the `SyncTargets`. See more in the section on deleting volumes.

## Moving owned volumes

> **TBD** - this section is a work in progress...

#### Detach from owner
Owned volumes require that *at most one* pcluster can use them at any given time. As placement changes, the coordination controller is responsible to serialize the state changes of the volume to move the ownership of the volume safely. First, it will detach the volume from the current owner, and wait for it to acknowledge that it successfully removed it, and only then will sync the volume to a new target. 

#### Forcing detach with fencing
However, in case the owner is not able to acknowledge that it detached the volume, a forced-detach flow might be possible. The storage system has to support a CSI extension for network fencing, effectively blocking an entire pcluster from accessing the storage until fencing is removed. Once the failed pcluster recovers, and can acknowledge that it detached from the moved volumes, fencing will be removed from the storage and that pcluster can recover the rest of its workloads. 

- [kubernetes-csi-addons](https://github.com/csi-addons/kubernetes-csi-addons)
- [NetworkFence](https://github.com/csi-addons/kubernetes-csi-addons/blob/main/docs/networkfence.md) (currently implemented only by ceph-csi).

## Storage classes

> **TBD** - this section is a work in progress...

Storage classes can be thought of as templates to PVs, which allow pclusters to support multiple storage providers, or configure different policies for the same provider. Just like PVs are invisible to users, so do storage classes. However, users may choose a storage class by name when specifying their PVCs. When the storage class field is left unspecified (which is common), the pcluster will use its default storage class. However, the default storage class is a bit limited for multi-tenancy because it is one class per the entire pcluster.

Matching storage classes between `SyncTargets` in the same `Location` would be a simple way to ensure that storage can be moved transparently. However KCP should be able to verify the storage classes match across the `Location` and warn when this is not the case, to prevent future issues.

#### Open questions
- How to match classes and make sure the same storage system is used in the location?
- How to support multiple classes per pcluster (eg. RWO + RWX)?
- Maybe a separate `SyncTarget` per class?
- Can we have a separate default class per workspace?

## Deleting volumes

> **TBD** - this section is a work in progress...

[Persistent-volumes reclaiming](https://kubernetes.io/docs/concepts/storage/persistent-volumes/#reclaiming) allows volumes to be configured how to behave when they are reclaimed. By default, storage classes will apply a `reclaimPolicy: Delete` to dynamically provisioned PVs unless explicitly specified to `Retain`. This means that volumes there were provisioned, will also get de-provisioned and their storage will be deleted. However, admins can modify the class to `Retain` volumes, and invoke cleanup on their own schedule.

While moving volumes, either shared or owned, the volume's `reclaimPolicy` will be set to `Retain` to prevent any `SyncTarget` from releasing the volume storage on scheduling changes.

Once the PVC is marked for deletion on KCP, the coordination controller will first pick one `SyncTarget` as owner (or use the current owner for owned volumes) and make sure to remove all sharers, and wait for their sync state to be cleared. Then it will set the owner's volume `reclaimPolicy` to `Delete` so that it will release the volume storage.

Setting a PV to `Retain` on KCP itself should also be respected by the controllers and allow manual cleanup of the volume in KCP, instead of automatically with the PVC.

## Copying volumes

> **TBD** - this section is a work in progress...

- [ramen](https://github.com/RamenDR/ramen)
- [volume-replication-operator](https://github.com/csi-addons/volume-replication-operator)
- [volsync](https://github.com/backube/volsync)

## Disaster recovery

> **TBD** - this section is a work in progress...

- Pairing locations as continuously replicating storage between each other.
- KCP would have to be able to map primary volumes to secondary volumes to failover workloads between locations.

## Examples

> **TBD** - this section is a work in progress...

#### Shared NFS storage
- NFS server running in every location, external to the `SyncTarget`, but available over the network.
- Note that high-availability and data-protection of the storage itself is out of scope and would be handled by storage admin or provided by enterprise products.
- Workloads allow volumes with RWX access-mode.
- KCP picks one `SyncTarget` to be the provisioner and syncs up the volume information.
- After provisioning completes, sync down to any `SyncTarget` in the `Location` that the workload decides to be placed to allow moving transparently as needed when clusters become offline or drained.
- Once the PVC is deleted, the deletion of the volume itself is performed by one of the `SyncTargets`.

## Roadmap

- Moving owned volumes
- Fencing
- Copy-on-demand
- Copy-continuous
- DR-location-pairing and primary->secondary volume mapping
- Statefulsets
- COSI Bucket + BucketAccess
