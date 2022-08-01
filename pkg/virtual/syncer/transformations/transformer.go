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
	"encoding/json"
	"strings"

	jsonpatch "github.com/evanphx/json-patch"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	"github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/transforming"
	syncercontext "github.com/kcp-dev/kcp/pkg/virtual/syncer/context"
)

type SyncerTransformation interface {
	ReadFromKCP(SyncTargetName string, newKCPResource, existingSyncerViewResource *unstructured.Unstructured, requestedSyncing map[string]SyncTargetSyncing) (newSyncerViewResource *unstructured.Unstructured, err error)
}

var _ transforming.ResourceTransformer = (*SyncerResourceTransformer)(nil)

type SyncerResourceTransformer struct {
	Transformation SyncerTransformation
}

func (*SyncerResourceTransformer) BeforeWrite(client dynamic.ResourceInterface, ctx context.Context, newSyncerViewObject *unstructured.Unstructured, subresources ...string) (newKCPViewObject *unstructured.Unstructured, err error) {
	syncTargetName, err := syncercontext.SyncTargetNameFrom(ctx)
	if err != nil {
		return nil, err
	}

	syncerFinalizerName := shared.SyncerFinalizerNamePrefix + syncTargetName

	syncerViewHasSyncerFinalizer := sets.NewString(newSyncerViewObject.GetFinalizers()...).Has(syncerFinalizerName)
	removeFromSyncer := newSyncerViewObject.GetDeletionTimestamp() != nil && !syncerViewHasSyncerFinalizer

	if bytes, err := yaml.Marshal(newSyncerViewObject.UnstructuredContent()); err == nil {
		klog.V(5).Infof("Before SyncerViewTransformer - New Syncer View Resource:\n%s", string(bytes))
	}

	newSyncerViewObjectJson, err := json.Marshal(newSyncerViewObject.UnstructuredContent())
	if err != nil {
		return nil, err
	}

	oldKCPViewObject, err := client.Get(ctx, newSyncerViewObject.GetName(), metav1.GetOptions{ResourceVersion: newSyncerViewObject.GetResourceVersion()})
	if err != nil {
		return nil, err
	}

	oldSyncerViews, err := GetAllSyncerViews(oldKCPViewObject)
	if err != nil {
		return nil, err
	}

	if removeFromSyncer {
		newSyncerViewObject = nil
	}

	newKCPViewObject = oldKCPViewObject.DeepCopy()

	newKCPViewAnnotations := newKCPViewObject.GetAnnotations()

	newKCPViewObjectJson, err := newKCPViewObject.MarshalJSON()
	if err != nil {
		return nil, err
	}

	if removeFromSyncer {
		delete(oldSyncerViews, syncTargetName)
	} else {
		oldSyncerViews[syncTargetName] = *newSyncerViewObject
	}

	newSyncing := GetSyncing(newKCPViewObject)

	// Update syncer view diff for all sync targets based on the new KCP View object
	for syncTarget, syncerView := range oldSyncerViews {
		if syncTargetSyncing := newSyncing[syncTarget]; syncTargetSyncing.ResourceState != v1alpha1.ResourceStateSync {
			delete(newKCPViewAnnotations, v1alpha1.InternalSyncerViewAnnotationPrefix+syncTarget)
			continue
		}

		var syncerViewObjectJson []byte
		if syncTarget == syncTargetName {
			syncerViewObjectJson = newSyncerViewObjectJson
		} else {
			syncerViewObjectJson, err = syncerView.MarshalJSON()
			if err != nil {
				return nil, err
			}
		}
		syncerViewDiff, err := jsonpatch.CreateMergePatch(newKCPViewObjectJson, syncerViewObjectJson)
		if err != nil {
			return nil, err
		}
		if newKCPViewAnnotations == nil {
			newKCPViewAnnotations = make(map[string]string)
		}
		newKCPViewAnnotations[v1alpha1.InternalSyncerViewAnnotationPrefix+syncTarget] = string(syncerViewDiff)
	}
	if removeFromSyncer {
		delete(newKCPViewAnnotations, v1alpha1.InternalSyncerViewAnnotationPrefix+syncTargetName)
	}
	newKCPViewObject.SetAnnotations(newKCPViewAnnotations)

	kcpViewFinalizers := sets.NewString(newKCPViewObject.GetFinalizers()...)
	if syncerViewHasSyncerFinalizer && !kcpViewFinalizers.Has(syncerFinalizerName) {
		kcpViewFinalizers.Insert(syncerFinalizerName)
		newKCPViewObject.SetFinalizers(kcpViewFinalizers.List())
	} else if removeFromSyncer && kcpViewFinalizers.Has(syncerFinalizerName) {
		kcpViewFinalizers.Delete(syncerFinalizerName)
		newKCPViewObject.SetFinalizers(kcpViewFinalizers.List())
	}

	if removeFromSyncer && len(subresources) == 0 {
		if annotations := newKCPViewObject.GetAnnotations(); annotations != nil {
			delete(annotations, v1alpha1.SyncingTransformationAnnotationPrefix+syncTargetName)
			delete(annotations, v1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+syncTargetName)
			newKCPViewObject.SetAnnotations(annotations)
		}
		if labels := newKCPViewObject.GetLabels(); labels != nil {
			delete(labels, v1alpha1.ClusterResourceStateLabelPrefix+syncTargetName)
			newKCPViewObject.SetLabels(labels)
		}
	}

	if bytes, err := yaml.Marshal(newKCPViewObject.UnstructuredContent()); err == nil {
		klog.V(5).Infof("Before SyncerViewTransformer - New KCP View Resource:\n%s", string(bytes))
	}

	return newKCPViewObject, nil
}

