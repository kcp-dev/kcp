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

// Package admin provides the Admin workspace: a virtual workspace served at
// /services/admin on every shard, exposing installation-wide administrative
// views. Admins reach it via the reserved pseudo-workspace path :admin
// (kubectl ws use :admin).
//
// The first (and currently only) resource is the aggregated, read-only view
// of every shard's Shard object. The view is backed by the cache server
// (where every shard's Shard object is already replicated), read with
// shard-wildcard scope. Because all shards proxy the same cache server,
// every shard serves an identical view in a single resourceVersion space;
// consumers can fail over between shards and resume watches with the same
// resourceVersion.
//
// This decouples shard discovery and shard administration from the root
// shard: the front-proxy and admin tooling consume this view instead of the
// Shard objects in the root workspace. Over time the Admin workspace is the
// home for further installation-wide admin surfaces (e.g. runtime knobs or
// debug views) without inventing new endpoints per resource.
package admin

// VirtualWorkspaceName is the name of the Admin workspace virtual workspace.
// It is also the URL path segment under /services/.
const VirtualWorkspaceName = "admin"
