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

package admin

import (
	"github.com/kcp-dev/logicalcluster/v3"
)

// GlobalAdminCluster is the logical cluster name under which installation-wide
// objects owned by the Admin workspace are stored in the cache server. It is
// not a real logical cluster on any shard: objects under it exist only in the
// cache, are written exclusively through /services/admin, and are read by
// every shard through its global (cache-backed) informers.
//
// It is deliberately NOT "system:admin", which is a real, shard-local logical
// cluster holding per-shard administrative objects such as leader-election
// leases.
var GlobalAdminCluster = logicalcluster.Name("system:global-admin")
