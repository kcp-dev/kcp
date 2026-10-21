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
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utiljson "k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apidefinition"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apiserver"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/forwardingregistry"

	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
)

// provideAPIIdentitiesRestStorage builds the REST storage for the
// apiidentities view: GET/LIST/WATCH forwarded to the cache server with
// shard-wildcard scope, so a single request returns/streams every shard's
// APIIdentity objects as one registry, plus UPDATE restricted to spec (the
// operator knobs of the legacy conversion), which is applied to the cache
// copy and picked up from there by the shard hosting the authoritative
// object. Everything else is derived by that shard and read-only.
func provideAPIIdentitiesRestStorage(
	mainConfig genericapiserver.CompletedConfig,
	cacheDynamicClusterClient kcpdynamic.ClusterInterface,
) (apidefinition.APIDefinition, error) {
	ctx, cancelFn := context.WithCancel(context.Background())
	clientFunc := forwardingregistry.DynamicClusterClientFunc(func(_ context.Context) (kcpdynamic.ClusterInterface, error) {
		return cacheDynamicClusterClient, nil
	})
	gr := adminv1alpha1.Resource("apiidentities")
	wrapper := forwardingregistry.StorageWrapperFunc(func(resource schema.GroupResource, storage *forwardingregistry.StoreFuncs) {
		withAggregateReadView(gr).Decorate(resource, storage)
		withSpecUpdate(cacheDynamicClusterClient, gr).Decorate(resource, storage)
	})
	restProvider := shardsRestProvider(ctx, clientFunc, wrapper)
	def, err := apiserver.CreateServingInfoFor(mainConfig, APIIdentitiesSchema, adminv1alpha1.SchemeGroupVersion.Version, restProvider)
	if err != nil {
		cancelFn()
		return nil, err
	}
	return &apiDefinitionWithCancel{
		APIDefinition: def,
		cancelFn:      cancelFn,
	}, nil
}

// withSpecUpdate adds an UpdaterFunc that accepts changes to spec only,
// decoded through the APIIdentitySpec type, and applies them to the cache
// copy of the object, from where the replication controller carries spec back
// to the shard hosting the authoritative object (spec is a cache-owned field
// of apiidentities).
func withSpecUpdate(cacheDynamicClusterClient kcpdynamic.ClusterInterface, gr schema.GroupResource) forwardingregistry.StorageWrapper {
	return forwardingregistry.StorageWrapperFunc(func(_ schema.GroupResource, storage *forwardingregistry.StoreFuncs) {
		// rawByName returns the cache copy with its bookkeeping annotations
		// intact, so the write can be routed to the exact cluster and shard
		// the copy lives under. The aggregate view's lister has already
		// stripped them, so go to the delegate.
		rawByName := func(ctx context.Context, name string) (*unstructured.Unstructured, error) {
			rawList, err := cacheDynamicClusterClient.Cluster(logicalcluster.Wildcard).
				Resource(adminv1alpha1.SchemeGroupVersion.WithResource(gr.Resource)).
				List(cacheclient.WithShardInContext(ctx, shard.Wildcard), metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			items := dedupeShards(rawList.Items)
			for i := range items {
				if items[i].GetName() == name {
					return &items[i], nil
				}
			}
			return nil, apierrors.NewNotFound(gr, name)
		}
		storage.UpdaterFunc = func(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo, _ rest.ValidateObjectFunc, updateValidation rest.ValidateObjectUpdateFunc, _ bool, options *metav1.UpdateOptions) (runtime.Object, bool, error) {
			raw, err := rawByName(ctx, name)
			if err != nil {
				return nil, false, err
			}
			current := raw.DeepCopy()
			fixupShard(current)
			updatedObj, err := objInfo.UpdatedObject(ctx, current)
			if err != nil {
				return nil, false, err
			}
			desired, ok := updatedObj.(*unstructured.Unstructured)
			if !ok {
				return nil, false, apierrors.NewBadRequest(fmt.Sprintf("unexpected object type %T", updatedObj))
			}
			if updateValidation != nil {
				if err := updateValidation(ctx, desired, current); err != nil {
					return nil, false, err
				}
			}
			spec, present, err := specUpdate(gr, name, current, desired)
			if err != nil {
				return nil, false, err
			}
			if !present {
				unstructured.RemoveNestedField(raw.Object, "spec")
			} else if err := unstructured.SetNestedField(raw.Object, runtime.DeepCopyJSONValue(spec), "spec"); err != nil {
				return nil, false, err
			}
			targetCtx := cacheclient.WithShardInContext(ctx, shard.Name(raw.GetAnnotations()[shard.AnnotationKey]))
			result, err := cacheDynamicClusterClient.Cluster(logicalcluster.From(raw).Path()).
				Resource(adminv1alpha1.SchemeGroupVersion.WithResource(gr.Resource)).
				Update(targetCtx, raw, *options)
			if err != nil {
				return nil, false, err
			}
			fixupShard(result)
			return result, false, nil
		}
	})
}

// specUpdate checks that desired differs from current in spec only and
// returns the desired spec decoded through APIIdentitySpec, re-encoded, along
// with whether it is present at all.
func specUpdate(gr schema.GroupResource, name string, current, desired *unstructured.Unstructured) (any, bool, error) {
	value, found, err := unstructured.NestedFieldNoCopy(desired.Object, "spec")
	if err != nil {
		return nil, false, apierrors.NewBadRequest(fmt.Sprintf("invalid spec: %v", err))
	}
	if found {
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, false, err
		}
		typed := &adminv1alpha1.APIIdentitySpec{}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(typed); err != nil {
			return nil, false, apierrors.NewInvalid(adminv1alpha1.Kind("APIIdentity"), name, field.ErrorList{
				field.Invalid(field.NewPath("spec"), value, err.Error()),
			})
		}
		if raw, err = json.Marshal(typed); err != nil {
			return nil, false, err
		}
		if err := utiljson.Unmarshal(raw, &value); err != nil {
			return nil, false, err
		}
	}
	normalizedDesired := desired.DeepCopy()
	normalizedCurrent := current.DeepCopy()
	unstructured.RemoveNestedField(normalizedDesired.Object, "spec")
	unstructured.RemoveNestedField(normalizedCurrent.Object, "spec")
	unstructured.RemoveNestedField(normalizedDesired.Object, "metadata", "managedFields")
	unstructured.RemoveNestedField(normalizedCurrent.Object, "metadata", "managedFields")
	if !equality.Semantic.DeepEqual(normalizedDesired.Object, normalizedCurrent.Object) {
		return nil, false, apierrors.NewForbidden(gr, name,
			fmt.Errorf("only spec may be changed through the Admin workspace; the rest of an APIIdentity is derived from the APIExports"))
	}
	return value, found, nil
}
