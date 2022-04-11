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
	"github.com/kcp-dev/kcp/pkg/virtual/framework/transforming"
	syncercontext "github.com/kcp-dev/kcp/pkg/virtual/syncer/context"
)

const (
	syncerViewAnnotationPrefix string = "diff.syncer.internal.kcp.dev/"
)

func syncerViewTransformer(gvr schema.GroupVersionResource, syncStrategy SyncStrategy) transforming.Transformer {
	return transforming.TransformsResource(
		"SyncerViewTransformer",

		// Update from a syncer view object (status) to the virtual workspace resource
		func(client dynamic.ResourceInterface, ctx context.Context, newSyncerViewObject *unstructured.Unstructured, subresources ...string) (newKCPViewObject *unstructured.Unstructured, err error) {
			workloadClusterName, err := syncercontext.WorkloadClusterNameFrom(ctx)
			if err != nil {
				return nil, err
			}

			numberOfFinalizers := len(newSyncerViewObject.GetFinalizers())
			removeFromSyncer := newSyncerViewObject.GetDeletionTimestamp() != nil && numberOfFinalizers == 0

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

			if bytes, err := yaml.Marshal(newKCPViewObject.UnstructuredContent()); err == nil {
				klog.V(5).Infof("Before SyncerViewTransformer - New KCP View Resource:\n%s", string(bytes))
			}

			return newKCPViewObject, nil
		},
		func(client dynamic.ResourceInterface, ctx context.Context, returnedKCPResource *unstructured.Unstructured, eventType *watch.EventType, subresources ...string) (transformedSyncerViewResource *unstructured.Unstructured, err error) {
			workloadClusterName, err := syncercontext.WorkloadClusterNameFrom(ctx)
			if err != nil {
				return nil, err
			}

			syncing := GetSyncing(returnedKCPResource)
			klog.Infof("After SyncerViewTransformer - KCP Object Syncing : %#v", syncing)

			if bytes, err := yaml.Marshal(returnedKCPResource.UnstructuredContent()); err == nil {
				klog.V(5).Infof("After SyncerViewTransformer - KCP Resource:\n%s", string(bytes))
			}

			kcpObjectJson, err := json.Marshal(returnedKCPResource.UnstructuredContent())
			if err != nil {
				return nil, err
			}

			var oldSyncerViewObject *unstructured.Unstructured
			if filteredOldSyncerViews, err := GetSyncerViews(returnedKCPResource, workloadClusterName); err != nil {
				return nil, err
			} else {
				if syncerView, exists := filteredOldSyncerViews[workloadClusterName]; exists {
					oldSyncerViewObject = &syncerView
				}
			}

			transformedSyncerViewResource, err = syncStrategy.ReadFromKCP(workloadClusterName, returnedKCPResource, oldSyncerViewObject, syncing)
			if err != nil {
				return nil, err
			}

			finalizers := sets.NewString(transformedSyncerViewResource.GetFinalizers()...)

			// TODO(david): change and validate the way finalizers are managed.
			// finalizers.Insert(syncer.SyncerFinalizer)
			transformedSyncerViewResource.SetFinalizers(finalizers.List())

			if deletionTimestamp := syncing[workloadClusterName].RequiredDeletion(); deletionTimestamp != nil {
				transformedSyncerViewResource.SetDeletionTimestamp(deletionTimestamp)
			}

			// Remove the syncer view diff annotation from the syncer view resource

			syncerViewResourceAnnotations := transformedSyncerViewResource.GetAnnotations()
			for name := range syncerViewResourceAnnotations {
				if strings.HasPrefix(name, syncerViewAnnotationPrefix) {
					delete(syncerViewResourceAnnotations, name)
				}
			}
			transformedSyncerViewResource.SetAnnotations(syncerViewResourceAnnotations)

			// Add the reverted diff annotation to the syncer view resource

			transformedJson, err := json.Marshal(transformedSyncerViewResource.UnstructuredContent())
			if err != nil {
				return nil, err
			}

			syncerViewDiff, err := jsonpatch.CreateMergePatch(kcpObjectJson, transformedJson)
			if err != nil {
				return nil, err
			}

			kcpAnnotations := returnedKCPResource.GetAnnotations()
			if kcpAnnotations == nil {
				kcpAnnotations = make(map[string]string)
			}

			kcpAnnotations[syncerViewAnnotationPrefix+workloadClusterName] = string(syncerViewDiff)
			returnedKCPResource.SetAnnotations(kcpAnnotations)
			syncerViewAnnotations := transformedSyncerViewResource.GetAnnotations()
			for name := range syncerViewAnnotations {
				if strings.HasPrefix(name, syncerViewAnnotationPrefix) {
					delete(syncerViewAnnotations, name)
				}
			}
			transformedSyncerViewResource.SetAnnotations(syncerViewAnnotations)

			// Finished adding the reverted diff annotation to the syncer view resource

			if bytes, err := yaml.Marshal(transformedSyncerViewResource.UnstructuredContent()); err == nil {
				klog.V(5).Infof("After SyncerViewTransformer - Transformed Resource:\n%s", string(bytes))
			}
			return transformedSyncerViewResource, nil
		},
	)
}

func cleanupSyncTarget(gvr schema.GroupVersionResource) transforming.Transformer {
	return transforming.Transformer{
		Name: "CleanupSyncTarget",
		AfterUpdate: func(client dynamic.ResourceInterface, ctx context.Context, updatedKCPObject *unstructured.Unstructured, options metav1.UpdateOptions, subresources []string, result *unstructured.Unstructured) (*unstructured.Unstructured, error) {
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
		},
	}
}

func StrategyTransformers(gvr schema.GroupVersionResource, syncStrategy SyncStrategy) []transforming.Transformer {
	return []transforming.Transformer{
		cleanupSyncTarget(gvr), // cleanup sync target-related labels and annotations after the UpdateStatus when the syncer view has been removed on the syncer side
		syncerViewTransformer(gvr, syncStrategy),
	}
}
