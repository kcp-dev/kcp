---
description: >
  Tips and notes for running a production-grade kcp setup.
---

# Production Setup

This document collects notes and tips on how to run a production-grade kcp setup.

## Overview

Running kcp consists of mainly two challenges:

* Running reliable **etcd** clusters for each kcp shard.
* Running **kcp** and dealing with its **sharding** to distribute load and limit the impact of
  downtimes to a subset of the entire kcp setup.

## Running etcd

Just like Kubernetes, kcp uses [etcd](https://etcd.io/) as its database: each (root)shard uses its own
etcd cluster.

The etcd documentation already contains a great number of [operations guides](https://etcd.io/docs/v3.7/op-guide/)
for common operations like performing backups, monitoring the health etc. Administrators should
familiarize themselves with the practices laid out there.

### Kubernetes

When running etcd inside Kubernetes, an operator can greatly help in running etcd.
[Etcd Druid](https://gardener.github.io/etcd-druid/) is one of them and offers great support for
operations tasks and the entire etcd lifecycle. Etcd clusters managed by Etcd Druid can be seamlessly
used with kcp.

### TLS

It is highly recommended to enable TLS in etcd to encrypt traffic in-transtit between kcp and etcd.
When using Kubernetes, [cert-manager](https://cert-manager.io/) is a great choice for managing CAs
and certificates in your cluster, and it can also provide certificates for use in etcd.

On the kcp side, all that is required is to configure three CLI flags:

* `--etcd-certfile`
* `--etcd-keyfile`
* `--etcd-cafile`

When using cert-manager, all three files are available in the Secret that is created for the
Certificate object.

When using Etcd Druid you have to manually create the necessary certificates or make use of one of
the community Helm charts like [hajowieland/etcd-druid-certs](https://artifacthub.io/packages/helm/hajowieland/etcd-druid-certs).

### Backups

As with any database, etcd clusters should be backed up regularly. This is especially important with
etcd because a permanent quorum loss can make the entire database unavailable, even though the data
is technically in some form still there.

Using an operator like the aforementioned Etcd Druid can greatly help in performing backups and
restores.

### Encryption

kcp supports encryption-at-rest for its storage backend, allowing administrators to configure
encryption keys or integration with external key-management systems to encrypt data written to disk.

Please refer to the [Kubernetes documentation](https://kubernetes.io/docs/tasks/administer-cluster/encrypt-data/)
for more information on configuring and using encryption in kcp.

Since each shard and its etcd is independent from other shards, the encryption configuration can be
different per shard, if desired.

### Scaling

etcd can be scaled to some degree by adding more resources and/or more members to an etcd cluster,
however [hard limits](https://etcd.io/docs/v3.7/dev-guide/limit/) set an upper boundary. It is
important to monitor etcd performance to assign resources accordingly.

Note that using scaling solutions like the Vertical Pod Autoscaler (VPA), care must be taken so that
not too many etcd members restart simultaneously or a permanent loss of quorum can occur, which would
require restoring etcd from a backup.

## Running kcp

Kubernetes is the native habitat of kcp and its recommended runtime environment. The kcp project
offers two ways of running kcp in Kubernetes:

* via [Helm chart](https://github.com/kcp-dev/helm-charts/)
* using the [kcp-operator](https://docs.kcp.io/kcp-operator/)

While still in its early stages, the kcp-operator is aimed to be the recommended approach to running
kcp: it offers more features than the Helm charts and can actively reconcile missing/changed
resources on its own.

### Sharding

kcp supports the concept of sharding to spread the workload horizontally across kcp processes. Even
if the database behind kcp would offer infinite performance at zero cost, kcp itself cannot scale
vertically indefinitely: each logical cluster requires a minimum of runtime resources, even if the
cluster is not actively used.

New workspaces in kcp are spread evenly across all available shards, however as of kcp 0.28, this
does not take into account the current number of logicalclusters on each shard. This means once
every existing shard has reached its administrator-defined limit, simply adding a new shard will not
make kcp schedule all new clusters onto it, but still distribute them evenly. There is currently
no mechanism to mark shards as "full" or unavailable for schedulding and the kcp scheduled does not
take shard metrics into account.

It's therefore recommended to start with a sharded setup instead of working with a single root shard
only. This not only improves realiability and performance, but can also help ensure newly developed
kcp client software does not by accident make false assumptions about sharding.

### Backups

All kcp data is stored in etcd, there is no need to perform a dedicated kcp backup.
