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

package coordination

import (
	"context"
	"reflect"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	syncercontext "github.com/kcp-dev/kcp/pkg/virtual/syncer/context"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer/transformations"
)

// FilteredSyncerViewsChanged returns true if the syncer view fields changed between old and new
// for at least one of the SyncTarget filtered by the [keepSynctarget] function.
func FilteredSyncerViewsChanged(old, new metav1.Object, keepSyncTarget func(syncTargetKey string) bool) bool {
	oldSyncerViewAnnotations := make(map[string]string)
	for name, value := range old.GetAnnotations() {
		if strings.HasPrefix(name, v1alpha1.InternalSyncerViewAnnotationPrefix) {
			if keepSyncTarget(strings.TrimPrefix(name, v1alpha1.InternalSyncerViewAnnotationPrefix)) {
				oldSyncerViewAnnotations[name] = value
			}
		}
	}

	newSyncerViewAnnotations := make(map[string]string)
	for name, value := range new.GetAnnotations() {
		if strings.HasPrefix(name, v1alpha1.InternalSyncerViewAnnotationPrefix) {
			if keepSyncTarget(strings.TrimPrefix(name, v1alpha1.InternalSyncerViewAnnotationPrefix)) {
				newSyncerViewAnnotations[name] = value
			}
		}
	}
	return !reflect.DeepEqual(oldSyncerViewAnnotations, newSyncerViewAnnotations)
}

// SyncerViewChanged returns true if the syncer view fields changed between old and new
// for at least one of the SyncTargets on which the resource is synced.
func AnySyncerViewChanged(old, new metav1.Object) bool {
	return FilteredSyncerViewsChanged(old, new, func(key string) bool {
		return true
	})
}

// SyncerViewChanged returns true if the syncer view fields changed between old and new
// for the given SyncTarget.
func SyncerViewChanged(old, new metav1.Object, syncTargetKey string) bool {
	return FilteredSyncerViewsChanged(old, new, func(key string) bool {
		return syncTargetKey == key
	})
}

type Object interface {
	metav1.Object
	runtime.Object
}

// UpstreamViewChanged check equality between old and new, ignoring the syncer view annotations.
func UpstreamViewChanged(old, new Object, equality func(old, new interface{}) bool) bool {
	old = old.DeepCopyObject().(Object)
	new = new.DeepCopyObject().(Object)

	oldAnnotations := make(map[string]string)
	for name, value := range old.GetAnnotations() {
		if !strings.HasPrefix(name, v1alpha1.InternalSyncerViewAnnotationPrefix) {
			oldAnnotations[name] = value
		}
	}
	old.SetAnnotations(oldAnnotations)

	newAnnotations := make(map[string]string)
	for name, value := range new.GetAnnotations() {
		if strings.HasPrefix(name, v1alpha1.InternalSyncerViewAnnotationPrefix) {
			newAnnotations[name] = value
		}
	}
	new.SetAnnotations(newAnnotations)

	return !equality(old, new)
}

// SyncerViewRetriever allows retrieving the syncer views on an upstream object.
// It is designed to use the same transformation and overriding logic as the one
// used by the Syncer virtual workspace.
type SyncerViewRetriever[T Object] interface {
	// GetFilteredSyncerViews retrieves the syncer views of the resource for
	// all the SyncTargets filtered by the [keepSyncTarget] function
	GetFilteredSyncerViews(ctx context.Context, gvr schema.GroupVersionResource, upstreamResource T, keepSyncTarget func(key string) bool) (map[string]T, error)
	// GetAllSyncerViews retrieves the syncer views of the resource for
	// all the SyncTargets on which the resource is being synced
	GetAllSyncerViews(ctx context.Context, gvr schema.GroupVersionResource, upstreamResource T) (map[string]T, error)
	// GetSyncerView retrieves the syncer view of the resource for
	// the given SyncTarget
	GetSyncerView(ctx context.Context, gvr schema.GroupVersionResource, upstreamResource T, syncTargetKey string) (T, error)
}

var _ SyncerViewRetriever[Object] = (*syncerViewRetriever[Object])(nil)

