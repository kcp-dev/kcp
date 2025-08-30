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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
)

type replicateResourceSchema struct {
	getAPIResourceSchema      func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error)
	getLocalAPIResourceSchema func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error)
	getCRD                    func(ctx context.Context, cluster logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error)

	createCachedAPIResourceSchema func(ctx context.Context, cluster logicalcluster.Name, sch *apisv1alpha1.APIResourceSchema) error
	updateCreateAPIResourceSchema func(ctx context.Context, cluster logicalcluster.Name, sch *apisv1alpha1.APIResourceSchema) error
}

func CachedAPIResourceSchemaName(cachedResourceUID types.UID, gr schema.GroupResource) string {
	return fmt.Sprintf("cachedresources-cache-kcp-io-%s.%s", cachedResourceUID, gr.String())
}

func ownAPIResourceSchema(cr *cachev1alpha1.CachedResource, sch *apisv1alpha1.APIResourceSchema) {
	sch.Name = CachedAPIResourceSchemaName(cr.UID, schema.GroupResource{
		Group:    cr.Spec.Group,
		Resource: cr.Spec.Resource,
	})
	sch.OwnerReferences = []metav1.OwnerReference{
		{
			APIVersion: cachev1alpha1.SchemeGroupVersion.String(),
			Kind:       "CachedResource",
			Name:       cr.Name,
			UID:        cr.UID,
		},
	}
}

