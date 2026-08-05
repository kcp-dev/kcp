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

package etcd

import (
	"strings"

	"github.com/kcp-dev/logicalcluster/v3"
)

// ScanPageSize is the page size used in etcd range scans.
const ScanPageSize int64 = 1000

// DumpMaxBytes is the default cap on the total size of entry values
// returned in a single LogicalClusterDump page, used when the request
// doesn't specify spec.maxBytes.
const DumpMaxBytes int64 = 2 * 1024 * 1024

// KeyParts holds the parsed components of an etcd key produced by SplitKey.
type KeyParts struct {
	Group    string
	Resource string
	// Segment is "customresources", an identity hash, or "" for built-in resources.
	Segment string
	Cluster logicalcluster.Name
	// Rest is everything after the cluster segment: [<namespace>/]<name>.
	Rest string
}

// ClusterPrefix reconstructs the etcd key prefix that scopes.
func (p KeyParts) ClusterPrefix(storagePrefix string) string {
	if p.Segment == "" {
		return storagePrefix + p.Group + "/" + p.Resource + "/" + string(p.Cluster)
	}
	return storagePrefix + p.Group + "/" + p.Resource + "/" + p.Segment + "/" + string(p.Cluster)
}

// SplitKey parses an etcd key into its structural components.
// true is only returned when the key was parsed correctly into KeyParts, not if KeyParts matches the lc.
func SplitKey(prefix, key string, lc logicalcluster.Name) (KeyParts, bool) {
	if !strings.HasPrefix(key, prefix) {
		return KeyParts{}, false
	}
	rest := strings.TrimPrefix(key, prefix)
	rest = strings.TrimPrefix(rest, "/")

	parts := strings.SplitN(rest, "/", 6)

	// key too short
	if len(parts) < 3 {
		return KeyParts{}, false
	}
	ret := KeyParts{
		Group:    parts[0],
		Resource: parts[1],
	}

	if parts[2] == "customresources" {
		// group/resource/"customresources"/cluster/namespace/name
		// group/resource/"customresources"/cluster/name
		ret.Segment = parts[2]
		ret.Cluster = logicalcluster.Name(parts[3])
		ret.Rest = strings.Join(parts[4:], "/")
		return ret, true
	}

	if len(parts) == 6 {
		// group/resource/identity/cluster/namespace/name
		ret.Segment = parts[2]
		ret.Cluster = logicalcluster.Name(parts[3])
		ret.Rest = strings.Join(parts[4:], "/")
		return ret, true
	}

	if len(parts) == 5 {
		// group/resource/identity/cluster/name
		// group/resource/cluster/namespace/name
		if parts[2] == lc.String() {
			// group/resource/cluster/namespace/name
			ret.Cluster = logicalcluster.Name(parts[2])
			ret.Rest = strings.Join(parts[3:], "/")
			return ret, true
		}
		// group/resource/identity/cluster/name
		ret.Segment = parts[2]
		ret.Cluster = logicalcluster.Name(parts[3])
		ret.Rest = strings.Join(parts[4:], "/")
		return ret, true
	}

	// group/resource/cluster/namespace/name
	// group/resource/cluster/name
	ret.Cluster = logicalcluster.Name(parts[2])
	ret.Rest = strings.Join(parts[3:], "/")
	return ret, true
}

// ClusterOf returns the logical cluster a storage key belongs to. Unlike
// SplitKey it does not require a target logical cluster: the ambiguous
// 5-segment case (group/resource/cluster/namespace/name for built-in
// namespaced resources vs. group/resource/identity/cluster/name for
// identity-based cluster-scoped resources) is resolved via isCluster, which
// reports whether the given segment is a known logical cluster name.
func ClusterOf(prefix, key string, isCluster func(string) bool) (logicalcluster.Name, bool) {
	if !strings.HasPrefix(key, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(key, prefix)
	rest = strings.TrimPrefix(rest, "/")

	parts := strings.SplitN(rest, "/", 6)

	// key too short
	if len(parts) < 3 {
		return "", false
	}

	if parts[2] == "customresources" || len(parts) == 6 {
		// group/resource/"customresources"/cluster/[namespace/]name
		// group/resource/identity/cluster/namespace/name
		if len(parts) < 4 {
			return "", false
		}
		return logicalcluster.Name(parts[3]), true
	}

	if len(parts) == 5 && !isCluster(parts[2]) {
		// group/resource/identity/cluster/name
		return logicalcluster.Name(parts[3]), true
	}

	// group/resource/cluster/[namespace/]name
	// group/resource/cluster/namespace/name
	return logicalcluster.Name(parts[2]), true
}

// BelongsToCluster reports whether key belongs to logical cluster lc.
// It checks both the built-in (parts[2]) and CRD/identity-based (parts[3])
// cluster positions via SplitKey.
func BelongsToCluster(prefix, key string, lc logicalcluster.Name) bool {
	split, ok := SplitKey(prefix, key, lc)
	if !ok {
		return false
	}
	return split.Cluster == lc
}
