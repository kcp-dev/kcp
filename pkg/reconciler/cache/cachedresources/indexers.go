/*
Copyright 2025 The kcp Authors.

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

package cachedresources

import (
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kcp-dev/logicalcluster/v3"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
)

const (
	// ByGVRAndLogicalCluster is the name for the index that indexes by an object's gvr and logical cluster.
	ByGVRAndLogicalCluster = "kcp-byGVRAndLogicalCluster"

	// ByIdentityAndGroupResource is the name for the index that indexes by an object's identity hash and group/resource.
	ByIdentityAndGroupResource = "kcp-byIdentityAndGroupResource"

	ByGroupResource = "kcp-byGroupResource"
)

// IndexByShardAndLogicalClusterAndNamespace is an index function that indexes by an object's gvr and logical cluster.
func IndexByGVRAndLogicalCluster(obj interface{}) ([]string, error) {
	cachedResource := obj.(*cachev1alpha1.CachedResource)

	if cachedResource.Status.IdentityHash == "" {
		return []string{}, nil
	}
	if cachedResource.Annotations == nil ||
		cachedResource.Annotations[AnnotationResourceKind] == "" ||
		cachedResource.Annotations[AnnotationResourceScope] == "" {
		return []string{}, nil
	}

	return []string{
		GVRAndLogicalClusterKey(
			schema.GroupVersionResource(cachedResource.Spec.GroupVersionResource),
			logicalcluster.From(cachedResource),
		),
	}, nil
}

// IndexByIdentityAndGroupResource is an index function that indexes by an object's identity hash and group/resource.
// Objects with an empty identity hash are not indexed.
func IndexByIdentityAndGroupResource(obj interface{}) ([]string, error) {
	cachedResource := obj.(*cachev1alpha1.CachedResource)

	if cachedResource.Status.IdentityHash == "" {
		return []string{}, nil
	}
	if cachedResource.Annotations == nil ||
		cachedResource.Annotations[AnnotationResourceKind] == "" ||
		cachedResource.Annotations[AnnotationResourceScope] == "" {
		return []string{}, nil
	}

	gvr := schema.GroupVersionResource(cachedResource.Spec.GroupVersionResource)
	return []string{IdentityAndGroupResourceKey(cachedResource.Status.IdentityHash, gvr.GroupResource())}, nil
}

// IdentityAndGroupResourceKey creates an index key from an identity hash and group/resource.
func IdentityAndGroupResourceKey(identity string, gr schema.GroupResource) string {
	return identity + "|" + gr.String()
}

// IndexByGroupResource is an index function that indexes by an object's group/resource.
func IndexByGroupResource(obj interface{}) ([]string, error) {
	cachedResource := obj.(*cachev1alpha1.CachedResource)
	if cachedResource.Status.IdentityHash == "" {
		return []string{}, nil
	}
	if cachedResource.Annotations == nil ||
		cachedResource.Annotations[AnnotationResourceKind] == "" ||
		cachedResource.Annotations[AnnotationResourceScope] == "" {
		return []string{}, nil
	}
	gvr := schema.GroupVersionResource(cachedResource.Spec.GroupVersionResource)
	return []string{GroupResourceKey(gvr.GroupResource())}, nil
}

// GroupResourceKey creates an index key from an identity hash and group/resource.
func GroupResourceKey(gr schema.GroupResource) string {
	return gr.String()
}

// GVRAndShardAndLogicalClusterAndNamespaceKey creates an index key from the given parameters.
// Key will be in the form of version.resource.group|cluster.
func GVRAndLogicalClusterKey(gvr schema.GroupVersionResource, cluster logicalcluster.Name) string {
	var key string
	if gvr.Group == "" {
		gvr.Group = "core"
	}
	key += gvr.Version + "." + gvr.Resource + "." + gvr.Group
	if !cluster.Empty() {
		key += "|" + cluster.String()
	}
	return key
}
