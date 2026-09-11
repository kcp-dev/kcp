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

package builder

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/kcp-dev/logicalcluster/v3"
)

func TestDigestURL(t *testing.T) {
	t.Parallel()
	const prefix = "/services/admin"

	tests := []struct {
		urlPath      string
		accepted     bool
		wildcard     bool
		clusterName  string
		prefixToTrim string
	}{
		{urlPath: "/services/admin", accepted: true, clusterName: "root", prefixToTrim: "/services/admin"},
		{urlPath: "/services/admin/apis/core.kcp.io/v1alpha1/shards", accepted: true, clusterName: "root", prefixToTrim: "/services/admin"},
		{urlPath: "/services/admin/clusters/*/apis/core.kcp.io/v1alpha1/shards", accepted: true, wildcard: true, prefixToTrim: "/services/admin/clusters/*"},
		{urlPath: "/services/admin/clusters/myws/apis/core.kcp.io/v1alpha1/shards", accepted: true, clusterName: "myws", prefixToTrim: "/services/admin/clusters/myws"},
		{urlPath: "/services/adminfoo/apis", accepted: false},
		{urlPath: "/services/other", accepted: false},
	}

	for _, tc := range tests {
		t.Run(tc.urlPath, func(t *testing.T) {
			t.Parallel()
			cluster, prefixToTrim, accepted := digestURL(tc.urlPath, prefix)
			if accepted != tc.accepted {
				t.Fatalf("accepted = %v, want %v", accepted, tc.accepted)
			}
			if !tc.accepted {
				return
			}
			if cluster.Wildcard != tc.wildcard {
				t.Errorf("wildcard = %v, want %v", cluster.Wildcard, tc.wildcard)
			}
			if !tc.wildcard && cluster.Name.String() != tc.clusterName {
				t.Errorf("cluster = %q, want %q", cluster.Name, tc.clusterName)
			}
			if prefixToTrim != tc.prefixToTrim {
				t.Errorf("prefixToStrip = %q, want %q", prefixToTrim, tc.prefixToTrim)
			}
		})
	}
}

func shardObj(name, cluster string, annotations map[string]string) unstructured.Unstructured {
	u := unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "core.kcp.io/v1alpha1",
		"kind":       "Shard",
		"metadata": map[string]interface{}{
			"name": name,
			"annotations": map[string]interface{}{
				logicalcluster.AnnotationKey: cluster,
			},
		},
	}}
	for k, v := range annotations {
		anns := u.GetAnnotations()
		anns[k] = v
		u.SetAnnotations(anns)
	}
	return u
}

func TestDedupeShardsPrefersSystemShard(t *testing.T) {
	t.Parallel()
	items := []unstructured.Unstructured{
		shardObj("alpha", "root", nil),
		shardObj("alpha", "system:shard", nil),
		shardObj("beta", "root", nil),
	}
	result := dedupeShards(items)
	if len(result) != 2 {
		t.Fatalf("expected 2 shards after dedupe, got %d", len(result))
	}
	if got := logicalcluster.From(&result[0]).String(); got != "system:shard" {
		t.Errorf("expected the system:shard copy of alpha to win, got cluster %q", got)
	}
	if result[1].GetName() != "beta" {
		t.Errorf("expected beta to survive, got %q", result[1].GetName())
	}
}

func TestFixupShardStripsCacheBookkeeping(t *testing.T) {
	t.Parallel()
	u := shardObj("alpha", "system:shard", map[string]string{
		"kcp.io/shard":                           "alpha",
		"cache.kcp.io/original-resource-version": "42",
		"cache.kcp.io/original-resource-UID":     "abc",
	})
	fixupShard(&u)
	anns := u.GetAnnotations()
	for _, key := range []string{"kcp.io/shard", "cache.kcp.io/original-resource-version", "cache.kcp.io/original-resource-UID"} {
		if _, ok := anns[key]; ok {
			t.Errorf("annotation %q must be stripped", key)
		}
	}
	if anns[logicalcluster.AnnotationKey] != "system:shard" {
		t.Error("the kcp.io/cluster annotation must be kept")
	}
}
