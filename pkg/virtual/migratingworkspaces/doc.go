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

// Package migratingworkspaces provides a virtual workspace that allows
// other shards to bypass the front-proxy to pull data of a logical
// cluster to be migrated directly from the origin shard.
// It only allows system:kcp:external-logical-cluster-admin access to
// the LogicalClusterDump in the migration.kcp.io API group.
package migratingworkspaces

import "path"

const VirtualWorkspaceName string = "migratingworkspaces"

// HandlerPath is the URL path that the dump handler responds to.
const HandlerPath = "/apis/migration.kcp.io/v1alpha1/logicalclusterdumps"

// URLFor returns the absolute path prefix for the migrating workspaces VW.
func URLFor() string {
	return path.Join("/services", VirtualWorkspaceName)
}
