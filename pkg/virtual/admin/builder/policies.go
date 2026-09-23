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
	"context"
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apiextensions-apiserver/pkg/registry/customresource"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apidefinition"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apiserver"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/forwardingregistry"

	configcrds "github.com/kcp-dev/kcp/config/crds"
	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	cachebootstrap "github.com/kcp-dev/kcp/pkg/cache/server/bootstrap"
	"github.com/kcp-dev/kcp/pkg/virtual/admin"
)

// PermissionClaimPoliciesSchema is the served schema of the
// permissionclaimpolicies view, derived from the CRD.
var PermissionClaimPoliciesSchema *apisv1alpha1.APIResourceSchema

func init() {
	crd := apiextensionsv1.CustomResourceDefinition{}
	if err := configcrds.Unmarshal("admin.kcp.io_permissionclaimpolicies.yaml", &crd); err != nil {
		panic(fmt.Sprintf("failed to unmarshal permissionclaimpolicies CRD: %v", err))
	}
	s, err := apisv1alpha1.CRDToAPIResourceSchema(&crd, "crd")
	if err != nil {
		panic(fmt.Sprintf("failed to convert CRD %s.%s to APIResourceSchema: %v", crd.Spec.Names.Plural, crd.Spec.Group, err))
	}
	PermissionClaimPoliciesSchema = s
}

// providePermissionClaimPoliciesRestStorage builds the REST storage for
// PermissionClaimPolicies. Unlike Shards, these objects are owned by the Admin
// workspace itself: they exist only in the cache server, under the synthetic
// logical cluster system:global-admin and the cache server's own shard, and are
// created, updated and deleted through this view. Every shard reads them back
// through its global (cache-backed) informers.
func providePermissionClaimPoliciesRestStorage(
	mainConfig genericapiserver.CompletedConfig,
	cacheDynamicClusterClient kcpdynamic.ClusterInterface,
) (apidefinition.APIDefinition, error) {
	ctx, cancelFn := context.WithCancel(context.Background())

	clientFunc := forwardingregistry.DynamicClusterClientFunc(func(_ context.Context) (kcpdynamic.ClusterInterface, error) {
		return cacheDynamicClusterClient, nil
	})

	restProvider := policiesRestProvider(ctx, clientFunc, withAdminOwnedStorage())
	def, err := apiserver.CreateServingInfoFor(mainConfig, PermissionClaimPoliciesSchema, adminv1alpha1.SchemeGroupVersion.Version, restProvider)
	if err != nil {
		cancelFn()
		return nil, err
	}

	return &apiDefinitionWithCancel{
		APIDefinition: def,
		cancelFn:      cancelFn,
	}, nil
}

// policiesRestProvider exposes full CRUD on the forwarded storage.
func policiesRestProvider(ctx context.Context, dynamicClusterClientFunc forwardingregistry.DynamicClusterClientFunc, wrapper forwardingregistry.StorageWrapper) apiserver.RestProviderFunc {
	return func(resource schema.GroupVersionResource, kind schema.GroupVersionKind, listKind schema.GroupVersionKind, typer runtime.ObjectTyper, tableConvertor rest.TableConvertor, namespaceScoped bool, schemaValidator validation.SchemaValidator, subresourcesSchemaValidator map[string]validation.SchemaValidator, structuralSchema *structuralschema.Structural) (mainStorage rest.Storage, subresourceStorages map[string]rest.Storage) {
		strategy := customresource.NewStrategy(
			typer,
			namespaceScoped,
			kind,
			forwardingregistry.ValidatePathSegmentName,
			schemaValidator,
			subresourcesSchemaValidator["status"],
			structuralSchema,
			nil, // no status here
			nil, // no scale here
			[]apiextensionsv1.SelectableField{},
		)

		storage, _, _ := forwardingregistry.NewStorage(
			ctx,
			resource,
			"",
			kind,
			listKind,
			strategy,
			nil,
			tableConvertor,
			nil,
			dynamicClusterClientFunc,
			nil,
			wrapper,
		)

		return &struct {
			forwardingregistry.FactoryFunc
			forwardingregistry.ListFactoryFunc
			forwardingregistry.DestroyerFunc

			forwardingregistry.GetterFunc
			forwardingregistry.ListerFunc
			forwardingregistry.WatcherFunc
			forwardingregistry.CreaterFunc
			forwardingregistry.UpdaterFunc
			forwardingregistry.GracefulDeleterFunc
			forwardingregistry.CollectionDeleterFunc

			forwardingregistry.TableConvertorFunc
			forwardingregistry.CategoriesProviderFunc
			forwardingregistry.ResetFieldsStrategyFunc
		}{
			FactoryFunc:     storage.FactoryFunc,
			ListFactoryFunc: storage.ListFactoryFunc,
			DestroyerFunc:   storage.DestroyerFunc,

			GetterFunc:            storage.GetterFunc,
			ListerFunc:            storage.ListerFunc,
			WatcherFunc:           storage.WatcherFunc,
			CreaterFunc:           storage.CreaterFunc,
			UpdaterFunc:           storage.UpdaterFunc,
			GracefulDeleterFunc:   storage.GracefulDeleterFunc,
			CollectionDeleterFunc: storage.CollectionDeleterFunc,

			TableConvertorFunc:      storage.TableConvertorFunc,
			CategoriesProviderFunc:  storage.CategoriesProviderFunc,
			ResetFieldsStrategyFunc: storage.ResetFieldsStrategyFunc,
		}, nil // no subresources
	}
}

