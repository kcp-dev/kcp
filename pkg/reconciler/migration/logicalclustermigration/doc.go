/*
Copyright 2026 The kcp Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package logicalclustermigration implements migrating logical clusters between shards.
//
// The migration process is roughly:
//
// - LogicalClusterMigration is created by an admin anywhere
// - LogicalClusterMigration is propagated to the cache server
//
// After the propagation the origin and destination shards see the
// LCMigration through their cache server informer and can react to
// changes on it, while making changes by using the client going through
// the front-proxy.
//
// Origin shard (reconcilePreparing):
//
//   - Sets itself in the .status.originShard
//   - Annotates the LC to prevent access
//   - Configures informers to ignore objects related to the LC
//   - Cancels all active connections to the LC
//   - Purges all objects in informers related to the LC
//
// Destination shard (reconcileMigrating):
//
//   - Configures informers to ignore objects related to the LC
//   - Copies over all objects from the origin shard and writes them
//     directly to the etcd
//
// Origin shard (reconcileOriginCleanup):
//
//   - Purges all objects, including the LC from etcd
//
// Destination shard (reconcileDestinationFinalize):
//
//   - Updates the LC to remove the annotation
//   - Resyncs all informers so they pick up the LC's objects
//
// The front-proxy automatically bars access to the LC and routes
// requests to the new shard by discovering the objects on the shards
// directly.
package logicalclustermigration