func (r *replicateResourceSchema) reconcile(ctx context.Context, cachedResource *cachev1alpha1.CachedResource) (reconcileStatus, error) {
	if !cachedResource.DeletionTimestamp.IsZero() {
		return reconcileStatusContinue, nil
	}
	if conditions.IsFalse(cachedResource, cachev1alpha1.CachedResourceSchemaSourceValid) {
		return reconcileStatusStopAndRequeue, nil
	}
	logger := klog.FromContext(ctx)

	gvr := schema.GroupVersionResource{
		Group:    cachedResource.Spec.Group,
		Version:  cachedResource.Spec.Version,
		Resource: cachedResource.Spec.Resource,
	}

	// We need to find out if we have the source schema replicated.

	// Get only the local schema. Global informer may be lagging behind
	// and return the schema even if it's been already deleted locally.
	_, err := r.getLocalAPIResourceSchema(logicalcluster.From(cachedResource), CachedAPIResourceSchemaName(cachedResource.UID, gvr.GroupResource()))
	replicatedSchemaNotFound := apierrors.IsNotFound(err)
	if err != nil && !replicatedSchemaNotFound {
		logger.Error(err, "failed to get replicated APIResourceSchema")
		return reconcileStatusStopAndRequeue, err
	}

	if cachedResource.Status.ResourceSchemaSource.APIResourceSchema != nil {
		if replicatedSchemaNotFound {
			// We don't, so it needs to be created.

			sourceSchema, err := r.getAPIResourceSchema(logicalcluster.Name(cachedResource.Status.ResourceSchemaSource.APIResourceSchema.ClusterName), cachedResource.Status.ResourceSchemaSource.APIResourceSchema.Name)
			if err != nil {
				logger.Error(err, "failed to get source APIResourceSchema")
				conditions.MarkFalse(
					cachedResource,
					cachev1alpha1.CachedResourceSourceSchemaReplicated,
					cachev1alpha1.SourceSchemaReplicatedFailedReason,
					conditionsv1alpha1.ConditionSeverityError,
					"Failed to get source APIResourceSchema: %v.",
					err,
				)
				return reconcileStatusStopAndRequeue, err
			}

			if !validateAPIResourceSchemaForGVR(sourceSchema, gvr) {
				conditions.MarkFalse(
					cachedResource,
					cachev1alpha1.CachedResourceSchemaSourceValid,
					cachev1alpha1.SchemaSourceInvalidReason,
					conditionsv1alpha1.ConditionSeverityError,
					"Schema is not valid. Please contact the APIExport owner to resolve.",
				)
				return reconcileStatusStop, nil
			}

			sch := sourceSchema.DeepCopy()
			sch.ResourceVersion = "" // This is a copy, we need to reset the RV.
			ownAPIResourceSchema(cachedResource, sch)

			if err = r.createCachedAPIResourceSchema(ctx, logicalcluster.From(cachedResource), sch); err != nil {
				logger.Error(err, "failed to create the cached APIResourceSchema")
				if !apierrors.IsAlreadyExists(err) {
					conditions.MarkFalse(
						cachedResource,
						cachev1alpha1.CachedResourceSourceSchemaReplicated,
						cachev1alpha1.SourceSchemaReplicatedFailedReason,
						conditionsv1alpha1.ConditionSeverityError,
						"Failed to store schema: %v.",
						err,
					)
					return reconcileStatusStopAndRequeue, err
				}
			}

			conditions.MarkTrue(cachedResource, cachev1alpha1.CachedResourceSourceSchemaReplicated)
			return reconcileStatusStopAndRequeue, nil
		}

		// The replicated APIResoureSchema already exists.
		// No need to check for updates because it is immutable.
		return reconcileStatusContinue, nil
	}

	if cachedResource.Status.ResourceSchemaSource.CRD != nil {
		crd, err := r.getCRD(ctx, logicalcluster.From(cachedResource), cachedResource.Status.ResourceSchemaSource.CRD.Name)
		if err != nil {
			logger.Error(err, "failed to get source CRD")
			conditions.MarkFalse(
				cachedResource,
				cachev1alpha1.CachedResourceSourceSchemaReplicated,
				cachev1alpha1.SourceSchemaReplicatedFailedReason,
				conditionsv1alpha1.ConditionSeverityError,
				"Failed to get source CRD: %v.",
				err,
			)
			return reconcileStatusStopAndRequeue, err
		}

		if apiextensionshelpers.IsCRDConditionFalse(crd, apiextensionsv1.Established) {
			conditions.MarkFalse(
				cachedResource,
				cachev1alpha1.CachedResourceSchemaSourceValid,
				cachev1alpha1.SchemaSourceNotReadyReason,
				conditionsv1alpha1.ConditionSeverityError,
				"API not ready.",
			)
			return reconcileStatusStop, nil
		}

		if !validateCRDForGVR(crd, gvr) {
			conditions.MarkFalse(
				cachedResource,
				cachev1alpha1.CachedResourceSchemaSourceValid,
				cachev1alpha1.SchemaSourceInvalidReason,
				conditionsv1alpha1.ConditionSeverityError,
				"CRD %s does not define the requested group, resource or version.",
				crd.Name,
			)
			return reconcileStatusStop, nil
		}

		if replicatedSchemaNotFound || crd.ObjectMeta.ResourceVersion != cachedResource.Status.ResourceSchemaSource.CRD.ResourceVersion {
			// Either the replicated schema doesn't exist, or it needs updating.

			sourceSchema, err := apisv1alpha1.CRDToAPIResourceSchema(crd, "prefix")
			if err != nil {
				logger.Error(err, "failed to convert CRD to APIResourceSchema")
				conditions.MarkFalse(
					cachedResource,
					cachev1alpha1.CachedResourceSourceSchemaReplicated,
					cachev1alpha1.SourceSchemaReplicatedFailedReason,
					conditionsv1alpha1.ConditionSeverityError,
					"Internal error while processing source CRD.",
				)
				return reconcileStatusStopAndRequeue, err
			}
			ownAPIResourceSchema(cachedResource, sourceSchema)

			if replicatedSchemaNotFound {
				if err = r.createCachedAPIResourceSchema(ctx, logicalcluster.From(cachedResource), sourceSchema); err != nil {
					logger.Error(err, "failed to replicate APIResourceSchema")
					if !apierrors.IsAlreadyExists(err) {
						conditions.MarkFalse(
							cachedResource,
							cachev1alpha1.CachedResourceSourceSchemaReplicated,
							cachev1alpha1.SourceSchemaReplicatedFailedReason,
							conditionsv1alpha1.ConditionSeverityError,
							"Failed to replicate schema: %v.",
							err,
						)
						return reconcileStatusStopAndRequeue, err
					}
				}
			} else {
				if err = r.updateCreateAPIResourceSchema(ctx, logicalcluster.From(cachedResource), sourceSchema); err != nil {
					logger.Error(err, "failed to update the replicated APIResourceSchema")
					conditions.MarkFalse(
						cachedResource,
						cachev1alpha1.CachedResourceSourceSchemaReplicated,
						cachev1alpha1.SourceSchemaReplicatedFailedReason,
						conditionsv1alpha1.ConditionSeverityError,
						"Failed to update the replicated schema: %v.",
						err,
					)
					return reconcileStatusStopAndRequeue, err
				}
			}

			conditions.MarkTrue(cachedResource, cachev1alpha1.CachedResourceSourceSchemaReplicated)
			cachedResource.Status.ResourceSchemaSource.CRD.ResourceVersion = crd.ObjectMeta.ResourceVersion
			return reconcileStatusStopAndRequeue, nil
		}

		return reconcileStatusContinue, nil
	}

	// This should never happen!

	conditions.MarkFalse(
		cachedResource,
		cachev1alpha1.CachedResourceSchemaSourceValid,
		cachev1alpha1.SchemaSourceNotReadyReason,
		conditionsv1alpha1.ConditionSeverityError,
		"API not ready.",
	)
	cachedResource.Status.ResourceSchemaSource = nil

	return reconcileStatusStopAndRequeue, nil
}

func validateAPIResourceSchemaForGVR(sch *apisv1alpha1.APIResourceSchema, gvr schema.GroupVersionResource) bool {
	if sch.Spec.Group != gvr.Group || sch.Spec.Names.Plural != gvr.Resource {
		return false
	}
	for i := range sch.Spec.Versions {
		if sch.Spec.Versions[i].Name == gvr.Version {
			return true
		}
	}
	return false
}

func validateCRDForGVR(crd *apiextensionsv1.CustomResourceDefinition, gvr schema.GroupVersionResource) bool {
	if crd.Spec.Group != gvr.Group || crd.Spec.Names.Plural != gvr.Resource {
		return false
	}
	for _, version := range crd.Status.StoredVersions {
		if version == gvr.Version {
			return true
		}
	}
	return false
}
