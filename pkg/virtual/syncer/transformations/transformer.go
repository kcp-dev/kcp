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

package transformations

import (
	"context"
	"errors"
	"fmt"
	"strings"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/apis/workload/helpers"
	"github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	. "github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/transforming"
	syncercontext "github.com/kcp-dev/kcp/pkg/virtual/syncer/context"
	. "github.com/kcp-dev/kcp/tmc/pkg/logging"
)

const (
	promotedToUpstream = "##promoted##"

	errorMessage    = "error during transformation"
	startingMessage = "starting transformation"
)

var _ transforming.ResourceTransformer = (*SyncerResourceTransformer)(nil)

// SyncerResourceTransformer manages both the transformation of resources exposed to a Syncer
// when syncing to downstream, and the management of fields updated by the Syncer
// when syncing back to upstream.
type SyncerResourceTransformer struct {
	TransformationProvider
	SummarizingRulesProvider
}

// TransformationFor implements [TransformationProvider.TransformationFor].
func (srt SyncerResourceTransformer) TransformationFor(resource metav1.Object) (Transformation, error) {
	if srt.TransformationProvider == nil {
		return nil, nil
	}
	return srt.TransformationProvider.TransformationFor(resource)
}

// SummarizingRulesFor implements [SummarizingRulesProvider.SummarizingRulesFor].
func (srt SyncerResourceTransformer) SummarizingRulesFor(resource metav1.Object) (SummarizingRules, error) {
	if srt.SummarizingRulesProvider == nil {
		return &DefaultSummarizingRules{}, nil
	}
	return srt.SummarizingRulesProvider.SummarizingRulesFor(resource)
}

