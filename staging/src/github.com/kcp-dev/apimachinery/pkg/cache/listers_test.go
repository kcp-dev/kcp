/*
Copyright 2022 The KCP Authors.

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

package cache

import (
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
)

func newUnstructured(cluster, namespace, name string, labels labels.Set) *unstructured.Unstructured {
	u := new(unstructured.Unstructured)
	u.SetAnnotations(map[string]string{
		logicalcluster.AnnotationKey: cluster,
	})
	u.SetNamespace(namespace)
	u.SetName(name)
	u.SetLabels(labels)
	return u
}

func newTestIndexer(t *testing.T) cache.Indexer {
	indexer := cache.NewIndexer(
		MetaClusterNamespaceKeyFunc,
		cache.Indexers{
			ClusterIndexName:             ClusterIndexFunc,
			ClusterAndNamespaceIndexName: ClusterAndNamespaceIndexFunc,
		})

	for _, cluster := range []string{"c1", "c2"} {
		err := indexer.Add(newUnstructured(cluster, "ns1", "n1", map[string]string{"app": "myapp"}))
		require.NoError(t, err)

		err = indexer.Add(newUnstructured(cluster, "ns2", "n1", map[string]string{"app": "myapp"}))
		require.NoError(t, err)
		err = indexer.Add(newUnstructured(cluster, "ns2", "n2", nil))
		require.NoError(t, err)

		err = indexer.Add(newUnstructured(cluster, "", "cn1", map[string]string{"app": "myapp"}))
		require.NoError(t, err)
		err = indexer.Add(newUnstructured(cluster, "", "cn2", nil))
		require.NoError(t, err)
	}

	return indexer
}

func TestGenericClusterLister(t *testing.T) {
	indexer := newTestIndexer(t)

	l := NewGenericClusterLister(indexer, schema.GroupResource{})

	tests := map[string]struct {
		selector   labels.Selector
		desiredLen int
	}{
		"nil":        {selector: nil, desiredLen: 10},
		"everything": {selector: labels.Everything(), desiredLen: 10},
		"app: myapp": {selector: labels.Set{"app": "myapp"}.AsSelector(), desiredLen: 6},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			list, err := l.List(tt.selector)
			require.NoError(t, err)
			require.Len(t, list, tt.desiredLen)
		})
	}
}

func TestGenericLister(t *testing.T) {
	indexer := newTestIndexer(t)

	l := NewGenericClusterLister(indexer, schema.GroupResource{})

	tests := map[string]struct {
		cluster    string
		selector   labels.Selector
		desiredLen int
		name       string
	}{
		"c1,nil":        {cluster: "c1", selector: nil, desiredLen: 5},
		"c2,nil":        {cluster: "c2", selector: nil, desiredLen: 5},
		"c1,everything": {cluster: "c1", selector: labels.Everything(), desiredLen: 5},
		"c2,everything": {cluster: "c2", selector: labels.Everything(), desiredLen: 5},
		"c1,app:myapp":  {cluster: "c1", selector: labels.Set{"app": "myapp"}.AsSelector(), desiredLen: 3},
		"c2,app:myapp":  {cluster: "c2", selector: labels.Set{"app": "myapp"}.AsSelector(), desiredLen: 3},
		"c1,cn1":        {cluster: "c1", name: "cn1"},
		"c1,cn2":        {cluster: "c1", name: "cn2"},
		"c2,cn1":        {cluster: "c2", name: "cn1"},
		"c2,cn2":        {cluster: "c2", name: "cn2"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			lister := l.ByCluster(logicalcluster.Name(tt.cluster))

			if tt.name == "" {
				list, err := lister.List(tt.selector)

				require.NoError(t, err)
				require.Len(t, list, tt.desiredLen)

				for _, item := range list {
					obj := item.(*unstructured.Unstructured)

					clusterName := logicalcluster.From(obj).String()
					require.Equal(t, tt.cluster, clusterName)

					if tt.selector != nil {
						var labelMap labels.Set = obj.GetLabels()
						require.True(t, tt.selector.Matches(labelMap))
					}
				}
			} else {
				item, err := lister.Get(tt.name)
				require.NoError(t, err)
				obj := item.(*unstructured.Unstructured)

				name := obj.GetName()
				require.Equal(t, tt.name, name)

				clusterName := logicalcluster.From(obj).String()
				require.Equal(t, tt.cluster, clusterName)
			}
		})
	}
}

func TestGenericNamespaceLister(t *testing.T) {
	indexer := newTestIndexer(t)

	cluster := "c1"

	l := NewGenericClusterLister(indexer, schema.GroupResource{}).ByCluster(logicalcluster.Name(cluster))

	tests := map[string]struct {
		namespace  string
		selector   labels.Selector
		desiredLen int
		name       string
	}{
		"ns1,nil":        {namespace: "ns1", selector: nil, desiredLen: 1},
		"ns2,nil":        {namespace: "ns2", selector: nil, desiredLen: 2},
		"ns1,everything": {namespace: "ns1", selector: labels.Everything(), desiredLen: 1},
		"ns2,everything": {namespace: "ns2", selector: labels.Everything(), desiredLen: 2},
		"ns1,app:myapp":  {namespace: "ns1", selector: labels.Set{"app": "myapp"}.AsSelector(), desiredLen: 1},
		"ns2,app:myapp":  {namespace: "ns2", selector: labels.Set{"app": "myapp"}.AsSelector(), desiredLen: 1},
		"ns1,n1":         {namespace: "ns1", name: "n1"},
		"ns2,n1":         {namespace: "ns2", name: "n1"},
		"ns2,n2":         {namespace: "ns2", name: "n2"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			lister := l.ByNamespace(tt.namespace)

			if tt.name == "" {
				list, err := lister.List(tt.selector)

				require.NoError(t, err)
				require.Len(t, list, tt.desiredLen)

				for _, item := range list {
					obj := item.(*unstructured.Unstructured)

					clusterName := logicalcluster.From(obj).String()
					require.Equal(t, cluster, clusterName)

					namespace := obj.GetNamespace()
					require.Equal(t, tt.namespace, namespace)

					if tt.selector != nil {
						var labelMap labels.Set = obj.GetLabels()
						require.True(t, tt.selector.Matches(labelMap))
					}
				}
			} else {
				item, err := lister.Get(tt.name)
				require.NoError(t, err)

				obj := item.(*unstructured.Unstructured)

				clusterName := logicalcluster.From(obj).String()
				require.Equal(t, cluster, clusterName)

				namespace := obj.GetNamespace()
				require.Equal(t, tt.namespace, namespace)

				name := obj.GetName()
				require.Equal(t, tt.name, name)
			}
		})
	}
}
