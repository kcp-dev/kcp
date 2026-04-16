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

package cachedresources

import (
	"context"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kcp-dev/logicalcluster/v3"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
)

const (
	// AnnotationResourceKind is set on CachedResource objects to record the Kind of the cached resource.
	AnnotationResourceKind = "cache.kcp.io/resource-kind"

	// AnnotationResourceScope records the REST scope of the cached resource: "Namespaced" or "Cluster".
	AnnotationResourceScope = "cache.kcp.io/resource-scope"
)

// reconcileResourceMetadata ensures the CachedResource carries Kind and Scope annotations.
// The cache server's CRD lister reads these to synthesize CRDs on demand.
type reconcileResourceMetadata struct {
	getKind      func(cluster logicalcluster.Name, gvr schema.GroupVersionResource) (schema.GroupVersionKind, error)
	getRESTScope func(cluster logicalcluster.Name, gvr schema.GroupVersionResource) (meta.RESTScope, error)
}

func (r *reconcileResourceMetadata) reconcile(ctx context.Context, cachedResource *cachev1alpha1.CachedResource) (reconcileStatus, error) {
	if !cachedResource.DeletionTimestamp.IsZero() {
		return reconcileStatusContinue, nil
	}

	gvr := schema.GroupVersionResource(cachedResource.Spec.GroupVersionResource)
	clusterName := logicalcluster.From(cachedResource)

	kind, err := r.getKind(clusterName, gvr)
	if err != nil {
		return reconcileStatusStopAndRequeue, err
	}
	restScope, err := r.getRESTScope(clusterName, gvr)
	if err != nil {
		return reconcileStatusStopAndRequeue, err
	}

	scopeStr := string(apiextensionsv1.ClusterScoped)
	if restScope.Name() == meta.RESTScopeNameNamespace {
		scopeStr = string(apiextensionsv1.NamespaceScoped)
	}

	annotations := cachedResource.Annotations
	if annotations == nil {
		annotations = make(map[string]string)
	}

	if annotations[AnnotationResourceKind] == kind.Kind && annotations[AnnotationResourceScope] == scopeStr {
		return reconcileStatusContinue, nil
	}

	annotations[AnnotationResourceKind] = kind.Kind
	annotations[AnnotationResourceScope] = scopeStr
	cachedResource.SetAnnotations(annotations)

	// Future iteration will pick up the metadata changes.
	return reconcileStatusStop, nil
}
