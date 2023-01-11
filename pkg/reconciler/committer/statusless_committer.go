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

package committer

import (
	"context"
	"encoding/json"
	"fmt"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
)

type StatuslessResource interface {
	runtime.Object
	metav1.Object
}

// ObjectMetaShallowCopy copies the object such that the ObjectMeta is a shallow copy that
// can be mutated without mutating the input object.
type ObjectMetaShallowCopy[R any] func(obj R) R

// ShallowCopy does a shallow copy using Golang's default assignment operator.
func ShallowCopy[R any](obj *R) *R {
	ret := new(R)
	*ret = *obj
	return ret
}

// NewStatuslessCommitter returns a function that can patch instances of status-less R.
func NewStatuslessCommitter[R StatuslessResource, P Patcher[R]](patcher ClusterPatcher[R, P], shallowCopy ObjectMetaShallowCopy[R]) func(context.Context, R, R) error {
	focusType := fmt.Sprintf("%T", new(R))
	return func(ctx context.Context, old, obj R) error {
		logger := klog.FromContext(ctx)
		clusterName := logicalcluster.From(old)

		patchBytes, err := generateStatusLessPatchAndSubResources(shallowCopy, old, obj)
		if err != nil {
			return fmt.Errorf("failed to create patch for %s %s: %w", focusType, obj.GetName(), err)
		}

		if len(patchBytes) == 0 {
			return nil
		}

		logger.V(2).Info(fmt.Sprintf("patching %s", focusType), "patch", string(patchBytes))
		_, err = patcher.Cluster(clusterName.Path()).Patch(ctx, obj.GetName(), types.MergePatchType, patchBytes, metav1.PatchOptions{})
		if err != nil {
			return fmt.Errorf("failed to patch %s %s|%s: %w", focusType, clusterName, old.GetName(), err)
		}

		return nil
	}
}

// NewStatuslessCommitterScoped returns a function that can patch instances of status-less R changes using a scoped patcher.
func NewStatuslessCommitterScoped[R StatuslessResource](patcher Patcher[R], shallowCopy ObjectMetaShallowCopy[R]) func(context.Context, R, R) error {
	focusType := fmt.Sprintf("%T", new(R))
	return func(ctx context.Context, old, obj R) error {
		logger := klog.FromContext(ctx)
		clusterName := logicalcluster.From(old)

		patchBytes, err := generateStatusLessPatchAndSubResources(shallowCopy, old, obj)
		if err != nil {
			return fmt.Errorf("failed to create patch for %s %s: %w", focusType, obj.GetName(), err)
		}

		if len(patchBytes) == 0 {
			return nil
		}

		logger.V(2).Info(fmt.Sprintf("patching %s", focusType), "patch", string(patchBytes))
		_, err = patcher.Patch(ctx, obj.GetName(), types.MergePatchType, patchBytes, metav1.PatchOptions{})
		if err != nil {
			return fmt.Errorf("failed to patch %s %s|%s: %w", focusType, clusterName, old.GetName(), err)
		}

		return nil
	}
}

func generateStatusLessPatchAndSubResources[R StatuslessResource](shallowCopy ObjectMetaShallowCopy[R], old, obj R) ([]byte, error) {
	if equality.Semantic.DeepEqual(old, obj) {
		return nil, nil
	}

	clusterName := logicalcluster.From(old)
	name := old.GetName()

	oldForPatch := shallowCopy(old)
	// to ensure they appear in the patch as preconditions
	oldForPatch.SetUID("")
	oldForPatch.SetResourceVersion("")

	oldData, err := json.Marshal(oldForPatch)
	if err != nil {
		return nil, fmt.Errorf("failed to Marshal old data for %s|%s: %w", clusterName, name, err)
	}

	newForPatch := shallowCopy(obj)
	// to ensure they appear in the patch as preconditions
	newForPatch.SetUID(old.GetUID())
	newForPatch.SetResourceVersion(old.GetResourceVersion())

	newData, err := json.Marshal(newForPatch)
	if err != nil {
		return nil, fmt.Errorf("failed to Marshal new data for %s|%s: %w", clusterName, name, err)
	}

	patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
	if err != nil {
		return nil, fmt.Errorf("failed to create patch for %s|%s: %w", clusterName, name, err)
	}

	return patchBytes, nil
}