// adminOwnedContext pins every forwarded request to the synthetic
// system:global-admin logical cluster on the cache server's own shard, regardless of what cluster
// segment the caller used against /services/admin.
func adminOwnedContext(ctx context.Context) context.Context {
	ctx = genericapirequest.WithCluster(ctx, genericapirequest.Cluster{Name: admin.GlobalAdminCluster})
	return cacheclient.WithShardInContext(ctx, shard.New(cachebootstrap.SystemCacheServerShard))
}

// stripCacheBookkeeping removes the cache server's shard annotation from an
// object handed back to the caller.
func stripCacheBookkeeping(obj runtime.Object) {
	if u, ok := obj.(*unstructured.Unstructured); ok {
		stripField(u, []string{"metadata", "annotations", shard.AnnotationKey})
	}
}

// withAdminOwnedStorage rewrites every request onto the admin-owned location
// in the cache and strips cache bookkeeping from what comes back.
func withAdminOwnedStorage() forwardingregistry.StorageWrapper {
	return forwardingregistry.StorageWrapperFunc(func(_ schema.GroupResource, storage *forwardingregistry.StoreFuncs) {
		delegateGet := storage.GetterFunc
		storage.GetterFunc = func(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
			obj, err := delegateGet(adminOwnedContext(ctx), name, options)
			if err != nil {
				return nil, err
			}
			stripCacheBookkeeping(obj)
			return obj, nil
		}

		delegateList := storage.ListerFunc
		storage.ListerFunc = func(ctx context.Context, options *metainternalversion.ListOptions) (runtime.Object, error) {
			result, err := delegateList(adminOwnedContext(ctx), options)
			if err != nil {
				return nil, err
			}
			if list, ok := result.(*unstructured.UnstructuredList); ok {
				for i := range list.Items {
					stripField(&list.Items[i], []string{"metadata", "annotations", shard.AnnotationKey})
				}
			}
			return result, nil
		}

		delegateWatch := storage.WatcherFunc
		storage.WatcherFunc = func(ctx context.Context, options *metainternalversion.ListOptions) (watch.Interface, error) {
			w, err := delegateWatch(adminOwnedContext(ctx), options)
			if err != nil {
				return nil, err
			}
			return watch.Filter(w, func(in watch.Event) (watch.Event, bool) {
				stripCacheBookkeeping(in.Object)
				return in, true
			}), nil
		}

		delegateCreate := storage.CreaterFunc
		storage.CreaterFunc = func(ctx context.Context, obj runtime.Object, createValidation rest.ValidateObjectFunc, options *metav1.CreateOptions) (runtime.Object, error) {
			result, err := delegateCreate(adminOwnedContext(ctx), obj, createValidation, options)
			if err != nil {
				return nil, err
			}
			stripCacheBookkeeping(result)
			return result, nil
		}

		delegateUpdate := storage.UpdaterFunc
		storage.UpdaterFunc = func(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo, createValidation rest.ValidateObjectFunc, updateValidation rest.ValidateObjectUpdateFunc, forceAllowCreate bool, options *metav1.UpdateOptions) (runtime.Object, bool, error) {
			result, created, err := delegateUpdate(adminOwnedContext(ctx), name, objInfo, createValidation, updateValidation, forceAllowCreate, options)
			if err != nil {
				return nil, false, err
			}
			stripCacheBookkeeping(result)
			return result, created, nil
		}

		delegateDelete := storage.GracefulDeleterFunc
		storage.GracefulDeleterFunc = func(ctx context.Context, name string, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions) (runtime.Object, bool, error) {
			result, done, err := delegateDelete(adminOwnedContext(ctx), name, deleteValidation, options)
			if err != nil {
				return nil, false, err
			}
			stripCacheBookkeeping(result)
			return result, done, nil
		}

		delegateDeleteCollection := storage.CollectionDeleterFunc
		storage.CollectionDeleterFunc = func(ctx context.Context, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions, listOptions *metainternalversion.ListOptions) (runtime.Object, error) {
			return delegateDeleteCollection(adminOwnedContext(ctx), deleteValidation, options, listOptions)
		}
	})
}
