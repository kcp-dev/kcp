/*
Copyright 2025 The KCP Authors.

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

package cachedresource

import (
	"context"
	"fmt"
	"io"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/admission"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"

	"github.com/kcp-dev/logicalcluster/v3"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"

	"github.com/kcp-dev/kcp/pkg/indexers"
	cachedresourcesreconciler "github.com/kcp-dev/kcp/pkg/reconciler/cache/cachedresources"
	"github.com/kcp-dev/kcp/pkg/reconciler/dynamicrestmapper"
)

// PluginName is the name used to identify this admission webhook.
const PluginName = "apis.kcp.io/CachedResource"

// Ensure that the required admission interfaces are implemented.
var (
	_ = admission.ValidationInterface(&CachedResourceAdmission{})
	_ = admission.InitializationValidator(&CachedResourceAdmission{})
)

// Register registers the reserved name admission webhook.
func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			p := &CachedResourceAdmission{
				Handler: admission.NewHandler(admission.Create),
			}
			p.listCachedResourcesByGVR = func(cluster logicalcluster.Name, gvr schema.GroupVersionResource) ([]*cachev1alpha1.CachedResource, error) {
				return indexers.ByIndex[*cachev1alpha1.CachedResource](
					p.localCachedResourcesIndexer,
					cachedresourcesreconciler.ByGVRAndLogicalCluster,
					cachedresourcesreconciler.GVRAndLogicalClusterKey(gvr, cluster),
				)
			}

			return p, nil
		})
}

// CachedResourceAdmission is an admission plugin for checking APIExport validity.
type CachedResourceAdmission struct {
	*admission.Handler

	listCachedResourcesByGVR    func(cluster logicalcluster.Name, gvr schema.GroupVersionResource) ([]*cachev1alpha1.CachedResource, error)
	localCachedResourcesIndexer cache.Indexer

	dynamicRESTMapper *dynamicrestmapper.DynamicRESTMapper
}

func (adm *CachedResourceAdmission) SetKcpInformers(local, global kcpinformers.SharedInformerFactory) {
	adm.SetReadyFunc(func() bool {
		return local.Cache().V1alpha1().CachedResources().Informer().HasSynced()
	})
	adm.localCachedResourcesIndexer = local.Cache().V1alpha1().CachedResources().Informer().GetIndexer()

	indexers.AddIfNotPresentOrDie(local.Cache().V1alpha1().CachedResources().Informer().GetIndexer(), cache.Indexers{
		cachedresourcesreconciler.ByGVRAndLogicalCluster: cachedresourcesreconciler.IndexByGVRAndLogicalCluster,
	})
}

func (adm *CachedResourceAdmission) SetDynamicRESTMapper(dynamicRESTMapper *dynamicrestmapper.DynamicRESTMapper) {
	adm.dynamicRESTMapper = dynamicRESTMapper
}

// ValidateInitialization ensures the required injected fields are set.
func (adm *CachedResourceAdmission) ValidateInitialization() error {
	if adm.localCachedResourcesIndexer == nil {
		return fmt.Errorf(PluginName + " plugin needs a CachedResource indexer")
	}
	return nil
}

// Validate ensures that the APIExport is valid.
func (adm *CachedResourceAdmission) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) error {
	if a.GetResource().GroupResource() != cachev1alpha1.Resource("cachedresources") || a.GetKind().GroupKind() != cachev1alpha1.Kind("CachedResource") {
		return nil
	}
	if a.GetOperation() != admission.Create {
		return nil
	}

	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}

	cachedResource := &cachev1alpha1.CachedResource{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, cachedResource); err != nil {
		return fmt.Errorf("failed to convert unstructured to CachedResource: %w", err)
	}

	return adm.validateV1alpha1(ctx, a, cachedResource)
}

func (adm *CachedResourceAdmission) validateV1alpha1(ctx context.Context, a admission.Attributes, cachedResource *cachev1alpha1.CachedResource) error {
	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return apierrors.NewInternalError(err)
	}

	gvr := schema.GroupVersionResource(cachedResource.Spec.GroupVersionResource)

	// We check that the resource in the CachedResource is cluster-scoped.
	// This is only advisory as the real check is done by CachedResource's controller,
	// which sets a condition if the resource is not cluster-scoped.
	scopedDynRESTMapper := adm.dynamicRESTMapper.ForCluster(clusterName)
	kind, err := scopedDynRESTMapper.KindFor(gvr)
	if err == nil {
		mapping, err := scopedDynRESTMapper.RESTMapping(kind.GroupKind(), kind.Version)
		if err == nil {
			if mapping.Scope != meta.RESTScopeRoot {
				return admission.NewForbidden(a,
					field.Invalid(
						field.NewPath("spec"),
						gvr.GroupResource().String(),
						"Resource referenced in CachedResource must be cluster-scoped",
					),
				)
			}
		}
	}

	existing, err := adm.listCachedResourcesByGVR(clusterName, gvr)
	if err != nil {
		return err
	}

	// Make sure there is at most one CachedResource per GVR.
	if len(existing) > 0 {
		return admission.NewForbidden(a,
			field.Invalid(
				field.NewPath("spec"),
				fmt.Sprintf("%s.%s.%s", gvr.Group, gvr.Version, gvr.Resource),
				fmt.Sprintf("CachedResource with this GVR already exists in the %q workspace", clusterName)),
		)
	}

	return nil
}
