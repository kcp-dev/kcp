# Storage and stateful applications

## Overview

KCP provides a control plane that implements the concept of Transparent Multi Cluster (TMC) for compute, network, and storage. In order to give the illusion of transparent storage in KCP, it exposes the same Kubernetes APIs for storage (PVC/PV), so users and workloads do not need to be aware of the coordinations taken by the control plane behind the scenes.

Placement for storage in KCP uses the same [concepts used for compute](locations-and-scheduling.md#main-concepts): "`SyncTargets` in a `Location` are transparent to the user, and workloads should be able to seamlessly move from one `SyncTarget` to another within a `Location`, based on operational concerns of the compute service provider, like decommissioning a cluster, rebalancing capacity, or due to an outage of a cluster. It is the compute service's responsibility to ensure that for workloads in a location, to the user it looks like ONE cluster."

KCP will provide the basic controllers and coordination logic for moving volumes, as efficiently as possible, using the underlying storage topology and capabilities. It will use the workload clusters storage APIs to manage volumes, and not require direct access from the control plane to the storage itself. For more advanced or custom solutions, KCP will allow external coordinators to take over.

## Main concepts

- [Transparent multi-cluster](investigations/transparent-multi-cluster.md) - describes the TMC concepts.

- [Placement, Locations and Scheduling](locations-and-scheduling.md) - describes the KCP APIs and mechanisms used to control compute placement, which will be used for storage as well. Refer to the concepts of `SyncTarget`, `Location`, and `Placement`.

- [Kubernetes storage concepts](https://kubernetes.io/docs/concepts/storage/) - documentation of storage APIs in Kubernetes.

- [Persistent Volumes](https://kubernetes.io/docs/concepts/storage/persistent-volumes/) - PVCs are the main storage APIs used to request storage resources for applications. PVs are invisible to users, and used by administrators or privileged controllers to provision storage to user claims, and will be coordinated by KCP to support transparent multi-cluster storage.

- [Kubernetes CSI](https://kubernetes-csi.github.io/docs/) - The Container Storage Interface (CSI) is a standard for exposing arbitrary block and file storage systems to containerized workloads. The list of [drivers](https://kubernetes-csi.github.io/docs/drivers.html) provides a "menu" of storage systems integrated with kubernetes and their properties.

- [StatefulSets volumeClaimTemplates](https://kubernetes.io/docs/concepts/workloads/controllers/statefulset/#volume-claim-templates) - workload definition used to manage “sharded” stateful applications. Specifying `volumeClaimTemplates` in the statefulset spec will provide stable storage by creating a PVC per instance.

## Volume types

Each workload-cluster brings its own storage to multi-cluster environments, and in order to make efficient coordination decisions, KCP will identify the following types:

#### Shared network-volumes
These volumes are provisioned from an external storage system that is available to all/some of the clusters over an infrastructure network. These volumes are typically provided by a shared-filesystem (aka [NAS](https://en.wikipedia.org/wiki/Network-attached_storage)), with access-mode of ReadWriteMany (RWX) or ReadOnlyMany (ROX). A shared volume can be used by any pod from any cluster (that can reach it) at the same time. The application is responsible for the consistency of its data (for example with eventual consistency semantics, or stronger synchronization services like zookeeper). Examples of such storage are generic-NFS/SMB, AWS-EFS, Azure-File, GCP-Filestore, CephFS, GlusterFS, NetApp, GPFS, etc.

#### Owned network-volumes
These volumes are provisioned from an external storage system that is available to all/some of the clusters over an infrastructure network. However unlike shared volumes, owned volumes require that only a single node/pod will mount the volume at a time. These volumes are typically provided by a block-level storage system, with access-mode of ReadWriteOnce (RWO) or ReadWriteOncePod (RWOP). It is possible to *move* the ownership between clusters (that have access to that storage), by detaching from the current owner, and then attaching to the new owner. But it would have to guarantee a single owner to prevent data inconsistencies or corruptions, and even work if the owner cluster is offline (see forcing detach with “fencing” below). Examples of such storage are AWS-EBS, Azure-Disk, Ceph-RBD, etc.

#### Internal volumes
These volumes are provisioned inside the workload cluster itself, and rely on its internal resources (aka hyper-converged or software-defined storage). This means that the availability of the workload cluster also determines the availability of the volume. In some systems these volumes are bound to a single node in the cluster, because the storage is physically attached to a host. However, advanced clustered/distributed systems make efforts to overcome temporary and permanent node failures by adding data redundancy over multiple nodes. These volumes can have any type of access-mode (RWO/RWOP/RWX/ROX), but their strong dependency on the cluster itself is the key difference from network volumes. Examples of such storage are host-path/local-drives, TopoLVM, Ceph-rook, Portworx, OpenEBS, etc.

## Topology and locations

#### Regular topology
A regular storage topology is one where every `Location` is defined so that all of its clusters are connected to the same storage system. This makes it trivial to move network volumes transparently between clusters inside the same location.

#### Multi-zone cluster
A more complex topology is where every cluster contains nodes from several availability-zones, for the sake of being resilient to a zone failure. Since volumes are bound to a single zone (where they were provisioned), then a volume will not be able to move to a cluster without nodes on that zone. This is ok if all the clusters use the same set of zones, but if the zones are different, or the capacity per zone is too limited, copying to another zone might be necessary.

#### Internal volumes
Internal volumes are always confined to one cluster, which has to be copied outside of the cluster continuously to keep the application available even if the current cluster fails entirely (network split, region issue, etc). This is similar to how DR solutions work between locations.

#### Disaster recover between locations
A regular Disaster Recovery (DR) topology will create pairs of `Locations` so that one is “primary” and the other is “secondary” (sometimes this relation is mutual). For volumes to be able to move between these locations, their storage systems would need to be configured to mirror/replicate/backup/snapshot (whichever approach is more appropriate depends on the case) every volume to its secondary. With such a setup, KCP would need to be able to map between the volumes on the primary and the secondary, so that it could failover and move workloads to the secondary and reconnect to the last copied volume state. See more on the DR section below.

## Provisioning volumes

Volume provisioning in Kubernetes involves the CSI controllers and sidecar, as well as a custom storage driver. It reconciles PVCs by dynamically creating a PV for a PVC, and binding them together. This process depends on the CSI driver to be running on the cluster’s compute resources, and would not be able to run on the control plane. Instead, KCP will pick a designated cluster for the workload placement, which will include the storage claims (PVCs), and the CSI driver on the cluster will perform the storage provisioning.

In order to support changing workload placement overtime, even if the provisioning cluster is offline, KCP will have to retrieve the volume information from the cluster, and keep it in the control plane for future coordination. The volume information inside the PV is expected to be transferable between clusters that connect to the same storage system and drivers, although some transformations would be required.

To retrieve the volume information and maintain it in KCP, a special sync state is required that will sync **UP** the PV from the cluster to KCP. This state is referred to as `Upsync` - see [Resource Upsyncing](locations-and-scheduling.md#resource-upsyncing).

The provisioning flow includes: (A) PVC synced to workload cluster, (B) CSI provisioning on the cluster, (C) Syncer detects PVC binding and initiates PV Upsync. Transformations would be applied in KCP virtual workspace to make sure that the PVC and PV would appear bound in KCP, similar to how it is on the cluster. Once provisioning itself is complete, coordination logic will decide how to proceed - for shared volumes it will switch to a normal sync mode to allow multiple clusters to share the same volume, but for owned volumes it will remain in Upsync state until decided to be detached.

## Moving shared volumes

Shared volume can easily move to clusters in the same location by syncing the PVC and PV together, so they bind only to each other on the cluster. Syncing will transform their mutual references so that the `PVC.volumeName = PV.name` and `PV.claimRef = { PVC.name, PVC.namespace }` are set appropriately for the target cluster, since the downstream `PVC.namespace` and `PV.name` will not be the same as upstream.

Deletion of shared volumes does require some more coordination. The reclaimPolicy would be transformed to always `Retain`, to avoid unintended deletion of the volume by one of the clusters while others use it. See more in the section on deleting volumes.

## Moving owned volumes

> **TBD** - this section is a work in progress...

#### Detach from owner
Owned volumes require that a single cluster will use them at any given time. Keeping the volume in `Upsync` state, will be used to lock the volume to the sync target, and no other cluster will be able to sync it at that time. As placement changes due to maintenance or cluster failure, an ownership-move flow would be coordinated from KCP. This flow will require the cluster to acknowledge that it successfully detached from the volume, and only then KCP will sync the volume to a new target and finally set it to Upsync. 

#### Forcing detach with fencing
However in case the owner cluster is not able to acknowledge that it detached from the volume, a forced-detach flow will be initiated, in which the storage has to support a CSI extension for network fencing, effectively blocking an entire cluster from accessing the storage until fencing is removed. Once the cluster recovers and can acknowledge that it detached from the moved volumes, fencing will be removed from the storage and that cluster can recover the rest of its workloads. 

- [kubernetes-csi-addons](https://github.com/csi-addons/kubernetes-csi-addons)
- [NetworkFence](https://github.com/csi-addons/kubernetes-csi-addons/blob/main/docs/networkfence.md) (currently implemented only by ceph-csi).

## Storage classes

> **TBD** - this section is a work in progress...

Storage classes can be thought of as templates to PVs, which allow clusters to support multiple storage providers, or configure different policies for the same provider. Just like PVs are invisible to users, so do storage classes. However, users may choose a storage class by name when specifying their PVCs. When the storage class field is left unspecified, which is common, the cluster will use a default storage class, which is one labeled class that applies to all users and all namespaces, which makes it a bit difficult to be used in multi-tenant clusters.

Matching storage classes between clusters in the same location would be a simple way to ensure that storage can be moved transparently. However KCP should be able to verify that matching to warn when this is not the case, and prevent future problems.

#### Open questions
- How to match classes and make sure the same storage system is used in the location?
- How to support multiple classes per cluster (eg. RWO + RWX)?
- A separate sync target per class?
- A separate default class per workspace/namespace?

## Deleting volumes

> **TBD** - this section is a work in progress...

[persistent-volumes reclaiming](https://kubernetes.io/docs/concepts/storage/persistent-volumes/#reclaiming) allows volumes to be configured how to behave when they are reclaimed. By default, storage classes will apply a `reclaimPolicy: Delete` to dynamically provisioned PVs unless explicitly specified to `Retain`. This means that volumes there were provisioned, will also get de-provisioned and their storage will be deleted. However, admins can modify the class to Retain volumes, and invoke cleanup on their own schedule.

For owned volumes, the cluster that owns the volume should also be responsible for deleting it. The exception to that is that detaching a volume should change the volume reclaim policy to retain to avoid deletion when removing the workload.

For shared volumes, KCP will transform synced PVs and override it to `Retain`, to avoid deletion in case the workload is removed from any one of the sync targets. For actual deletion of shared volumes, one cluster would be designated to de-provision, which would work like an owned volume.

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
- NFS server running in every location, external to the workload clusters, but available over the network.
- Note that high-availability and data-protection of the storage itself is out of scope and would be handled by storage admin or provided by enterprise products.
- Workloads allow volumes with RWX access-mode.
- KCP picks one cluster to be the provisioner and syncs up the volume information.
- After provisioning sync down to any cluster in the location that the workload decides to be placed to allow moving transparently as needed when clusters become offline or drained.
- Once the PVC is deleted, the deletion of the volume itself is performed by one of the clusters.

## Roadmap

- Moving owned volumes
- Fencing
- Copy-on-demand
- Copy-continuous
- DR-location-pairing and primary->secondary volume mapping
- Statefulsets
- COSI Bucket + BucketAccess
