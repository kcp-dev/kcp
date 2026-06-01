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

// Package shardpaths declares the set of HTTP paths that are served by a shard
// (or the cache server) as shard-wide resources and must not be reachable via a
// workspace-scoped URL such as /clusters/<ws>/<path> or
// /services/cache/shards/<sh>/clusters/<ws>/<path>.
package shardpaths

import "k8s.io/apimachinery/pkg/util/sets"

// Paths enumerates the URL paths that are shard-level only. The filters in
// pkg/server/filters and pkg/cache/server reject any workspace-scoped request
// targeting one of these paths, and scope a top-level request to the root
// workspace for RBAC evaluation.
//
// The set includes /metrics and the standard kube health probes
// (/livez, /readyz, /healthz). All of these expose process-level state for
// the running shard or cache server with no per-workspace or per-shard
// meaning, so the workspace-scoped forms (/clusters/<ws>/livez,
// /services/cache/shards/<sh>/livez, ...) must not resolve.
var Paths = sets.New(
	"/metrics",
	"/livez",
	"/readyz",
	"/healthz",
)
