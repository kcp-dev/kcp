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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kcp-dev/logicalcluster/v3"
)

func TestSplitKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		prefix       string
		key          string
		lc           logicalcluster.Name
		wantOK       bool
		wantKeyParts KeyParts
	}{
		{
			name:   "built-in resource",
			prefix: "/registry/",
			key:    "/registry/apps/deployments/root:ws/default/my-deploy",
			lc:     logicalcluster.Name("root:ws"),
			wantOK: true,
			wantKeyParts: KeyParts{
				Group:    "apps",
				Resource: "deployments",
				Segment:  "",
				Cluster:  "root:ws",
				Rest:     "default/my-deploy",
			},
		},
		{
			name:   "CRD resource",
			prefix: "/registry/",
			key:    "/registry/mygroup.io/widgets/customresources/root:ws/my-widget",
			lc:     logicalcluster.Name("root:ws"),
			wantOK: true,
			wantKeyParts: KeyParts{
				Group:    "mygroup.io",
				Resource: "widgets",
				Segment:  "customresources",
				Cluster:  "root:ws",
				Rest:     "my-widget",
			},
		},
		{
			name:   "identity-based resource",
			prefix: "/registry/",
			key:    "/registry/mygroup.io/widgets/abc123def/root:ws/my-widget",
			lc:     logicalcluster.Name("root:ws"),
			wantOK: true,
			wantKeyParts: KeyParts{
				Group:    "mygroup.io",
				Resource: "widgets",
				Segment:  "abc123def",
				Cluster:  "root:ws",
				Rest:     "my-widget",
			},
		},
		{
			name:   "key too short",
			prefix: "/registry/",
			key:    "/registry/apps/deployments",
			wantOK: false,
		},
		{
			name:   "no prefix match",
			prefix: "/registry/",
			key:    "/other/apps/deployments/root:ws/my-deploy",
			lc:     logicalcluster.Name("root:ws"),
			wantOK: false,
		},
		{
			name:   "prefix without trailing slash normalised",
			prefix: "/registry",
			key:    "/registry/apps/deployments/root:ws/my-deploy",
			lc:     logicalcluster.Name("root:ws"),
			wantOK: true,
			wantKeyParts: KeyParts{
				Group:    "apps",
				Resource: "deployments",
				Segment:  "",
				Cluster:  "root:ws",
				Rest:     "my-deploy",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			keyParts, ok := SplitKey(tt.prefix, tt.key, tt.lc)
			require.Equal(t, tt.wantOK, ok)
			if !ok {
				return
			}
			assert.Equal(t, tt.wantKeyParts, keyParts)
		})
	}
}

func TestClusterOf(t *testing.T) {
	t.Parallel()

	knownClusters := map[string]bool{
		"root:ws":    true,
		"root:other": true,
	}
	isCluster := func(segment string) bool {
		return strings.HasPrefix(segment, "system:") || knownClusters[segment]
	}

	tests := []struct {
		name        string
		prefix      string
		key         string
		wantOK      bool
		wantCluster logicalcluster.Name
	}{
		{
			name:        "built-in namespaced resource",
			prefix:      "/registry/",
			key:         "/registry/apps/deployments/root:ws/default/my-deploy",
			wantOK:      true,
			wantCluster: "root:ws",
		},
		{
			name:        "built-in cluster-scoped resource",
			prefix:      "/registry/",
			key:         "/registry/rbac.authorization.k8s.io/clusterroles/root:ws/my-role",
			wantOK:      true,
			wantCluster: "root:ws",
		},
		{
			name:        "CRD namespaced resource",
			prefix:      "/registry/",
			key:         "/registry/mygroup.io/widgets/customresources/root:ws/default/my-widget",
			wantOK:      true,
			wantCluster: "root:ws",
		},
		{
			name:        "CRD cluster-scoped resource",
			prefix:      "/registry/",
			key:         "/registry/mygroup.io/widgets/customresources/root:ws/my-widget",
			wantOK:      true,
			wantCluster: "root:ws",
		},
		{
			name:        "identity-based namespaced resource",
			prefix:      "/registry/",
			key:         "/registry/mygroup.io/widgets/abc123def/root:ws/default/my-widget",
			wantOK:      true,
			wantCluster: "root:ws",
		},
		{
			name:        "identity-based cluster-scoped resource (ambiguous 5 segments)",
			prefix:      "/registry/",
			key:         "/registry/mygroup.io/widgets/abc123def/root:ws/my-widget",
			wantOK:      true,
			wantCluster: "root:ws",
		},
		{
			name:        "system cluster",
			prefix:      "/registry/",
			key:         "/registry/core/configmaps/system:admin/default/cm",
			wantOK:      true,
			wantCluster: "system:admin",
		},
		{
			name:   "key too short",
			prefix: "/registry/",
			key:    "/registry/apps/deployments",
			wantOK: false,
		},
		{
			name:   "no prefix match",
			prefix: "/registry/",
			key:    "/other/apps/deployments/root:ws/my-deploy",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cluster, ok := ClusterOf(tt.prefix, tt.key, isCluster)
			require.Equal(t, tt.wantOK, ok)
			if !ok {
				return
			}
			assert.Equal(t, tt.wantCluster, cluster)
		})
	}
}

