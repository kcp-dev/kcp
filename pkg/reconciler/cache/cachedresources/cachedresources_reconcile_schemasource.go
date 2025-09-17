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

package cachedresources

import (
	"context"
	"fmt"

	apiextensionshelpers "k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
)

type schemaSource struct {
	getLogicalCluster    func(cluster logicalcluster.Name) (*corev1alpha1.LogicalCluster, error)
	getAPIBinding        func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIBinding, error)
	getAPIExport         func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error)
	getAPIResourceSchema func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error)
	listCRDsByGR         func(cluster logicalcluster.Name, gr schema.GroupResource) ([]*apiextensionsv1.CustomResourceDefinition, error)
}

func (r *schemaSource) reconcile(ctx context.Context, cachedResource *cachev1alpha1.CachedResource) (reconcileStatus, error) {
	logger := klog.FromContext(ctx)

	if !cachedResource.DeletionTimestamp.IsZero() {
		return reconcileStatusContinue, nil
	}

	cachedResource.Status.ResourceSchemaSource = nil
	cachedResource.Status.Schema = ""
	conditions.Delete(cachedResource, cachev1alpha1.CachedResourceSourceSchemaReplicated)

	gvr := schema.GroupVersionResource{
		Group:    cachedResource.Spec.Group,
		Version:  cachedResource.Spec.Version,
		Resource: cachedResource.Spec.Resource,
	}
	cluster := logicalcluster.From(cachedResource)

	// Find out where the schema comes from.

	// Start with inspecting the LogicalCluster to find out if we have an associated
	// APIBinding for this GR, which could mean the schema comes from an APIResourceSchema.

	lc, err := r.getLogicalCluster(cluster)
	if err != nil {
		return reconcileStatusStopAndRequeue, err
	}

	boundResources, err := apibinding.GetResourceBindings(lc)
	if err != nil {
		return reconcileStatusStopAndRequeue, err
	}

	notReadyCond := conditions.FalseCondition(
		cachev1alpha1.CachedResourceSchemaSourceValid,
		cachev1alpha1.SchemaSourceNotReadyReason,
		conditionsv1alpha1.ConditionSeverityError,
		"API not ready.",
	)

	if bindingLock, found := boundResources[gvr.GroupResource().String()]; found {
		if bindingLock.Name != "" {
			// This resource's schema originates from an APIResourceSchema
			// because we have an associated APIBinding.

			apiBinding, err := r.getAPIBinding(cluster, bindingLock.Name)
			if err != nil {
				return reconcileStatusStopAndRequeue, err
			}

			apiExport, err := r.getAPIExport(logicalcluster.NewPath(apiBinding.Spec.Reference.Export.Path), apiBinding.Spec.Reference.Export.Name)
			if err != nil {
				return reconcileStatusStopAndRequeue, err
			}

			var schemaName string
			for _, res := range apiExport.Spec.Resources {
				if res.Group != gvr.Group || res.Name != gvr.Resource {
					continue
				}
				if res.Storage.CRD == nil {
					conditions.MarkFalse(
						cachedResource,
						cachev1alpha1.CachedResourceSchemaSourceValid,
						cachev1alpha1.SchemaSourceInvalidReason,
						conditionsv1alpha1.ConditionSeverityError,
						"Schema %s in APIExport %s:%s is incompatible. Please contact the APIExport owner to resolve.",
						res.Schema,
						apiBinding.Spec.Reference.Export.Path,
						apiBinding.Spec.Reference.Export.Name,
					)
					return reconcileStatusStop, nil
				}
				schemaName = res.Schema
				break
			}

			if schemaName == "" {
				// The LogicalCluster is holding a lock for a GR that is
				// not defined in the associated APIExport???
				conditions.MarkFalse(
					cachedResource,
					cachev1alpha1.CachedResourceSchemaSourceValid,
					cachev1alpha1.SchemaSourceInvalidReason,
					conditionsv1alpha1.ConditionSeverityError,
					"No valid schema available in APIExport %s:%s. Please contact the APIExport owner to resolve.",
					apiBinding.Spec.Reference.Export.Path,
					apiBinding.Spec.Reference.Export.Name,
				)
				return reconcileStatusStop, nil
			}

			sourceSchema, err := r.getAPIResourceSchema(logicalcluster.From(apiExport), schemaName)
			if err != nil {
				return reconcileStatusStopAndRequeue, err
			}

			var hasRequestedVersion bool
			for i := range sourceSchema.Spec.Versions {
				if sourceSchema.Spec.Versions[i].Name == gvr.Version {
					hasRequestedVersion = true
					break
				}
			}

			if !hasRequestedVersion {
				conditions.MarkFalse(
					cachedResource,
					cachev1alpha1.CachedResourceSchemaSourceValid,
					cachev1alpha1.SchemaSourceInvalidReason,
					conditionsv1alpha1.ConditionSeverityError,
					"Schema %s in APIExport %s:%s does not define the requested resource version. Please contact the APIExport owner to resolve.",
					schemaName,
					apiBinding.Spec.Reference.Export.Path,
					apiBinding.Spec.Reference.Export.Name,
				)
				return reconcileStatusStop, nil
			}

			conditions.MarkTrue(cachedResource, cachev1alpha1.CachedResourceSchemaSourceValid)
			cachedResource.Status.ResourceSchemaSource = &cachev1alpha1.CachedResourceSchemaSource{
				APIResourceSchema: &cachev1alpha1.APIResourceSchemaSource{
					ClusterName: logicalcluster.From(sourceSchema).String(),
					Name:        sourceSchema.Name,
				},
			}
			cachedResource.Status.Schema = CachedAPIResourceSchemaName(cachedResource.UID, gvr.GroupResource())

			return reconcileStatusContinue, nil
		} else if bindingLock.CRD {
			// The resource is backed by a CRD. Fall through to find that CRD.
		} else {
			// This should never happen! Neither APIBinding or CRD are present in the binding lock.
			// We can drop this item from the queue. We'll try again once the LogicalCluster annotation is updated.

			logger.Error(nil, "failed to process bindings annotation on LogicalCluster",
				"LogicalCluster", fmt.Sprintf("%s|%s", cluster, corev1alpha1.LogicalClusterName),
				"annotationKey", apibinding.ResourceBindingsAnnotationKey,
				"annotation", lc.Annotations[apibinding.ResourceBindingsAnnotationKey])
			conditions.Set(cachedResource, notReadyCond)
			return reconcileStatusStop, nil
		}
	}

	// Maybe it's a CRD?

	crds, err := r.listCRDsByGR(cluster, gvr.GroupResource())
	if err != nil {
		return reconcileStatusStopAndRequeue, err
	}

	if len(crds) != 1 {
		// Zero means there is nothing serving this GR at the moment, and >1 is impossible.
		conditions.Set(cachedResource, notReadyCond)
		return reconcileStatusStop, nil
	}

	// It's definitely a CRD!

	crd := crds[0]

	if apiextensionshelpers.IsCRDConditionFalse(crd, apiextensionsv1.Established) {
		conditions.Set(cachedResource, notReadyCond)
		return reconcileStatusStop, nil
	}

	var hasRequestedVersion bool
	for _, version := range crd.Status.StoredVersions {
		if version == gvr.Version {
			hasRequestedVersion = true
			break
		}
	}
	if !hasRequestedVersion {
		conditions.MarkFalse(
			cachedResource,
			cachev1alpha1.CachedResourceSchemaSourceValid,
			cachev1alpha1.SchemaSourceInvalidReason,
			conditionsv1alpha1.ConditionSeverityError,
			"CRD %s does not define the requested resource version.",
			crd.Name,
		)
		return reconcileStatusStop, nil
	}

	conditions.MarkTrue(cachedResource, cachev1alpha1.CachedResourceSchemaSourceValid)
	cachedResource.Status.ResourceSchemaSource = &cachev1alpha1.CachedResourceSchemaSource{
		CRD: &cachev1alpha1.CRDSchemaSource{
			Name: crd.Name,
		},
	}
	cachedResource.Status.Schema = CachedAPIResourceSchemaName(cachedResource.UID, gvr.GroupResource())

	return reconcileStatusStopAndRequeue, nil
}