func (rt *SyncerResourceTransformer) AfterRead(client dynamic.ResourceInterface, ctx context.Context, returnedKCPResource *unstructured.Unstructured, eventType *watch.EventType, subresources ...string) (*unstructured.Unstructured, error) {
	syncTargetName, err := syncercontext.SyncTargetNameFrom(ctx)
	if err != nil {
		return nil, err
	}

	syncerFinalizerName := shared.SyncerFinalizerNamePrefix + syncTargetName

	syncing := GetSyncing(returnedKCPResource)
	klog.Infof("After SyncerViewTransformer - KCP Object Syncing : %#v", syncing)

	if bytes, err := yaml.Marshal(returnedKCPResource.UnstructuredContent()); err == nil {
		klog.V(5).Infof("After SyncerViewTransformer - KCP Resource:\n%s", string(bytes))
	}

	existingSyncerViewResource, err := GetSyncerView(returnedKCPResource, syncTargetName)
	if err != nil {
		return nil, err
	}

	cleanedKCPResource := returnedKCPResource.DeepCopy()
	cleanedKCPResource.SetFinalizers(nil)
	cleanedKCPResource.SetDeletionTimestamp(nil)

	// Remove the syncer view diff annotation from the syncer view resource
	annotations := cleanedKCPResource.GetAnnotations()
	for name := range annotations {
		if strings.HasPrefix(name, v1alpha1.InternalSyncerViewAnnotationPrefix) {
			delete(annotations, name)
		}
	}
	cleanedKCPResource.SetAnnotations(annotations)

	transformedSyncerViewResource := cleanedKCPResource
	if rt.Transformation != nil {
		transformedSyncerViewResource, err = rt.Transformation.ReadFromKCP(syncTargetName, cleanedKCPResource, existingSyncerViewResource, syncing)
		if err != nil {
			return nil, err
		}
	}

	// Carry on existing syncer view status to the new syncer view:
	// Status NEVER comes from upstream.
	if existingSyncerViewResource != nil {
		// Set the status to the syncer view (last set status for this sync target)
		if status, exists, err := unstructured.NestedFieldCopy(existingSyncerViewResource.UnstructuredContent(), "status"); err != nil {
			return nil, err
		} else if exists {
			if err := unstructured.SetNestedField(transformedSyncerViewResource.UnstructuredContent(), status, "status"); err != nil {
				return nil, err
			}
		} else {
			unstructured.RemoveNestedField(transformedSyncerViewResource.UnstructuredContent(), "status")
		}
	}

	kcpResourceFinalizers := sets.NewString(returnedKCPResource.GetFinalizers()...)
	syncerViewFinalizers := sets.NewString(transformedSyncerViewResource.GetFinalizers()...)
	if kcpResourceFinalizers.Has(syncerFinalizerName) {
		syncerViewFinalizers.Insert(syncerFinalizerName)
	}

	transformedSyncerViewResource.SetFinalizers(syncerViewFinalizers.List())

	if deletionTimestamp := syncing[syncTargetName].DeletionTimestamp; deletionTimestamp != nil && syncing[syncTargetName].Finalizers == "" {
		transformedSyncerViewResource.SetDeletionTimestamp(deletionTimestamp)
	}

	if bytes, err := yaml.Marshal(transformedSyncerViewResource.UnstructuredContent()); err == nil {
		klog.V(5).Infof("After SyncerViewTransformer - Transformed Resource:\n%s", string(bytes))
	}
	return transformedSyncerViewResource, nil
}
