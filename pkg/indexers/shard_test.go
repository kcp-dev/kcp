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

package indexers

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	corev1alpha1listers "github.com/kcp-dev/sdk/client/listers/core/v1alpha1"
)

func shardInCluster(name, cluster string) *corev1alpha1.Shard {
	return &corev1alpha1.Shard{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{logicalcluster.AnnotationKey: cluster},
		},
	}
}

func TestShardByName(t *testing.T) {
	t.Parallel()
	indexer := cache.NewIndexer(kcpcache.MetaClusterNamespaceKeyFunc, cache.Indexers{})
	for _, shard := range []*corev1alpha1.Shard{
		shardInCluster("alpha", "system:shard"),
		shardInCluster("alpha", "root"), // legacy copy, authoritative wins
		shardInCluster("legacy", "root"),
	} {
		if err := indexer.Add(shard); err != nil {
			t.Fatal(err)
		}
	}
	lister := corev1alpha1listers.NewShardClusterLister(indexer)

	shard, err := ShardByName(lister, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if cluster := logicalcluster.From(shard); cluster != "system:shard" {
		t.Errorf("expected the authoritative shard from system:shard, got the one from %q", cluster)
	}

	shard, err = ShardByName(lister, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if cluster := logicalcluster.From(shard); cluster != "root" {
		t.Errorf("expected the legacy shard from root, got the one from %q", cluster)
	}

	if _, err := ShardByName(lister, "missing"); !errors.IsNotFound(err) {
		t.Errorf("expected a NotFound error, got %v", err)
	}
}
