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

package builder

import (
	"context"
	"fmt"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/registry/customresource"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/client-go/dynamic"
	"k8s.io/kube-openapi/pkg/validation/validate"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apiserver"
	registry "github.com/kcp-dev/kcp/pkg/virtual/framework/forwardingregistry"
)

func provideForwardingRestStorage(ctx context.Context, clusterClient dynamic.ClusterInterface, workloadClusterName string) apiserver.RestProviderFunc {
	return func(resource schema.GroupVersionResource, kind schema.GroupVersionKind, listKind schema.GroupVersionKind, typer runtime.ObjectTyper, tableConvertor rest.TableConvertor, namespaceScoped bool, schemaValidator *validate.SchemaValidator, subresourcesSchemaValidator map[string]*validate.SchemaValidator, structuralSchema *structuralschema.Structural) (mainStorage rest.Storage, subresourceStorages map[string]rest.Storage) {
		statusSchemaValidate, statusEnabled := subresourcesSchemaValidator["status"]

		var statusSpec *apiextensions.CustomResourceSubresourceStatus
		if statusEnabled {
			statusSpec = &apiextensions.CustomResourceSubresourceStatus{}
		}

		var scaleSpec *apiextensions.CustomResourceSubresourceScale
		// TODO(sttts): implement scale subresource

		strategy := customresource.NewStrategy(
			typer,
			namespaceScoped,
			kind,
			schemaValidator,
			statusSchemaValidate,
			map[string]*structuralschema.Structural{resource.Version: structuralSchema},
			statusSpec,
			scaleSpec,
		)

		storage := registry.NewStorage(
			ctx,
			resource,
			kind,
			listKind,
			strategy,
			nil,
			tableConvertor,
			nil,
			clusterClient,
			nil,
			wrapStorageWithLabelSelector(map[string]string{workloadv1alpha1.InternalClusterResourceStateLabelPrefix + workloadClusterName: string(workloadv1alpha1.ResourceStateSync)}),
		)

		subresourceStorages = make(map[string]rest.Storage)
		if statusEnabled {
			subresourceStorages["status"] = storage.Status
		}

		// TODO(sttts): add scale subresource

		return storage.CustomResource, subresourceStorages
	}
}

func wrapStorageWithLabelSelector(labelSelector map[string]string) registry.StorageWrapper {
	return func(resource schema.GroupResource, storage customresource.Store) customresource.Store {
		requirements, selectable := labels.SelectorFromSet(labels.Set(labelSelector)).Requirements()
		if !selectable {
			// we can't return an error here since this ends up inside of the k8s apiserver code where
			// no errors are expected, so the best we can do is panic - this is likely ok since there's
			// no real way that the syncer virtual workspace would ever create an unselectable selector
			panic(fmt.Sprintf("creating a new store with an unselectable set: %v", labelSelector))
		}

		return &LabelSelectingStore{
			DefaultQualifiedResource: resource,
			Store:                    storage,
			filter:                   requirements,
		}
	}
}

type LabelSelectingStore struct {
	// DefaultQualifiedResource is the pluralized name of the resource.
	// This field is used if there is no request info present in the context.
	// See qualifiedResourceFromContext for details.
	DefaultQualifiedResource schema.GroupResource

	// this is the storage we're wrapping
	customresource.Store

	filter labels.Requirements
}

var _ customresource.Store = &LabelSelectingStore{}

// List returns a list of items matching labels and field according to the store's PredicateFunc.
func (s *LabelSelectingStore) List(ctx context.Context, options *metainternalversion.ListOptions) (runtime.Object, error) {
	selector := options.LabelSelector
	if selector == nil {
		selector = labels.Everything()
	}
	options.LabelSelector = selector.Add(s.filter...)
	return s.Store.List(ctx, options)
}

// Get implements rest.Getter
func (s *LabelSelectingStore) Get(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
	obj, err := s.Store.Get(ctx, name, options)
	if err != nil {
		return obj, err
	}

	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return nil, fmt.Errorf("expected a metav1.Object, got %T", obj)
	}
	if !labels.Everything().Add(s.filter...).Matches(labels.Set(metaObj.GetLabels())) {
		return nil, kerrors.NewNotFound(s.DefaultQualifiedResource, name)
	}

	return obj, err
}

// Watch implements rest.Watcher.
func (s *LabelSelectingStore) Watch(ctx context.Context, options *metainternalversion.ListOptions) (watch.Interface, error) {
	selector := options.LabelSelector
	if selector == nil {
		selector = labels.Everything()
	}
	options.LabelSelector = selector.Add(s.filter...)
	return s.Store.Watch(ctx, options)
}