// BeforeWrite implements [transforming.ResourceTransformer.BeforeWrite].
// It will be called when the Syncer updates a given resource through the
// Syncer virtual workspace.
// It performs the appropriate cleanup of the resource if the Syncer doesn't own the resource anymore
// (syncer finalizer was removed).
// In all other cases, it applies every summarized fields updated by the Syncer
// to the Syncer View annotation, possibly promoting it to te upstream resource.
func (rt *SyncerResourceTransformer) BeforeWrite(client dynamic.ResourceInterface, ctx context.Context, gvr schema.GroupVersionResource, syncerViewResource *unstructured.Unstructured, subresources ...string) (*unstructured.Unstructured, error) {
	apiDomainKey := dynamiccontext.APIDomainKeyFrom(ctx)
	_, _, syncTargetName, err := kcpcache.SplitMetaClusterNamespaceKey(string(apiDomainKey))
	if err != nil {
		return nil, err
	}
	logger := klog.FromContext(ctx).WithName("syncer-transformer").V(5).
		WithValues("step", "before", "gvr", gvr.String(), "subresources", subresources, "apiDomainKey", apiDomainKey, SyncTargetName, syncTargetName)
	logger = logger.WithValues(FromPrefix("syncerView", syncerViewResource)...)

	syncTargetKey, err := syncercontext.SyncTargetKeyFrom(ctx)
	if err != nil {
		logger.Error(err, errorMessage)
		return nil, err
	}
	logger = logger.WithValues(SyncTargetKey, syncTargetKey)

	syncerFinalizerName := shared.SyncerFinalizerNamePrefix + syncTargetKey

	syncerViewHasSyncerFinalizer := sets.NewString(syncerViewResource.GetFinalizers()...).Has(syncerFinalizerName)
	syncerViewDeletionTimestamp := syncerViewResource.GetDeletionTimestamp()
	syncerViewResourceVersion := syncerViewResource.GetResourceVersion()

	logger = logger.WithValues(
		"syncerView.HasSyncerFinalizer", syncerViewHasSyncerFinalizer,
		"syncerView.ResourceVersion", syncerViewResourceVersion,
	)
	if syncerViewDeletionTimestamp != nil {
		logger = logger.WithValues(
			"syncerView.deletionTimestamp", syncerViewDeletionTimestamp,
		)
	} else {
		logger = logger.WithValues(
			"syncerView.deletionTimestamp", "",
		)
	}

	logger.Info(startingMessage)

	removedFromSyncer := syncerViewDeletionTimestamp != nil && !syncerViewHasSyncerFinalizer && len(subresources) == 0

	existingUpstreamResource, err := client.Get(ctx, syncerViewResource.GetName(), metav1.GetOptions{ResourceVersion: syncerViewResourceVersion})
	if err != nil {
		return nil, err
	}

	logger = logger.WithValues(FromPrefix("upstreamResource", syncerViewResource)...)

	if existingUpstreamResource.GetResourceVersion() != syncerViewResource.GetResourceVersion() {
		logger.Info("upstream resource has the wrong resource version: return conflict error to the syncer", "upstreamViewResourceVersion", existingUpstreamResource.GetResourceVersion())
		return nil, kerrors.NewConflict(gvr.GroupResource(), existingUpstreamResource.GetName(), fmt.Errorf("the resource has been modified in the meantime"))
	}

	var fieldsToSummarize []FieldToSummarize
	if summarizingRules, err := rt.SummarizingRulesFor(existingUpstreamResource); err != nil {
		logger.Error(err, errorMessage)
		return nil, kerrors.NewInternalError(fmt.Errorf("unable to get summarizing rules from object upstream resource %s|%s/%s for SyncTarget %s: %w", logicalcluster.From(existingUpstreamResource), existingUpstreamResource.GetNamespace(), existingUpstreamResource.GetName(), syncTargetKey, err))
	} else if summarizingRules != nil {
		fieldsToSummarize = summarizingRules.FieldsToSummarize(gvr)
	}

	if removedFromSyncer {
		logger.Info("resource has been removed from the syncer")
		existingSyncing, err := helpers.GetSyncIntents(existingUpstreamResource)
		if err != nil {
			logger.Error(err, errorMessage)
			return nil, kerrors.NewInternalError(err)
		}
		delete(existingSyncing, syncTargetKey)
		if len(existingSyncing) == 1 {
			var singleSyncTarget string
			for key := range existingSyncing {
				singleSyncTarget = key
			}

			if existingSyncing[singleSyncTarget].ResourceState == "Sync" &&
				sets.NewString(existingUpstreamResource.GetFinalizers()...).Has(shared.SyncerFinalizerNamePrefix+singleSyncTarget) {
				// If removing the current SyncTarget leaves only one SyncTarget,
				// and the remaining syncTarget has the Sync label,
				// then let's promote syncer view overriding field values of this remaining SyncTarget
				// for both the Status and Spec (as needed)

				logger.Info("after removing the resource from the syncer, it will be scheduled on only 1 syncTarget => manage promotion")
				statusPromoted := false
				promoted := false

				existingSyncerViewFields, err := getSyncerViewFields(existingUpstreamResource, singleSyncTarget)
				if err != nil {
					logger.Error(err, errorMessage)
					return nil, kerrors.NewInternalError(fmt.Errorf("unable to get syncer view fields from upstream resource %s|%s/%s for SyncTarget %s: %w", logicalcluster.From(existingUpstreamResource), existingUpstreamResource.GetNamespace(), existingUpstreamResource.GetName(), singleSyncTarget, err))
				}
				if existingSyncerViewFields != nil {
					for _, field := range fieldsToSummarize {
						logger := logger.WithValues("field", field.Path())
						if !field.CanPromoteToUpstream() {
							continue
						}
						logger.Info("promoting field to the upstream resource")
						if syncerViewFieldValue := existingSyncerViewFields[field.Path()]; syncerViewFieldValue != promotedToUpstream {
							err := field.Set(existingUpstreamResource, syncerViewFieldValue)
							if err != nil {
								logger.Error(err, errorMessage)
								return nil, kerrors.NewInternalError(fmt.Errorf("unable to set promoted syncer view field %s on upstream resource %s|%s/%s for SyncTarget %s: %w", field.Path(), logicalcluster.From(existingUpstreamResource), existingUpstreamResource.GetNamespace(), existingUpstreamResource.GetName(), singleSyncTarget, err))
							}
							existingSyncerViewFields[field.Path()] = promotedToUpstream
							promoted = true
							if field.IsStatus() {
								statusPromoted = true
							}
						}
					}
					if promoted {
						if err := setSyncerViewFields(existingUpstreamResource, singleSyncTarget, existingSyncerViewFields); err != nil {
							logger.Error(err, errorMessage)
							return nil, kerrors.NewInternalError(fmt.Errorf("unable to set syncer view fields for upstream resource %s|%s/%s for SyncTarget %s: %w", logicalcluster.From(existingUpstreamResource), existingUpstreamResource.GetNamespace(), existingUpstreamResource.GetName(), singleSyncTarget, err))
						}
					}
				}

				if statusPromoted {
					logger.Info("updating the status of the upstream resource after field promotion, before the normal update")
					existingUpstreamResource, err = client.UpdateStatus(ctx, existingUpstreamResource, metav1.UpdateOptions{})
					if err != nil {
						logger.Error(err, errorMessage)
						return nil, err
					}
				}
			}
		}

		logger.Info("removing the syncTarget-related labels, annotation and finalizers")
		if labels := existingUpstreamResource.GetLabels(); labels != nil {
			delete(labels, v1alpha1.ClusterResourceStateLabelPrefix+syncTargetKey)
			existingUpstreamResource.SetLabels(labels)
		}

		finalizers := sets.NewString(existingUpstreamResource.GetFinalizers()...)
		if finalizers.Has(syncerFinalizerName) {
			finalizers.Delete(syncerFinalizerName)
			existingUpstreamResource.SetFinalizers(finalizers.List())
		}

		if annotations := existingUpstreamResource.GetAnnotations(); annotations != nil {
			delete(annotations, v1alpha1.InternalSyncerViewAnnotationPrefix+syncTargetKey)
			delete(annotations, v1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+syncTargetKey)
			delete(annotations, v1alpha1.ClusterSpecDiffAnnotationPrefix)
			existingUpstreamResource.SetAnnotations(annotations)
		}

		return existingUpstreamResource, nil
	}

	logger.Info("checking the requested syncing")
	newSyncing, err := helpers.GetSyncIntents(existingUpstreamResource)
	if err != nil {
		logger.Error(err, errorMessage)
		return nil, kerrors.NewInternalError(err)
	}
	if syncTargetSyncing, exists := newSyncing[syncTargetKey]; !exists || syncTargetSyncing.ResourceState != "Sync" {
		logger.Error(errors.New("the Syncer tried to write resource though it is not assigned to it"), errorMessage)
		return nil, kerrors.NewInternalError(fmt.Errorf("tried to write resource %s(%s/%s) though it is not assigned to the current SyncTarget",
			strings.Join(append([]string{gvr.Resource}, subresources...), "/"),
			existingUpstreamResource.GetNamespace(),
			existingUpstreamResource.GetName()))
	}

	if !syncerViewHasSyncerFinalizer {
		logger.Error(errors.New("the Syncer tried to write resource though it is not owning it (syncer finalizer doesn't exist"), errorMessage)
		return nil, kerrors.NewInternalError(fmt.Errorf("tried to write resource %s(%s/%s) though it is not owning it (syncer finalizer doesn't exist)",
			strings.Join(append([]string{gvr.Resource}, subresources...), "/"),
			existingUpstreamResource.GetNamespace(),
			existingUpstreamResource.GetName()))
	}

	upstreamObjectSyncerFinalizers := sets.NewString()
	for _, finalizer := range existingUpstreamResource.GetFinalizers() {
		if strings.HasPrefix(finalizer, shared.SyncerFinalizerNamePrefix) {
			upstreamObjectSyncerFinalizers.Insert(finalizer)
		}
	}

	if len(newSyncing) > 1 && upstreamObjectSyncerFinalizers.Len() == 1 {
		// There's more than one SyncTarget State label
		// If there is only one SyncTarget that currently owns the resource (syncer finalizer is present on the upstream resource),
		// then we should unpromote the syncer view field that would have been promoted to the upstream resource.

		logger.Info("resource scheduled on several syncTargets, but only one SyncTarget currently owns the resource => manage field unpromoting before the resource is owned by the second syncer")
		singleOwningSyncTarget := strings.TrimPrefix(upstreamObjectSyncerFinalizers.UnsortedList()[0], shared.SyncerFinalizerNamePrefix)

		existingSyncerViewFields, err := getSyncerViewFields(existingUpstreamResource, singleOwningSyncTarget)
		if err != nil {
			logger.Error(err, errorMessage)
			return nil, kerrors.NewInternalError(fmt.Errorf("unable to get syncer view fields from upstream resource %s|%s/%s for SyncTarget %s: %w", logicalcluster.From(existingUpstreamResource), existingUpstreamResource.GetNamespace(), existingUpstreamResource.GetName(), singleOwningSyncTarget, err))
		}
		unpromoted := false
		for _, field := range fieldsToSummarize {
			logger := logger.WithValues("field", field.Path())

			if !field.CanPromoteToUpstream() {
				continue
			}
			if existingSyncerViewFields[field.Path()] == promotedToUpstream {
				logger.Info("unpromoting field from the upstream resource")
				promotedUpstreamFieldValue, exists, err := field.Get(existingUpstreamResource)
				if err != nil {
					logger.Error(err, errorMessage)
					return nil, kerrors.NewInternalError(fmt.Errorf("unable to get promoted syncer view field %s from upstream resource %s|%s/%s for SyncTarget %s: %w", field.Path(), logicalcluster.From(existingUpstreamResource), existingUpstreamResource.GetNamespace(), existingUpstreamResource.GetName(), singleOwningSyncTarget, err))
				}
				if !exists {
					logger.Info("WARNING: this should not happen: the presence of a promoted syncer view field in upstream should match the presence of a promotion marker in the annotation on upstream resource")
					continue
				}

				existingSyncerViewFields[field.Path()] = promotedUpstreamFieldValue
				unpromoted = true
			}
		}
		if unpromoted {
			if err := setSyncerViewFields(existingUpstreamResource, singleOwningSyncTarget, existingSyncerViewFields); err != nil {
				logger.Error(err, errorMessage)
				return nil, kerrors.NewInternalError(fmt.Errorf("unable to set syncer view fields on upstream resource %s|%s/%s for SyncTarget %s: %w", logicalcluster.From(existingUpstreamResource), existingUpstreamResource.GetNamespace(), existingUpstreamResource.GetName(), singleOwningSyncTarget, err))
			}
		}
	}

	logger.Info("update the syncer view fields annotation in the upstream resource based on the syncer view")
	existingSyncerViewFields, err := getSyncerViewFields(existingUpstreamResource, syncTargetKey)
	if err != nil {
		logger.Error(err, errorMessage)
		return nil, kerrors.NewInternalError(fmt.Errorf("unable to get syncer view fields from upstream resource %s|%s/%s for SyncTarget %s: %w", logicalcluster.From(existingUpstreamResource), existingUpstreamResource.GetNamespace(), existingUpstreamResource.GetName(), syncTargetKey, err))
	}

	syncerViewFields := make(map[string]interface{})

	statusSubresource := len(subresources) == 1 && subresources[0] == "status"
	for _, field := range fieldsToSummarize {
		logger := logger.WithValues("field", field.Path())

		if field.IsStatus() && !statusSubresource || !field.IsStatus() && statusSubresource {
			if existingValue, exists := existingSyncerViewFields[field.Path()]; exists {
				logger.Info("keeping the previous syncer view field value")
				syncerViewFields[field.Path()] = existingValue
			}
			continue
		}

		logger.Info("getting field on syncerView")
		syncerViewFieldValue, exists, err := field.Get(syncerViewResource)
		if err != nil {
			logger.Error(err, errorMessage)
			return nil, kerrors.NewInternalError(fmt.Errorf("unable to get syncer view field %s from downstream resource %s/%s coming from SyncTarget %s: %w", field.Path(), syncerViewResource.GetNamespace(), syncerViewResource.GetName(), syncTargetKey, err))
		}
		if !exists {
			logger.Info("field does not exist on syncerView")
			continue
		}

		if field.CanPromoteToUpstream() {
			if len(newSyncing) == 1 {
				logger.Info("resource is scheduled on a single syncTarget => promote the field")
				// There is only one SyncTarget, so let's promote the field value to the upstream resource
				if err := field.Set(existingUpstreamResource, syncerViewFieldValue); err != nil {
					logger.Error(err, errorMessage)
					return nil, kerrors.NewInternalError(fmt.Errorf("unable to promote syncer view field %s from downstream resource %s/%s coming from SyncTarget %s: %w", field.Path(), syncerViewResource.GetNamespace(), syncerViewResource.GetName(), syncTargetKey, err))
				}
				// Only add a promotion marker for the field in the syncer view fields annotation
				syncerViewFieldValue = promotedToUpstream
			}
		}

		logger.Info("setting the syncer view field value", "value", syncerViewFieldValue)
		// Now simply add the field in the syncer view fields annotation
		syncerViewFields[field.Path()] = syncerViewFieldValue
	}

	logger.Info("setting the updated syncer view fields annotation on the upstream resource")
	if err := setSyncerViewFields(existingUpstreamResource, syncTargetKey, syncerViewFields); err != nil {
		logger.Error(err, errorMessage)
		return nil, kerrors.NewInternalError(fmt.Errorf("unable to set syncer view fields on upstream resource %s|%s/%s for SyncTarget %s: %w", logicalcluster.From(existingUpstreamResource), existingUpstreamResource.GetNamespace(), existingUpstreamResource.GetName(), syncTargetKey, err))
	}

	if syncerViewHasSyncerFinalizer && !upstreamObjectSyncerFinalizers.Has(syncerFinalizerName) {
		logger.Info("adding the syncer finalizer to the upstream resource")
		existingUpstreamResource.SetFinalizers(sets.NewString(append(existingUpstreamResource.GetFinalizers(), syncerFinalizerName)...).List())
	}

	logger.Info("resource transformed")
	return existingUpstreamResource, nil
}

