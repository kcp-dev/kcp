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
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/kcp-dev/kcp/pkg/apis/core"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	syncershared "github.com/kcp-dev/kcp/pkg/syncer/shared"
)

const (
	// BySyncerFinalizerKey is the name for the index that indexes by syncer finalizer label keys.
	BySyncerFinalizerKey = "bySyncerFinalizerKey"
	// APIBindingByClusterAndAcceptedClaimedGroupResources is the name for the index that indexes an APIBinding by its
	// cluster name and accepted claimed group resources.
	APIBindingByClusterAndAcceptedClaimedGroupResources = "byClusterAndAcceptedClaimedGroupResources"
	// ByClusterResourceStateLabelKey indexes resources based on the cluster state label key.
	ByClusterResourceStateLabelKey = "ByClusterResourceStateLabelKey"
	// ByLogicalClusterPath indexes by logical cluster path, if the annotation exists.
	ByLogicalClusterPath = "ByLogicalClusterPath"
	// ByLogicalClusterPathAndName indexes by logical cluster path and object name, if the annotation exists.
	ByLogicalClusterPathAndName = "ByLogicalClusterPathAndName"
	// NamespaceLocatorIndexName is the name of the index that allows you to filter by namespace locator.
	NamespaceLocatorIndexName = "ns-locator"
)

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

// IndexByClusterResourceStateLabelKey indexes resources based on the cluster state key label.
func IndexByClusterResourceStateLabelKey(obj interface{}) ([]string, error) {
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be a metav1.Object, but is %T", obj)
	}

	ClusterResourceStateLabelKeys := []string{}
	for k := range metaObj.GetLabels() {
		if strings.HasPrefix(k, workloadv1alpha1.ClusterResourceStateLabelPrefix) {
			ClusterResourceStateLabelKeys = append(ClusterResourceStateLabelKeys, k)
		}
	}
	return ClusterResourceStateLabelKeys, nil
}

// IndexByLogicalClusterPath indexes by logical cluster path, if the annotation exists.
func IndexByLogicalClusterPath(obj interface{}) ([]string, error) {
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be a metav1.Object, but is %T", obj)
	}
	if path, found := metaObj.GetAnnotations()[core.LogicalClusterPathAnnotationKey]; found {
		return []string{
			logicalcluster.NewPath(path).String(),
			logicalcluster.From(metaObj).String(),
		}, nil
	}

	return []string{logicalcluster.From(metaObj).String()}, nil
}

// IndexByLogicalClusterPathAndName indexes by logical cluster path and object name, if the annotation exists.
func IndexByLogicalClusterPathAndName(obj interface{}) ([]string, error) {
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be a metav1.Object, but is %T", obj)
	}
	if path, found := metaObj.GetAnnotations()[core.LogicalClusterPathAnnotationKey]; found {
		return []string{
			logicalcluster.NewPath(path).Join(metaObj.GetName()).String(),
			logicalcluster.From(metaObj).Path().Join(metaObj.GetName()).String(),
		}, nil
	}

	return []string{logicalcluster.From(metaObj).Path().Join(metaObj.GetName()).String()}, nil
}

// ByIndex returns all instances of T that match indexValue in indexName in indexer.
func ByIndex[T runtime.Object](indexer cache.Indexer, indexName, indexValue string) ([]T, error) {
	list, err := indexer.ByIndex(indexName, indexValue)
	if err != nil {
		return nil, err
	}

	ret := make([]T, 0, len(list))
	for _, o := range list {
		ret = append(ret, o.(T))
	}

	return ret, nil
}

// ByPathAndName returns the instance of T from the indexer with the matching path and name. Path may be a canonical path
// or a cluster name. Note: this depends on the presence of the optional "kcp.io/path" annotation.
func ByPathAndName[T runtime.Object](groupResource schema.GroupResource, indexer cache.Indexer, path logicalcluster.Path, name string) (ret T, err error) {
	objs, err := indexer.ByIndex(ByLogicalClusterPathAndName, path.Join(name).String())
	if err != nil {
		return ret, err
	}
	if len(objs) == 0 {
		return ret, apierrors.NewNotFound(groupResource, path.Join(name).String())
	}
	if len(objs) > 1 {
		return ret, fmt.Errorf("multiple %s found for %s", groupResource, path.Join(name).String())
	}
	return objs[0].(T), nil
}

// IndexByNamespaceLocator is a cache.IndexFunc that indexes namespaces by the namespaceLocator annotation.
func IndexByNamespaceLocator(obj interface{}) ([]string, error) {
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return nil, fmt.Errorf("obj is supposed to be a metav1.Object, but is %T", obj)
	}

	if loc, found, err := syncershared.LocatorFromAnnotations(metaObj.GetAnnotations()); err != nil {
		return nil, fmt.Errorf("failed to get locator from annotations: %w", err)
	} else if !found {
		return nil, nil
	} else {
		nsLocByte, err := json.Marshal(loc)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal locator %#v: %w", loc, err)
		}
		return []string{NamespaceLocatorIndexKey(nsLocByte)}, nil
	}
}

// NamespaceLocatorIndexKey formats the index key for a namespace locator.
func NamespaceLocatorIndexKey(namespaceLocator []byte) string {
	return string(namespaceLocator)
}