type syncerViewRetriever[T Object] struct {
	transformations.SyncerResourceTransformer
}

// NewSyncerViewRetriever creates a [SyncerViewRetriever] based on a given
// [transformations.TransformationProvider] and a given [transformations.SummarizingRulesProvider].
// Retrieving the syncer views on an upstream object should use the same
// transformation and overriding logic as the one used by the Syncer virtual workspace.
// So the 2 arguments should be chosen accordingly.
func NewSyncerViewRetriever[T Object](transformationProvider transformations.TransformationProvider,
	summarizingRulesprovider transformations.SummarizingRulesProvider) SyncerViewRetriever[T] {
	return &syncerViewRetriever[T]{
		transformations.SyncerResourceTransformer{
			TransformationProvider:   transformationProvider,
			SummarizingRulesProvider: summarizingRulesprovider,
		},
	}
}

// NewDefaultSyncerViewManager creates a [SyncerViewRetriever] based on the default
// transfomation and summarizing rules providers.
func NewDefaultSyncerViewManager[T Object]() SyncerViewRetriever[T] {
	return NewSyncerViewRetriever[T](&transformations.SpecDiffTransformation{}, &transformations.DefaultSummarizingRules{})
}

func toUnstructured[T Object](obj T) (*unstructured.Unstructured, error) {
	unstructured := &unstructured.Unstructured{Object: map[string]interface{}{}}
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}
	unstructured.Object = raw
	return unstructured, nil
}

func fromUnstructured[T Object](unstr *unstructured.Unstructured) (T, error) {
	var obj T
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstr.Object, &obj)
	if err != nil {
		return obj, err
	}
	return obj, nil
}

func (svm *syncerViewRetriever[T]) GetFilteredSyncerViews(ctx context.Context, gvr schema.GroupVersionResource, upstreamResource T, keepSyncTarget func(key string) bool) (map[string]T, error) {
	syncerViews := make(map[string]T)
	for name := range upstreamResource.GetAnnotations() {
		if !strings.HasPrefix(name, v1alpha1.InternalSyncerViewAnnotationPrefix) {
			continue
		}
		syncTargetKey := strings.TrimPrefix(name, v1alpha1.InternalSyncerViewAnnotationPrefix)
		if !keepSyncTarget(syncTargetKey) {
			continue
		}
		var unstrResource *unstructured.Unstructured
		if unstructured, isUnstructured := Object(upstreamResource).(*unstructured.Unstructured); isUnstructured {
			unstrResource = unstructured
		} else {
			unstructured, err := toUnstructured(upstreamResource)
			if err != nil {
				return nil, err
			}
			unstrResource = unstructured
		}
		if unstrSyncerView, err := svm.SyncerResourceTransformer.AfterRead(nil, syncercontext.WithSyncTargetKey(ctx, syncTargetKey), gvr, unstrResource, nil); err != nil {
			return nil, err
		} else {
			var syncerView T
			if typedSyncerView, isTyped := Object(unstrSyncerView).(T); isTyped {
				syncerView = typedSyncerView
			} else {
				if typedSyncerView, err = fromUnstructured[T](unstrSyncerView); err != nil {
					return nil, err
				} else {
					syncerView = typedSyncerView
				}
			}
			syncerViews[syncTargetKey] = syncerView
		}
	}
	return syncerViews, nil
}

func (svm *syncerViewRetriever[T]) GetAllSyncerViews(ctx context.Context, gvr schema.GroupVersionResource, upstreamResource T) (map[string]T, error) {
	return svm.GetFilteredSyncerViews(ctx, gvr, upstreamResource, func(key string) bool {
		return true
	})
}

func (svm *syncerViewRetriever[T]) GetSyncerView(ctx context.Context, gvr schema.GroupVersionResource, upstreamResource T, syncTargetKey string) (T, error) {
	if views, err := svm.GetFilteredSyncerViews(ctx, gvr, upstreamResource, func(key string) bool {
		return syncTargetKey == key
	}); err != nil {
		var zeroVal T
		return zeroVal, err
	} else {
		return views[syncTargetKey], nil
	}
}
