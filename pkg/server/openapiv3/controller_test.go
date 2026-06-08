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

package openapiv3

import (
	"testing"

	"k8s.io/kube-openapi/pkg/cached"
	"k8s.io/kube-openapi/pkg/spec3"

	"github.com/kcp-dev/logicalcluster/v3"
)

// TestEvictCluster verifies EvictCluster drops the whole per-cluster
// bucket. This is the back-stop for missed CRD delete events: if a
// LogicalCluster is gone, every cached spec under it must go too.
func TestEvictCluster(t *testing.T) {
	t.Parallel()
	c := &Controller{
		byClusterNameVersion: map[logicalcluster.Name]map[string]map[string]cached.Value[*spec3.OpenAPI]{
			"target":  {"crd-a": {"v1": cached.Static(&spec3.OpenAPI{}, "etag")}},
			"other":   {"crd-b": {"v1": cached.Static(&spec3.OpenAPI{}, "etag")}},
			"another": {"crd-c": {"v1": cached.Static(&spec3.OpenAPI{}, "etag")}},
		},
	}

	c.EvictCluster("target")

	if _, ok := c.byClusterNameVersion["target"]; ok {
		t.Error("byClusterNameVersion[target] should be removed after EvictCluster")
	}
	if _, ok := c.byClusterNameVersion["other"]; !ok {
		t.Error("byClusterNameVersion[other] must be preserved")
	}
	if _, ok := c.byClusterNameVersion["another"]; !ok {
		t.Error("byClusterNameVersion[another] must be preserved")
	}

	// Idempotent: evicting again must not panic and must leave others alone.
	c.EvictCluster("target")
	c.EvictCluster("never-existed")
	if len(c.byClusterNameVersion) != 2 {
		t.Errorf("expected 2 remaining buckets, got %d", len(c.byClusterNameVersion))
	}
}