// AfterRead implements [transforming.ResourceTransformer.AfterRead].
// It will be called when an upstream resource is read from the Syncer Virtual Workspace
// for a given SyncTarget (typically by the Syncer).
// It transforms the upstream resource according to the provided Transformation,
// and applies on top of the transformed resource every summarized fields previously updated
// by the Syncer.
func (rt *SyncerResourceTransformer) AfterRead(_ dynamic.ResourceInterface, ctx context.Context, gvr schema.GroupVersionResource, upstreamResource *unstructured.Unstructured, eventType *watch.EventType, subresources ...string) (*unstructured.Unstructured, error) {
	apiDomainKey := dynamiccontext.APIDomainKeyFrom(ctx)
	logger := klog.FromContext(ctx).WithName("syncer-transformer").V(5).
		WithValues("step", "after", "groupVersionResource", gvr.String(), "subresources", subresources, "apiDomainKey", apiDomainKey)
	if eventType != nil {
		logger = logger.WithValues("eventType", *eventType)
	} else {
		logger = logger.WithValues("eventType", "")
	}
	logger = logger.WithValues(FromPrefix("upstreamResource", upstreamResource)...)

	syncTargetKey, err := syncercontext.SyncTargetKeyFrom(ctx)
	if err != nil {
		logger.Error(err, errorMessage)
		return nil, err
	}
	logger = logger.WithValues(SyncTargetKey, syncTargetKey)

	logger.Info(startingMessage)

	syncerFinalizerName := shared.SyncerFinalizerNamePrefix + syncTargetKey

	syncing, err := helpers.GetSyncIntents(upstreamResource)
	if err != nil {
		logger.Error(err, errorMessage)
		return nil, kerrors.NewInternalError(err)
	}

	syncerViewFields, err := getSyncerViewFields(upstreamResource, syncTargetKey)
	if err != nil {
		logger.Error(err, errorMessage)
		return nil, kerrors.NewInternalError(fmt.Errorf("unable to get syncer view fields from upstream resource %s|%s/%s for SyncTarget %s: %w", logicalcluster.From(upstreamResource), upstreamResource.GetNamespace(), upstreamResource.GetName(), syncTargetKey, err))
	}

	cleanedUpstreamResource := upstreamResource.DeepCopy()
	cleanedUpstreamResource.SetFinalizers(nil)
	cleanedUpstreamResource.SetDeletionTimestamp(nil)

	logger.Info("cleaning the upstream resource before calling the transformation")

	// Remove the syncer view diff annotation from the syncer view resource
	annotations := cleanedUpstreamResource.GetAnnotations()
	for name := range annotations {
		if strings.HasPrefix(name, v1alpha1.InternalSyncerViewAnnotationPrefix) {
			delete(annotations, name)
		}
	}
	cleanedUpstreamResource.SetAnnotations(annotations)

	transformedSyncerViewResource := cleanedUpstreamResource
	if transformation, err := rt.TransformationFor(upstreamResource); err != nil {
		logger.Error(err, errorMessage)
		return nil, kerrors.NewInternalError(fmt.Errorf("unable to get transformation from object upstream resource %s|%s/%s for SyncTarget %s: %w", logicalcluster.From(upstreamResource), upstreamResource.GetNamespace(), upstreamResource.GetName(), syncTargetKey, err))
	} else if transformation != nil {
		logger.Info("calling the syncer transformation")
		transformedSyncerViewResource, err = transformation.ToSyncerView(syncTargetKey, gvr, cleanedUpstreamResource, syncerViewFields, syncing)
		if err != nil {
			return nil, err
		}
	}

	if summarizingRules, err := rt.SummarizingRulesFor(upstreamResource); err != nil {
		logger.Error(err, errorMessage)
		return nil, kerrors.NewInternalError(fmt.Errorf("unable to get summarizing rules from object upstream resource %s|%s/%s for SyncTarget %s: %w", logicalcluster.From(upstreamResource), upstreamResource.GetNamespace(), upstreamResource.GetName(), syncTargetKey, err))
	} else if summarizingRules != nil {
		logger.Info("applying summarizing rules")

		for _, field := range summarizingRules.FieldsToSummarize(gvr) {
			logger := logger.WithValues("field", field.Path())
			existingSyncerViewValue, syncerViewValueExists := syncerViewFields[field.Path()]

			if field.CanPromoteToUpstream() {
				if !syncerViewValueExists {
					// Fields that can be promoted to the upstream resource (like status) are ALWAYS owned by the syncer.
					// So if the value of the field is not explicitly overridden in the syncer view annotation,
					// let's remove the field from the syncer view.
					logger.Info("field can be promoted, but is not found in the syncer view fields annotation => delete the field from the transformed syncer view resource")
					field.Delete(transformedSyncerViewResource)
					continue
				}
				if existingSyncerViewValue == promotedToUpstream {
					logger.Info("syncer view field is promoted  => get the field value from the upstream resource")
					promotedValueExists := false
					existingSyncerViewValue, promotedValueExists, err = field.Get(upstreamResource)
					if err != nil {
						logger.Error(err, errorMessage)
						return nil, kerrors.NewInternalError(fmt.Errorf("unable to get promoted syncer view field %s on resource %s|%s/%s: %w", field.Path(), logicalcluster.From(upstreamResource), upstreamResource.GetNamespace(), upstreamResource.GetName(), err))
					}
					if !promotedValueExists {
						logger.Error(errors.New("promoted syncer view field does not exist: this should never happen"), errorMessage)
						logger.Info("dropping invalid field")
						field.Delete(transformedSyncerViewResource)
						continue
					}
				}
				logger.Info("overriding field with syncer view field value", "value", existingSyncerViewValue)
				if err := field.Set(transformedSyncerViewResource, existingSyncerViewValue); err != nil {
					logger.Error(err, errorMessage)
					return nil, kerrors.NewInternalError(fmt.Errorf("unable to override field %s on resource %s|%s/%s: %w", field.Path(), logicalcluster.From(upstreamResource), upstreamResource.GetNamespace(), upstreamResource.GetName(), err))
				}
				continue
			}

			// Field cannot be promoted to upstream and could possibly be owned by both the syncer and KCP
			// In this case let's keep the upstream value of the field if no overriding value was provided by the syncer
			if syncerViewValueExists {
				// TODO(davidfestal): in the future, when we also summarize some Spec fields (like Ingress class),
				// we might want to check the existence of the field in the transformedSyncerView, and decide which of both
				// the transformed value and the previously-overriding value should be kept.
				logger.Info("overriding field with syncer view field value", "value", existingSyncerViewValue)
				if err := field.Set(transformedSyncerViewResource, existingSyncerViewValue); err != nil {
					logger.Error(err, errorMessage)
					return nil, kerrors.NewInternalError(fmt.Errorf("unable to override field %s on resource %s|%s/%s: %w", field.Path(), logicalcluster.From(transformedSyncerViewResource), transformedSyncerViewResource.GetNamespace(), transformedSyncerViewResource.GetName(), err))
				}
			}
		}
	}

	upstreamResourceFinalizers := sets.NewString(upstreamResource.GetFinalizers()...)
	syncerViewFinalizers := sets.NewString(transformedSyncerViewResource.GetFinalizers()...)
	if upstreamResourceFinalizers.Has(syncerFinalizerName) {
		logger.Info("propagating the syncer finalizer from the upstream resource to the transformed syncer view")
		syncerViewFinalizers.Insert(syncerFinalizerName)
		transformedSyncerViewResource.SetFinalizers(syncerViewFinalizers.List())
	}

	if deletionTimestamp := syncing[syncTargetKey].DeletionTimestamp; deletionTimestamp != nil && syncing[syncTargetKey].Finalizers == "" {
		// Only propagate the deletionTimestamp if no soft finalizers exist for this SyncTarget.
		logger.Info("propagating the deletionTimestanmp annotation as the real deletionTimestamp of the transformed syncer view")
		transformedSyncerViewResource.SetDeletionTimestamp(deletionTimestamp)
	}

	logger.Info("resource transformed")
	return transformedSyncerViewResource, nil
}
