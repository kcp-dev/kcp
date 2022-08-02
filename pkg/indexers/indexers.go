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

package indexers

import (
	"fmt"
	"strings"

	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"

	syncershared "github.com/kcp-dev/kcp/pkg/syncer/shared"
)

const (
	// ByLogicalCluster is the name for the index that indexes by an object's logical cluster.
	ByLogicalCluster = "kcp-global-byLogicalCluster"
	// ByLogicalClusterAndNamespace is the name for the index that indexes by an object's logical cluster and namespace.
	ByLogicalClusterAndNamespace = "kcp-global-byLogicalClusterAndNamespace"
	// IndexAPIExportByIdentity is the indexer name for by identity index for the API Export indexers.
	IndexAPIExportByIdentity = "byIdentity"
	// BySyncerFinalizerKey is the name for the index that indexes by syncer finalizer label keys.
	BySyncerFinalizerKey = "bySyncerFinalizerKey"
)

// ClusterScoped returns cache.Indexers appropriate for cluster-scoped resources.
func ClusterScoped() cache.Indexers {
	return cache.Indexers{
		ByLogicalCluster: IndexByLogicalCluster,
	}
}

// NamespaceScoped returns cache.Indexers appropriate for namespace-scoped resources.
func NamespaceScoped() cache.Indexers {
	return cache.Indexers{
		ByLogicalCluster:             IndexByLogicalCluster,
		ByLogicalClusterAndNamespace: IndexByLogicalClusterAndNamespace,
	}
}

// IndexByLogicalCluster is an index function that indexes by an object's logical cluster.
func IndexByLogicalCluster(obj interface{}) ([]string, error) {
	a, err := meta.Accessor(obj)
	if err != nil {
		return nil, err
	}

	return []string{logicalcluster.From(a).String()}, nil
}

// IndexByLogicalClusterAndNamespace is an index function that indexes by an object's logical cluster and namespace.
func IndexByLogicalClusterAndNamespace(obj interface{}) ([]string, error) {
	a, err := meta.Accessor(obj)
	if err != nil {
		return nil, err
	}

	return []string{clusters.ToClusterAwareKey(logicalcluster.From(a), a.GetNamespace())}, nil
}

// IndexBySyncerFinalizerKey indexes by syncer finalizer label keys.
func IndexBySyncerFinalizerKey(obj interface{}) ([]string, error) {
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be a metav1.Object, but is %T", obj)
	}

	syncerFinalizers := []string{}
	for _, f := range metaObj.GetFinalizers() {
		if strings.HasPrefix(f, syncershared.SyncerFinalizerNamePrefix) {
			syncerFinalizers = append(syncerFinalizers, f)
		}
	}

	return syncerFinalizers, nil
}
