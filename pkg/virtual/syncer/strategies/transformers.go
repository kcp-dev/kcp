/*
Copyright 2021 The KCP Authors.

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

package strategies

import (
	"context"
	"encoding/json"
	"strings"

	jsonpatch "github.com/evanphx/json-patch"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

const (
	syncerViewAnnotationPrefix string = "diff.syncer.internal.kcp.dev/"
)

func transformBeforeWrite(gvr schema.GroupVersionResource, syncStrategy SyncStrategy) transforming.TransformResourceBeforeFunc {
	return func(client dynamic.ResourceInterface, ctx context.Context, newSyncerViewObject *unstructured.Unstructured, subresources ...string) (newKCPViewObject *unstructured.Unstructured, err error) {
		workloadClusterName, err := syncercontext.WorkloadClusterNameFrom(ctx)
		if err != nil {
			return nil, err
		}

		syncerFinalizerName := shared.SyncerFinalizerNamePrefix + workloadClusterName

		hasSyncerFinalizer := sets.NewString(newSyncerViewObject.GetFinalizers()...).Has(syncerFinalizerName)
		removeFromSyncer := newSyncerViewObject.GetDeletionTimestamp() != nil && !hasSyncerFinalizer

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

		oldSyncerViews, err := GetSyncerViews(oldKCPViewObject)
		if err != nil {
			return nil, err
		}

		if removeFromSyncer {
			newSyncerViewObject = nil
		}

		syncing := GetSyncing(oldKCPViewObject)

		newKCPViewObject, err = syncStrategy.UpdateFromSyncTarget(workloadClusterName, newSyncerViewObject, oldKCPViewObject, oldSyncerViews, syncing)
		if err != nil {
			return nil, err
		}

		newKCPViewAnnotations := newKCPViewObject.GetAnnotations()
		if newKCPViewAnnotations == nil {
			newKCPViewAnnotations = make(map[string]string)
		}

		newKCPViewObjectJson, err := newKCPViewObject.MarshalJSON()
		if err != nil {
			return nil, err
		}

		if removeFromSyncer {
			delete(oldSyncerViews, workloadClusterName)
		} else {
			oldSyncerViews[workloadClusterName] = *newSyncerViewObject
		}

		newSyncing := GetSyncing(newKCPViewObject)

		// Update syncer view diff for all sync targets based on the new KCP View object
		for syncTarget, syncerView := range oldSyncerViews {
			if syncTargetSyncing := newSyncing[syncTarget]; !syncTargetSyncing.Active() {
				delete(newKCPViewAnnotations, syncerViewAnnotationPrefix+syncTarget)
				continue
			}

			var syncerViewObjectJson []byte
			if syncTarget == workloadClusterName {
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
			newKCPViewAnnotations[syncerViewAnnotationPrefix+syncTarget] = string(syncerViewDiff)
		}
		if removeFromSyncer {
			delete(newKCPViewAnnotations, syncerViewAnnotationPrefix+workloadClusterName)
		}
		newKCPViewObject.SetAnnotations(newKCPViewAnnotations)

		if hasSyncerFinalizer {
			finalizers := sets.NewString(newKCPViewObject.GetFinalizers()...)
			if !finalizers.Has(syncerFinalizerName) {
				finalizers.Insert(syncerFinalizerName)
				newKCPViewObject.SetFinalizers(finalizers.List())
			}
		}

		if bytes, err := yaml.Marshal(newKCPViewObject.UnstructuredContent()); err == nil {
			klog.V(5).Infof("Before SyncerViewTransformer - New KCP View Resource:\n%s", string(bytes))
		}

		return newKCPViewObject, nil
	}
}

func transformAfterRead(gvr schema.GroupVersionResource, syncStrategy SyncStrategy) transforming.TransformResourceAfterFunc {
	return func(client dynamic.ResourceInterface, ctx context.Context, returnedKCPResource *unstructured.Unstructured, eventType *watch.EventType, subresources ...string) (transformedSyncerViewResource *unstructured.Unstructured, err error) {
		workloadClusterName, err := syncercontext.WorkloadClusterNameFrom(ctx)
		if err != nil {
			return nil, err
		}

		syncerFinalizerName := shared.SyncerFinalizerNamePrefix + workloadClusterName

		syncing := GetSyncing(returnedKCPResource)
		klog.Infof("After SyncerViewTransformer - KCP Object Syncing : %#v", syncing)

		if bytes, err := yaml.Marshal(returnedKCPResource.UnstructuredContent()); err == nil {
			klog.V(5).Infof("After SyncerViewTransformer - KCP Resource:\n%s", string(bytes))
		}

		var oldSyncerViewObject *unstructured.Unstructured
		if filteredOldSyncerViews, err := GetSyncerViews(returnedKCPResource, workloadClusterName); err != nil {
			return nil, err
		} else {
			if syncerView, exists := filteredOldSyncerViews[workloadClusterName]; exists {
				oldSyncerViewObject = &syncerView
			}
		}

		cleanedKCPResource := returnedKCPResource.DeepCopy()
		cleanedKCPResource.SetFinalizers(nil)
		cleanedKCPResource.SetDeletionTimestamp(nil)

		// Remove the syncer view diff annotation from the syncer view resource
		annotations := cleanedKCPResource.GetAnnotations()
		for name := range annotations {
			if strings.HasPrefix(name, syncerViewAnnotationPrefix) {
				delete(annotations, name)
			}
		}
		cleanedKCPResource.SetAnnotations(annotations)

		transformedSyncerViewResource, err = syncStrategy.ReadFromKCP(workloadClusterName, cleanedKCPResource, oldSyncerViewObject, syncing)
		if err != nil {
			return nil, err
		}

		kcpResourceFinalizers := sets.NewString(returnedKCPResource.GetFinalizers()...)
		syncerViewFinalizers := sets.NewString(transformedSyncerViewResource.GetFinalizers()...)
		if kcpResourceFinalizers.Has(syncerFinalizerName) {
			syncerViewFinalizers.Insert(syncerFinalizerName)
		}

		transformedSyncerViewResource.SetFinalizers(syncerViewFinalizers.List())

		if deletionTimestamp := syncing[workloadClusterName].RequiredDeletion(); deletionTimestamp != nil {
			transformedSyncerViewResource.SetDeletionTimestamp(deletionTimestamp)
		}

		if bytes, err := yaml.Marshal(transformedSyncerViewResource.UnstructuredContent()); err == nil {
			klog.V(5).Infof("After SyncerViewTransformer - Transformed Resource:\n%s", string(bytes))
		}
		return transformedSyncerViewResource, nil
	}
}

func cleanupKCPResourceWhenRemvovedFromSyncTarget(gvr schema.GroupVersionResource) func(client dynamic.ResourceInterface, ctx context.Context, obj *unstructured.Unstructured, options metav1.UpdateOptions, subresources []string, result *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	return func(client dynamic.ResourceInterface, ctx context.Context, updatedKCPObject *unstructured.Unstructured, options metav1.UpdateOptions, subresources []string, result *unstructured.Unstructured) (*unstructured.Unstructured, error) {
		workloadClusterName, err := syncercontext.WorkloadClusterNameFrom(ctx)
		if err != nil {
			return nil, err
		}

		syncerViews, err := GetSyncerViews(updatedKCPObject)
		if err != nil {
			return nil, err
		}
		if _, exists := syncerViews[workloadClusterName]; !exists {
			klog.Infof("Updating syncer annotations and labels for resource %q (%q) after removal from workload cluster %q", updatedKCPObject.GetName(), gvr.String(), workloadClusterName)
			if updatedKCPViewAnnotations := updatedKCPObject.GetAnnotations(); updatedKCPViewAnnotations != nil {
				delete(updatedKCPViewAnnotations, SyncingTransformationAnnotationPrefix+workloadClusterName)
				delete(updatedKCPViewAnnotations, v1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+workloadClusterName)
				updatedKCPObject.SetAnnotations(updatedKCPViewAnnotations)
			}
			if labels := updatedKCPObject.GetLabels(); labels != nil {
				delete(labels, v1alpha1.InternalClusterResourceStateLabelPrefix+workloadClusterName)
				updatedKCPObject.SetLabels(labels)
			}
			if updated, err := client.Update(ctx, updatedKCPObject, metav1.UpdateOptions{}); err != nil {
				return nil, err
			} else {
				return updated, nil
			}
		}
		return updatedKCPObject, nil
	}
}

func StrategyTransformers(gvr schema.GroupVersionResource, syncStrategy SyncStrategy) []transforming.Transformer {
	return []transforming.Transformer{
		// cleanup sync target-related labels and annotations after UpdateStatus when the syncer view has been removed on the syncer side
		{
			Name:        "CleanupSyncTarget",
			AfterUpdate: cleanupKCPResourceWhenRemvovedFromSyncTarget(gvr),
		},
		transforming.TransformsResource(
			"SyncerViewTransformer",

			// Update from a syncer view resource (mainly status or finalizers) to the virtual workspace resource
			transformBeforeWrite(gvr, syncStrategy),
			// Read the virtual workspace resource and transform it to return a syncer view resource
			transformAfterRead(gvr, syncStrategy),
		),
	}
}
