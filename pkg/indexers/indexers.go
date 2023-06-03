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

	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/kcp-dev/kcp/sdk/apis/core"
)

const (
	// APIBindingByClusterAndAcceptedClaimedGroupResources is the name for the index that indexes an APIBinding by its
	// cluster name and accepted claimed group resources.
	APIBindingByClusterAndAcceptedClaimedGroupResources = "byClusterAndAcceptedClaimedGroupResources"
	// ByLogicalClusterPath indexes by logical cluster path, if the annotation exists.
	ByLogicalClusterPath = "ByLogicalClusterPath"
	// ByLogicalClusterPathAndName indexes by logical cluster path and object name, if the annotation exists.
	ByLogicalClusterPathAndName = "ByLogicalClusterPathAndName"
)

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

// ByIndexWithFallback returns all instances of T that match indexValue in indexer, if any. If none match,
// the same query is done of globalIndexer. Any errors short-circuit this logic.
func ByIndexWithFallback[T runtime.Object](indexer, globalIndexer cache.Indexer, indexName, indexValue string) ([]T, error) {
	// Try local informer first
	results, err := ByIndex[T](indexer, indexName, indexValue)
	if err != nil {
		// Unrecoverable error
		return nil, err
	}
	if err == nil && len(results) > 0 {
		// Quick happy path - found something locally
		return results, nil
	}
	// Didn't find it locally - try remote
	return ByIndex[T](globalIndexer, indexName, indexValue)
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

// ByPathAndNameWithFallback returns the instance of T from the indexer with the matching path and name. Path may be a canonical path
// or a cluster name. Note: this depends on the presence of the optional "kcp.io/path" annotation. If no instance is found, globalIndexer
// is searched as well. Any errors short-circuit this logic.
func ByPathAndNameWithFallback[T runtime.Object](groupResource schema.GroupResource, indexer, globalIndexer cache.Indexer, path logicalcluster.Path, name string) (ret T, err error) {
	// Try local informer first
	result, err := ByPathAndName[T](groupResource, indexer, path, name)
	if err != nil && !apierrors.IsNotFound(err) {
		// Unrecoverable error
		return ret, err
	}
	if err == nil {
		// Quick happy path - found it locally
		return result, nil
	}
	// Didn't find it locally - try remote
	return ByPathAndName[T](groupResource, globalIndexer, path, name)
}