func TestBelongsToCluster(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		prefix string
		key    string
		lc     logicalcluster.Name
		want   bool
	}{
		{
			name:   "built-in resource",
			prefix: "/registry/",
			key:    "/registry/apps/deployments/root:ws/default/my-deploy",
			lc:     "root:ws",
			want:   true,
		},
		{
			name:   "CRD resource",
			prefix: "/registry/",
			key:    "/registry/mygroup.io/widgets/customresources/root:ws/my-widget",
			lc:     "root:ws",
			want:   true,
		},
		{
			name:   "identity-based resource",
			prefix: "/registry/",
			key:    "/registry/mygroup.io/widgets/abc123def/root:ws/my-widget",
			lc:     "root:ws",
			want:   true,
		},
		{
			name:   "wrong cluster built-in",
			prefix: "/registry/",
			key:    "/registry/apps/deployments/root:other/default/my-deploy",
			lc:     "root:ws",
			want:   false,
		},
		{
			name:   "wrong cluster CRD",
			prefix: "/registry/",
			key:    "/registry/mygroup.io/widgets/customresources/root:other/my-widget",
			lc:     "root:ws",
			want:   false,
		},
		{
			name:   "key too short",
			prefix: "/registry/",
			key:    "/registry/apps/deployments",
			lc:     "root:ws",
			want:   false,
		},
		{
			name:   "no prefix match",
			prefix: "/registry/",
			key:    "/other/apps/deployments/root:ws/my-deploy",
			lc:     "root:ws",
			want:   false,
		},
		{
			name:   "identity hash equals cluster name (false-positive)",
			prefix: "/registry/",
			key:    "/registry/mygroup.io/widgets/root:ws/root:ws/my-widget",
			lc:     "root:ws",
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := BelongsToCluster(tt.prefix, tt.key, tt.lc)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestKeyPartsClusterPrefix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		parts         KeyParts
		storagePrefix string
		want          string
	}{
		{
			name:          "built-in",
			parts:         KeyParts{Group: "apps", Resource: "deployments", Segment: "", Cluster: "root:ws"},
			storagePrefix: "/registry/",
			want:          "/registry/apps/deployments/root:ws",
		},
		{
			name:          "CRD",
			parts:         KeyParts{Group: "mygroup.io", Resource: "widgets", Segment: "customresources", Cluster: "root:ws"},
			storagePrefix: "/registry/",
			want:          "/registry/mygroup.io/widgets/customresources/root:ws",
		},
		{
			name:          "identity-based",
			parts:         KeyParts{Group: "mygroup.io", Resource: "widgets", Segment: "abc123def", Cluster: "root:ws"},
			storagePrefix: "/registry/",
			want:          "/registry/mygroup.io/widgets/abc123def/root:ws",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.parts.ClusterPrefix(tt.storagePrefix)
			assert.Equal(t, tt.want, got)
		})
	}
}
